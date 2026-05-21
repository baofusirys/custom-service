package ws

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/security"
)

// Hub 是 WSS 中枢：管理所有在线连接 + Redis Pub/Sub 跨节点广播。
//
// 关键设计：
//   - 注册/注销/广播都通过单 goroutine 串行化（incoming/register/unregister），
//     避免并发 map 写锁竞争 —— 高并发下这套模型扛万级长连接稳如老狗。
//   - 实际写出去用每个 client 自己的 writePump，互不阻塞。
//   - Redis Pub/Sub 让多副本部署时跨容器消息互通。
//
// 持久化：Hub 不直接写 DB，由上层 messageSink 异步落库（见 service 层）。

type Hub struct {
	cfg HubConfig

	visitors sync.Map // visitorID -> *Client
	agents   sync.Map // agentID(string) -> *Client
	byConv   sync.Map // convID -> map[connID]*Client（一会话多端：访客 + 多客服）

	register   chan *Client
	unregister chan *Client
	incoming   chan incoming
	broadcast  chan *Envelope

	rdb  *redis.Client
	pub  string // 本节点 → 其他节点广播频道
	sub  *redis.PubSub
	sink MessageSink

	bizLog *zap.Logger
	rawLog *zap.Logger
	secLog *zap.Logger

	cipher *security.Cipher
}

type HubConfig struct {
	NodeID       string
	BizLog       *zap.Logger
	RawLog       *zap.Logger
	SecLog       *zap.Logger
	Cipher       *security.Cipher
	Redis        *redis.Client
	Sink         MessageSink
	HeartbeatSec int
}

type incoming struct {
	Client *Client
	Env    *Envelope
}

// MessageSink 由 service 层实现：异步持久化 + 离线分发。
type MessageSink interface {
	OnVisitorMessage(ctx context.Context, e *Envelope, c *Client) error
	OnAgentMessage(ctx context.Context, e *Envelope, c *Client) error
	OnVisitorConnect(ctx context.Context, c *Client) error
	OnVisitorDisconnect(ctx context.Context, c *Client) error
	OnAgentConnect(ctx context.Context, c *Client) error
	OnAgentDisconnect(ctx context.Context, c *Client) error
}

const broadcastChannel = "cs:bcast"

func NewHub(cfg HubConfig) *Hub {
	h := &Hub{
		cfg:        cfg,
		register:   make(chan *Client, 256),
		unregister: make(chan *Client, 256),
		incoming:   make(chan incoming, 4096),
		broadcast:  make(chan *Envelope, 4096),
		rdb:        cfg.Redis,
		pub:        broadcastChannel,
		sink:       cfg.Sink,
		bizLog:     cfg.BizLog,
		rawLog:     cfg.RawLog,
		secLog:     cfg.SecLog,
		cipher:     cfg.Cipher,
	}
	return h
}

// Run 启动主循环 + Redis 订阅。阻塞直到 ctx 结束。
func (h *Hub) Run(ctx context.Context) {
	h.sub = h.rdb.Subscribe(ctx, h.pub)
	defer h.sub.Close()
	go h.fanoutFromRedis(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			h.handleRegister(ctx, c)
		case c := <-h.unregister:
			h.handleUnregister(ctx, c)
		case in := <-h.incoming:
			h.handleIncoming(ctx, in)
		case e := <-h.broadcast:
			h.fanoutLocal(e)
		}
	}
}

// Register 由 handler 把新连接交给 hub。
func (h *Hub) Register(c *Client) {
	h.register <- c
	go c.writePump()
	go c.readPump()
}

func (h *Hub) handleRegister(ctx context.Context, c *Client) {
	switch c.Kind {
	case KindVisitor:
		h.visitors.Store(c.ID, c)
		// 同会话索引
		h.attachConv(c)
		_ = h.sink.OnVisitorConnect(ctx, c)
		h.bizLog.Info("visitor connected",
			zap.String("conn", c.ConnID), zap.String("vid", c.ID), zap.String("site", c.SiteID))
	case KindAgent:
		h.agents.Store(c.ID, c)
		_ = h.sink.OnAgentConnect(ctx, c)
		h.bizLog.Info("agent connected",
			zap.String("conn", c.ConnID), zap.String("aid", c.ID))
	}
	c.Send(&Envelope{
		Type:     "hello",
		ID:       uuid.NewString(),
		TS:       NowMS(),
		ConvID:   c.ConvID,
		Priority: 0,
		Extra: map[string]any{
			"conn_id":         c.ConnID,
			"heartbeat_sec":   30,
			"server_now":      time.Now().In(beijing()).Format("2006-01-02 15:04:05"),
			"server_node":     h.cfg.NodeID,
		},
	})
}

func (h *Hub) handleUnregister(ctx context.Context, c *Client) {
	switch c.Kind {
	case KindVisitor:
		if v, ok := h.visitors.Load(c.ID); ok && v == c {
			h.visitors.Delete(c.ID)
		}
		h.detachConv(c)
		_ = h.sink.OnVisitorDisconnect(ctx, c)
		h.bizLog.Info("visitor disconnected",
			zap.String("conn", c.ConnID), zap.String("vid", c.ID))
	case KindAgent:
		if v, ok := h.agents.Load(c.ID); ok && v == c {
			h.agents.Delete(c.ID)
		}
		_ = h.sink.OnAgentDisconnect(ctx, c)
		h.bizLog.Info("agent disconnected",
			zap.String("conn", c.ConnID), zap.String("aid", c.ID))
	}
}

func (h *Hub) attachConv(c *Client) {
	if c.ConvID == "" {
		return
	}
	m, _ := h.byConv.LoadOrStore(c.ConvID, &sync.Map{})
	m.(*sync.Map).Store(c.ConnID, c)
}

func (h *Hub) detachConv(c *Client) {
	if c.ConvID == "" {
		return
	}
	if m, ok := h.byConv.Load(c.ConvID); ok {
		m.(*sync.Map).Delete(c.ConnID)
	}
}

// AttachAgentToConv 客服动态加入一个会话（接管访客）。
func (h *Hub) AttachAgentToConv(agentID, convID string) {
	if v, ok := h.agents.Load(agentID); ok {
		c := v.(*Client)
		// 先卸下旧会话
		h.detachConv(c)
		c.ConvID = convID
		h.attachConv(c)
	}
}

func (h *Hub) handleIncoming(ctx context.Context, in incoming) {
	e := in.Env
	c := in.Client

	// 服务器盖时间戳（防客户端伪造）+ 强制北京时
	e.TS = NowMS()
	if e.ID == "" {
		e.ID = uuid.NewString()
	}

	switch e.Type {
	case "ping":
		c.Send(&Envelope{Type: "pong", ID: e.ID, TS: e.TS, Priority: 0})
		return
	case "chat":
		if c.Kind == KindVisitor {
			e.From = "visitor:" + c.ID
			e.ConvID = c.ConvID
			if err := h.sink.OnVisitorMessage(ctx, e, c); err != nil {
				h.bizLog.Error("sink visitor msg err", zap.Error(err))
			}
		} else {
			e.From = "agent:" + c.ID
			if err := h.sink.OnAgentMessage(ctx, e, c); err != nil {
				h.bizLog.Error("sink agent msg err", zap.Error(err))
			}
		}
		h.FanoutToConv(ctx, e)
	case "typing", "read":
		// 仅转发，不落库
		h.FanoutToConv(ctx, e)
	default:
		h.bizLog.Warn("unknown ws type", zap.String("type", e.Type), zap.String("conn", c.ConnID))
	}
}

// FanoutToConv 给会话内所有成员投递；同时通过 Redis 广播到其他节点。
func (h *Hub) FanoutToConv(ctx context.Context, e *Envelope) {
	e.Priority = 0 // 所有实时聊天都走高优队列
	h.fanoutLocal(e)
	if h.rdb != nil {
		_ = h.rdb.Publish(ctx, h.pub, mustJSON(e)).Err()
	}
}

func (h *Hub) fanoutLocal(e *Envelope) {
	if e.ConvID == "" {
		return
	}
	if m, ok := h.byConv.Load(e.ConvID); ok {
		m.(*sync.Map).Range(func(_, v any) bool {
			v.(*Client).Send(e)
			return true
		})
	}
}

func (h *Hub) fanoutFromRedis(ctx context.Context) {
	ch := h.sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-ch:
			if m == nil {
				return
			}
			var e Envelope
			if err := fromJSON([]byte(m.Payload), &e); err != nil {
				h.bizLog.Warn("bad redis bcast", zap.Error(err))
				continue
			}
			h.fanoutLocal(&e)
		}
	}
}

// OnlineAgentCount 公开 API：当前在线客服数。
func (h *Hub) OnlineAgentCount() int {
	n := 0
	h.agents.Range(func(_, _ any) bool { n++; return true })
	return n
}

// OnlineVisitorCount 公开 API：当前在线访客数。
func (h *Hub) OnlineVisitorCount() int {
	n := 0
	h.visitors.Range(func(_, _ any) bool { n++; return true })
	return n
}

// PushToAgent 服务端主动给客服推消息（如有未分配会话）。
func (h *Hub) PushToAgent(agentID string, e *Envelope) bool {
	if v, ok := h.agents.Load(agentID); ok {
		v.(*Client).Send(e)
		return true
	}
	return false
}

// PushToVisitor 服务端主动给访客推消息（如客服离线时的系统提示）。
func (h *Hub) PushToVisitor(visitorID string, e *Envelope) bool {
	if v, ok := h.visitors.Load(visitorID); ok {
		v.(*Client).Send(e)
		return true
	}
	return false
}

func beijing() *time.Location {
	tz, _ := time.LoadLocation("Asia/Shanghai")
	return tz
}
