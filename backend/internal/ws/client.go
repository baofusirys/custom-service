package ws

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/security"
)

const (
	writeWait       = 10 * time.Second
	pongWait        = 70 * time.Second
	pingPeriod      = 30 * time.Second
	maxMessageBytes = 64 * 1024 // 单条消息 64KB 上限，防内存炸
	sendBufHigh     = 256       // 单连接高优队列长度
	sendBufLow      = 1024      // 单连接低优队列长度
)

type Kind int

const (
	KindVisitor Kind = 1
	KindAgent   Kind = 2
)

type outboundFrame struct {
	data    []byte
	traceID string
}

type Client struct {
	hub  *Hub
	conn *websocket.Conn

	ConnID string
	Kind   Kind
	ID     string // visitor id 或 agent id
	SiteID string // 关联站点
	convMu sync.RWMutex
	convID string // 当前会话（visitor 必带，agent 动态切换）

	// 双优先级出队（爷爷需求：消息处理顺序 WSS 优先 ——
	// WSS 即时消息走 high；DB 加载历史等慢通道走 low）
	high chan outboundFrame
	low  chan outboundFrame

	closed atomic.Bool
	log    *zap.Logger
	rawLog *zap.Logger
}

func newClient(h *Hub, conn *websocket.Conn, kind Kind, id, site, conv, connID string, log, rawLog *zap.Logger) *Client {
	return &Client{
		hub:    h,
		conn:   conn,
		Kind:   kind,
		ID:     id,
		SiteID: site,
		convID: conv,
		ConnID: connID,
		high:   make(chan outboundFrame, sendBufHigh),
		low:    make(chan outboundFrame, sendBufLow),
		log:    log,
		rawLog: rawLog,
	}
}

// ConversationID/SetConversationID 统一保护动态会话绑定；HTTP assign、Hub 和持久化工作协程可并发访问。
func (c *Client) ConversationID() string {
	c.convMu.RLock()
	defer c.convMu.RUnlock()
	return c.convID
}

func (c *Client) SetConversationID(convID string) {
	c.convMu.Lock()
	c.convID = convID
	c.convMu.Unlock()
}

// Send 往该连接异步发消息；返回是否成功进入发送队列。
// high 队列满 → 关连接（防慢客户端拖累全局），调用方可据此统计真实入队数。
func (c *Client) Send(env *Envelope) bool {
	if c.closed.Load() {
		return false
	}
	data, err := json.Marshal(env)
	if err != nil {
		c.log.Error("client envelope marshal failed", zap.Error(err), zap.String("conn", c.ConnID))
		return false
	}
	q := c.high
	if env.Priority >= 1 {
		q = c.low
	}
	traceID := env.ID
	if traceID == "" {
		traceID = c.ConnID
	}
	select {
	case q <- outboundFrame{data: data, traceID: traceID}:
		return true
	default:
		// 队列满：保护性断连，避免阻塞 hub
		c.log.Warn("client send queue full, dropping",
			zap.String("conn", c.ConnID), zap.Int("kind", int(c.Kind)))
		c.close()
		return false
	}
}

func (c *Client) close() {
	if c.closed.Swap(true) {
		return
	}
	_ = c.conn.Close()
	c.hub.unregister <- c
}

// readPump：阻塞读，把消息分发到 hub。
func (c *Client) readPump() {
	defer c.close()
	c.conn.SetReadLimit(maxMessageBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.log.Info("ws read close", zap.String("conn", c.ConnID), zap.Error(err))
			}
			return
		}
		var env Envelope
		decodeErr := json.Unmarshal(raw, &env)
		traceID := env.ID
		if traceID == "" {
			traceID = c.ConnID
		}
		// 原始报文长效落盘；解析失败也保留原帧，并使用连接 ID 贯通排查。
		c.rawLog.Info("WebSocket frame received",
			zap.String("event", "ws_frame_rx"), zap.String("trace_id", traceID), zap.String("request_id", traceID),
			zap.String("conn", c.ConnID), zap.Int("kind", int(c.Kind)), zap.String("actor_id", c.ID),
			zap.ByteString("payload", security.RedactJSONPayload(raw)))
		if decodeErr != nil {
			c.log.Warn("ws malformed json",
				zap.String("conn", c.ConnID), zap.Error(decodeErr))
			continue
		}
		c.hub.incoming <- incoming{Client: c, Env: &env}
	}
}

// writePump：把 high/low 两条队列写出去；high 优先级永远先发。
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()

	send := func(frame outboundFrame) bool {
		_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
		if err := c.conn.WriteMessage(websocket.TextMessage, frame.data); err != nil {
			return false
		}
		c.rawLog.Info("WebSocket frame sent",
			zap.String("event", "ws_frame_tx"), zap.String("trace_id", frame.traceID), zap.String("request_id", frame.traceID),
			zap.String("conn", c.ConnID), zap.Int("kind", int(c.Kind)), zap.String("actor_id", c.ID),
			zap.ByteString("payload", security.RedactJSONPayload(frame.data)))
		return true
	}

	for {
		// 高优先级优先消费（在低优有积压时也能立即处理 WSS 新消息）。
		select {
		case msg, ok := <-c.high:
			if !ok {
				return
			}
			if !send(msg) {
				return
			}
			// 把当前批次的高优消息一次性榨干（限 32 条避免 starvation）
			for i := 0; i < 32; i++ {
				select {
				case m2 := <-c.high:
					if !send(m2) {
						return
					}
				default:
					i = 32
				}
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		default:
			// 高优为空，再轮询低优 + 心跳
			select {
			case msg := <-c.high:
				if !send(msg) {
					return
				}
			case msg := <-c.low:
				if !send(msg) {
					return
				}
			case <-ticker.C:
				_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}
}
