package ws

import (
	"context"
	"strings"
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
	// agents: agentID(string) -> *sync.Map[connID]*Client
	// 一个 agentID 可以有多个 WSS 连接（同账号在 web + app 双端同时登录），
	// 所有连接都会收到 BroadcastToAllAgents / fanoutLocal 的外溢。
	agents sync.Map
	byConv sync.Map // convID -> *sync.Map[connID]*Client（一会话多端：访客 + 多客服）

	// pendingCalls: 进行中的语音来电 buffer（30 秒 TTL）。
	// 解决问题：voice_call 是一次性广播；客服 iPhone 收推送拉起 App 时 WSS 重连后
	// 会错过旧的 voice_call。register agent 时扫描 buffer，重投未过期的 call。
	// 收到 voice_accept/reject/end 时删除对应 call_id。
	pendingCalls sync.Map // map[callID(string)]*pendingCall

	// finishedCalls: 已通知 service 写过 sys 消息的 callID，5 分钟 dedup TTL。
	// 跟 pendingCalls 解耦：pendingCalls 30s 后由 AfterFunc 强制清，无法靠它去重晚到的 voice_end。
	// 双方同时挂断时，第二个 voice_end 凭这里的标志跳过，避免重复写 sys。
	finishedCalls sync.Map // map[callID(string)]time.Time

	// [069] pendingAccepts: agent 已 voice_accept 但还没回 voice_answer 的中间态。
	// 关键场景：客服点了「接听」按钮，hub 已把 voice_accept 转发给 visitor，但 agent 端
	// WebRTC SDP 协商（setRemoteDescription/createAnswer/setLocalDescription 三段）任一抛错、
	// 或麦克风未授权、或 PC 创建失败，都会导致 voice_answer 永远不来，visitor 端原本要等
	// 30 秒 ringing 超时才弹「未接听」，体验极差。
	//
	// 解决方案：voice_accept 到达时 Store(callID) + AfterFunc(5s) 启动看门狗；
	// 5 秒内若没收到 voice_answer，主动 fanout voice_finished(reason=agent_no_answer_5s)，
	// 双方 UI 在 5 秒内关闭通话浮窗 + 显示「客服 5 秒未应答自动挂断」。
	//
	// reason enum（与 service.codeToText / mobile_app voice_controller 三端对齐）：
	//   - agent_no_answer_5s   客服 accept 后 5s 内未回 voice_answer（本看门狗触发）
	//   - mic_permission_denied 客服麦克风权限被拒（agent 上报 voice_accept_failed）
	//   - mic_busy             麦克风被占用
	//   - mic_hardware_error   麦克风硬件异常
	//   - no_audio_tracks      未能获取音频通道
	//   - signal_exception     WebRTC 信令异常（agent 上报 voice_signal_error）
	//   - no_answer_sdp        应答 SDP 解析失败
	//   - no_ice_candidate     ICE 候选超时
	//   - ice_disconnected     网络中断
	//   - normal_hangup        正常挂断（兼容老路径，等价于无 reason）
	pendingAccepts sync.Map // map[callID(string)]*pendingAccept
	// [069] acceptTimers: 持有 5s 看门狗 timer 句柄；voice_answer 到达时 Stop，避免误触发。
	acceptTimers sync.Map // map[callID(string)]*time.Timer

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

// MessageSink 由 service 层实现。
//
// 设计原则（爷爷需求：消息处理顺序 WSS 优先）：
//   - Preprocess 阶段「同步」：必须先做的限流 + 内容清洗 + 注入检测。
//     返回 false 表示消息被拒（如限流），Hub 不再广播。
//   - Persist 阶段「异步」：入库（潜在慢操作）；Hub 先 Fanout 再触发 Persist。
//     这样实时通道永远不会被 DB 写入拖累，万人秒达的保证。
type MessageSink interface {
	OnVisitorConnect(ctx context.Context, c *Client) error
	OnVisitorDisconnect(ctx context.Context, c *Client) error
	OnAgentConnect(ctx context.Context, c *Client) error
	OnAgentDisconnect(ctx context.Context, c *Client) error

	PreprocessVisitorMessage(ctx context.Context, e *Envelope, c *Client) bool
	PreprocessAgentMessage(ctx context.Context, e *Envelope, c *Client) bool
	PersistMessageAsync(e *Envelope, c *Client, sender string)

	// PersistReadAsync 异步落库「读到时刻」：把指定 role 的 last_read_*_at 推到现在。
	// role: "agent" 或 "visitor"。失败仅记日志，不阻塞 WSS 广播。
	PersistReadAsync(e *Envelope, c *Client, role string)

	// OnPageNavigation 访客跳转到新页面时触发：异步落库一条 sys 消息 +
	// 广播 page_navigation 事件给所有在线 agent。
	OnPageNavigation(visitorID, convID, url, title string)

	// OnVisitorVoiceCall 访客发起语音呼叫时触发：service 用来发 luckfast 推送
	// 给客服 iPhone（让锁屏/后台时也能收到通知，点击拉起 App 接听）。
	// 仅信令转发已经在 fanoutVoice 完成；此回调不影响 WSS 流程，仅用于侧通道推送。
	OnVisitorVoiceCall(visitorID, callID string)

	// OnVoiceCallFinished 语音通话终结（任何一方挂断 / 拒绝 / 未接 / 失败）时触发：
	// service 写一条 sys 系统消息到该 visitor 的当前会话 + WSS 广播给会话成员实时显示。
	// code: no_answer / rejected / busy / cancel / hangup / failed
	// reason: 细化原因 enum（见 [069] pendingAccepts 上方注释），空串表示无细化原因。
	//   service 层会优先按 reason 渲染中文文本；reason 为空时 fallback 到 code 的旧文案。
	// durSec: 仅 code=hangup 时有意义（通话时长）
	OnVoiceCallFinished(visitorID, callID, code, reason string, durSec int)
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
		// 多连接：把这个 conn 加进该 agentID 的 conn map（不覆盖已有连接）
		connsMap, _ := h.agents.LoadOrStore(c.ID, &sync.Map{})
		connsMap.(*sync.Map).Store(c.ConnID, c)
		_ = h.sink.OnAgentConnect(ctx, c)
		h.bizLog.Info("agent connected",
			zap.String("conn", c.ConnID), zap.String("aid", c.ID))
		// 重投未过期的 pending voice_call：解决「客服 iPhone 收推送拉起 App 后
		// WSS 重连，但旧 voice_call 已经过去、App 内看不到来电浮窗」的问题
		now := time.Now()
		h.pendingCalls.Range(func(_, v any) bool {
			pc := v.(*pendingCall)
			if pc.expires.After(now) {
				envCopy := pc.env // 值拷贝再取地址发送
				c.Send(&envCopy)
				h.bizLog.Info("replay pending voice_call",
					zap.String("conn", c.ConnID), zap.String("aid", c.ID),
					zap.String("call_id", extractCallID(&envCopy)))
			}
			return true
		})
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
		// 多连接：只删除当前这个 conn；如果该 agent 没有任何连接了再删 agentID
		if v, ok := h.agents.Load(c.ID); ok {
			m := v.(*sync.Map)
			m.Delete(c.ConnID)
			empty := true
			m.Range(func(_, _ any) bool { empty = false; return false })
			if empty {
				h.agents.Delete(c.ID)
			}
		}
		h.detachConv(c)
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
// 同 agentID 的所有连接都会被同时 attach（保证多端一致）。
func (h *Hub) AttachAgentToConv(agentID, convID string) {
	if v, ok := h.agents.Load(agentID); ok {
		v.(*sync.Map).Range(func(_, cv any) bool {
			c := cv.(*Client)
			h.detachConv(c)
			c.ConvID = convID
			h.attachConv(c)
			return true
		})
	}
}

func (h *Hub) handleIncoming(ctx context.Context, in incoming) {
	e := in.Env
	c := in.Client

	// 服务器盖时间戳（防客户端伪造）+ 强制北京时 + 盖发起方 ConnID（多端去重用）
	e.TS = NowMS()
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	e.ConnID = c.ConnID

	switch e.Type {
	case "ping":
		c.Send(&Envelope{Type: "pong", ID: e.ID, TS: e.TS, Priority: 0})
		return
	case "chat":
		// ============================================================
		// 顺序（WSS 优先，爷爷需求）：
		//   1) Preprocess 同步：限流 + 清洗（必须先做，挡住违规消息）
		//   2) Fanout 立即广播：实时通道不被 DB 写入拖累
		//   3) Persist 异步：后台 goroutine 入库
		// ============================================================
		var sender string
		if c.Kind == KindVisitor {
			e.From = "visitor:" + c.ID
			e.ConvID = c.ConvID
			sender = "visitor"
			if !h.sink.PreprocessVisitorMessage(ctx, e, c) {
				return // 被限流或拒绝，不广播也不入库
			}
		} else {
			e.From = "agent:" + c.ID
			sender = "agent"
			if !h.sink.PreprocessAgentMessage(ctx, e, c) {
				return
			}
		}
		// 立即广播（实时 WSS 优先）
		h.FanoutToConv(ctx, e)
		// 异步入库（不阻塞实时通道）
		h.sink.PersistMessageAsync(e, c, sender)
	case "read":
		// 已读事件：盖发送者 + 异步落库 last_read_*_at + 广播给会话内对端
		var role string
		if c.Kind == KindVisitor {
			e.From = "visitor:" + c.ID
			role = "visitor"
		} else {
			e.From = "agent:" + c.ID
			role = "agent"
		}
		if c.ConvID != "" {
			e.ConvID = c.ConvID
		}
		h.sink.PersistReadAsync(e, c, role)
		h.FanoutToConv(ctx, e)
	case "page":
		// 访客跳转页面：只接受 visitor 上报；agent 不允许伪造页面事件
		if c.Kind != KindVisitor || c.ConvID == "" {
			return
		}
		var url, title string
		if m, ok := e.Extra.(map[string]any); ok {
			if v, ok := m["url"].(string); ok {
				url = v
			}
			if v, ok := m["title"].(string); ok {
				title = v
			}
		}
		h.sink.OnPageNavigation(c.ID, c.ConvID, url, title)
	case "typing":
		// 仅转发，不落库
		h.FanoutToConv(ctx, e)
	case "voice_call", "voice_accept", "voice_reject", "voice_taken",
		"voice_offer", "voice_answer", "voice_ice", "voice_end":
		// 语音通话信令：纯信令，后端无状态转发，不落库
		//   - voice_call (To 空): 访客发起 → 广播给所有 agent (来电浮窗)
		//   - voice_accept/reject (To=visitor:xx): agent 接听/拒绝 → 直推 visitor
		//   - voice_offer/answer/ice: WebRTC SDP/ICE 信令 → 根据 To 字段点对点转发
		//   - voice_end (To=对方): 任何一方挂断
		// 业务侧（service.go）目前不参与 voice 流程；P2P 音频流由 WebRTC 直连，不过后端
		if c.Kind == KindVisitor {
			e.From = "visitor:" + c.ID
		} else {
			e.From = "agent:" + c.ID
		}
		h.fanoutVoice(ctx, e)
		// voice_* 状态机：
		//   voice_call: 存 buffer (30s TTL) + 触发 APNs 推送
		//   voice_accept: 延长 buffer 到 30 分钟（通话期间，agent 重连还能收到旧 call）
		//   voice_end / voice_reject: 通知 service 写 sys 消息 + finished dedup + 清 buffer
		switch e.Type {
		case "voice_call":
			if c.Kind == KindVisitor {
				callID := extractCallID(e)
				if callID != "" {
					h.pendingCalls.Store(callID, &pendingCall{
						env:       *e,
						visitorID: c.ID,
						expires:   time.Now().Add(callBufferTTL),
					})
					// 30 秒后未接通 → 自动清理
					time.AfterFunc(callBufferTTL, func() {
						h.pendingCalls.Delete(callID)
					})
				}
				h.sink.OnVisitorVoiceCall(c.ID, callID)
			}
		case "voice_accept":
			// 接听后通话可能很长，buffer TTL 延到 30 分钟，避免 voice_end 来时找不到 visitorID
			cid := extractCallID(e)
			if cid == "" {
				break
			}
			var visitorID string
			if v, ok := h.pendingCalls.Load(cid); ok {
				pc := v.(*pendingCall)
				pc.expires = time.Now().Add(callTalkingTTL)
				visitorID = pc.visitorID
			}
			// [069] 启动 5s 看门狗：voice_accept 之后等 voice_answer
			// 5 秒内没回 answer 即视为客服端 SDP 协商失败，主动 fanout finished 让 visitor
			// 端瞬间显示「客服 5 秒未应答自动挂断」，告别原 30s 等 ringing 超时的死等体验
			pa := &pendingAccept{
				callID:    cid,
				visitorID: visitorID,
				agentFrom: e.From,
				startedAt: time.Now(),
			}
			h.pendingAccepts.Store(cid, pa)
			// dedup 保险：同 callID 重复 voice_accept（agent 多端同时点接听）先 Stop 旧 timer
			if oldT, ok := h.acceptTimers.LoadAndDelete(cid); ok {
				oldT.(*time.Timer).Stop()
			}
			timer := time.AfterFunc(acceptAnswerTimeout, func() {
				h.fireAcceptWatchdog(cid)
			})
			h.acceptTimers.Store(cid, timer)
			h.bizLog.Info("voice_accept watchdog armed",
				zap.String("call_id", cid),
				zap.String("agent_from", e.From),
				zap.String("visitor_id", visitorID),
				zap.Duration("timeout", acceptAnswerTimeout))
		case "voice_answer":
			// [069] 答应到了：Stop 看门狗，清 pendingAccepts
			if cid := extractCallID(e); cid != "" {
				if t, ok := h.acceptTimers.LoadAndDelete(cid); ok {
					t.(*time.Timer).Stop()
				}
				if _, ok := h.pendingAccepts.LoadAndDelete(cid); ok {
					h.bizLog.Info("voice_answer arrived, watchdog cancelled",
						zap.String("call_id", cid))
				}
			}
		case "voice_end", "voice_reject":
			// [069] 任何一方主动挂断 / 拒绝 → 取消看门狗（避免 5s 后误触发又 fanout 一次 finished）
			if cid := extractCallID(e); cid != "" {
				if t, ok := h.acceptTimers.LoadAndDelete(cid); ok {
					t.(*time.Timer).Stop()
				}
				h.pendingAccepts.Delete(cid)
			}
			// 第一次到达 → 调 service 写 sys 消息 + 记 finishedCalls；后续 5 分钟内重复来的跳过
			if cid := extractCallID(e); cid != "" {
				// dedup：双方同时挂断时第二个 voice_end 在这里被拦截
				if _, done := h.finishedCalls.Load(cid); done {
					break
				}
				var visitorID string
				if v, ok := h.pendingCalls.Load(cid); ok {
					pc := v.(*pendingCall)
					visitorID = pc.visitorID
				} else {
					// buffer 过期或重启了，fallback 从 envelope 推 visitor
					visitorID = extractVisitorID(e)
				}
				if visitorID == "" {
					break
				}
				code := extractCode(e)
				if code == "" {
					// 客户端没传 code（旧版本兼容）：voice_reject 默认 rejected，voice_end 默认 hangup
					if e.Type == "voice_reject" {
						code = "rejected"
					} else {
						code = "hangup"
					}
				}
				// 进入 dedup map（5 分钟自动清），并清掉 pendingCalls
				h.finishedCalls.Store(cid, time.Now())
				time.AfterFunc(5*time.Minute, func() {
					h.finishedCalls.Delete(cid)
				})
				h.pendingCalls.Delete(cid)
				// [069] reason 取自 envelope.Extra.reason；客户端没传走 normal_hangup（向后兼容）
				reason := extractReason(e)
				if reason == "" {
					reason = "normal_hangup"
				}
				h.sink.OnVoiceCallFinished(visitorID, cid, code, reason, extractDurationSec(e))
			}
		}
	default:
		h.bizLog.Warn("unknown ws type", zap.String("type", e.Type), zap.String("conn", c.ConnID))
	}
}

// pendingCall 待接听的来电。env 用值类型避免被后续 handleIncoming 改字段污染。
// visitorID: voice_call 发起者，service 写 sys 消息时找会话用
// 去重靠 Hub.finishedCalls，不在这里存
type pendingCall struct {
	env       Envelope
	visitorID string
	expires   time.Time
}

// [069] pendingAccept 记录 agent voice_accept 已到但 voice_answer 未到的中间态。
// 5 秒看门狗超时即 fanout voice_finished(reason=agent_no_answer_5s)，写 sys 消息让访客
// 端立刻关浮窗 + 显示「客服 5 秒未应答自动挂断」，取代原 30s ringing 超时的死等体验。
type pendingAccept struct {
	callID    string
	visitorID string    // 找会话用；voice_accept 时从 pendingCalls 同步
	agentFrom string    // "agent:xxx"，看门狗触发时回推给 agent
	startedAt time.Time // accept 到达时刻
}

// 初始 TTL = 30 秒（等待接听窗口）；voice_accept 后延长到 30 分钟（通话进行中）
const callBufferTTL = 30 * time.Second
const callTalkingTTL = 30 * time.Minute

// [069] acceptAnswerTimeout: voice_accept 后等 voice_answer 的最大时长。
// 超时即视为客服端 SDP 协商挂死（mic 未授权 / setRemoteDescription 抛错 / PC 创建失败等），
// 主动 fanout voice_finished 让双方瞬间结束通话流程。
const acceptAnswerTimeout = 5 * time.Second

// extractCallID 从 envelope 的 Extra 里取 call_id
func extractCallID(e *Envelope) string {
	if m, ok := e.Extra.(map[string]any); ok {
		if v, ok := m["call_id"].(string); ok {
			return v
		}
	}
	return ""
}

// [069] extractReason 从 envelope.Extra 取 reason（voice_signal_error / voice_accept_failed /
// voice_end 都可携带；语音通话终结的细化原因 enum，详见 Hub.pendingAccepts 上方注释）。
func extractReason(e *Envelope) string {
	if m, ok := e.Extra.(map[string]any); ok {
		if v, ok := m["reason"].(string); ok {
			return v
		}
	}
	return ""
}

// [069] fireAcceptWatchdog 5 秒看门狗到期回调。
//
// 触发路径：voice_accept 到达 → AfterFunc(5s) → 此函数。
// 行为：
//  1. 用 LoadAndDelete 同时检查并取消 pendingAccepts；若已被 voice_answer / voice_end /
//     voice_signal_error 清掉则 no-op（race-safe）。
//  2. 通过 finishedCalls dedup：本看门狗触发后写入 finishedCalls，后续晚到的 voice_answer /
//     voice_end 会被 dedup 拦截，不会再次落 sys 消息。
//  3. fanout voice_finished(reason=agent_no_answer_5s, code=no_answer) 给访客 + 客服两端，
//     UI 立刻关通话浮窗 + 弹中文原因。
//  4. 调 sink.OnVoiceCallFinished 落 sys 消息到会话历史（reason 进 SenderRef "voice:reason"）。
func (h *Hub) fireAcceptWatchdog(callID string) {
	v, stillPending := h.pendingAccepts.LoadAndDelete(callID)
	h.acceptTimers.Delete(callID)
	if !stillPending {
		return // voice_answer 已到，或 voice_end 已拆，已被取消
	}
	pa := v.(*pendingAccept)
	// dedup 保险：voice_end 已处理过就别再写一次（双保险，正常路径下 LoadAndDelete 已挡住）
	if _, done := h.finishedCalls.Load(callID); done {
		return
	}
	h.bizLog.Warn("voice_accept watchdog fired (agent_no_answer_5s)",
		zap.String("call_id", callID),
		zap.String("visitor_id", pa.visitorID),
		zap.String("agent_from", pa.agentFrom),
		zap.Duration("waited", time.Since(pa.startedAt)))
	// 写 finishedCalls 防 voice_end / voice_answer 晚到再次重复处理
	h.finishedCalls.Store(callID, time.Now())
	time.AfterFunc(5*time.Minute, func() {
		h.finishedCalls.Delete(callID)
	})
	h.pendingCalls.Delete(callID)

	const (
		code   = "no_answer"
		reason = "agent_no_answer_5s"
		phase  = "await_voice_answer"
	)
	// 1) fanout 给 visitor（关浮窗 + 弹中文原因）
	if pa.visitorID != "" {
		envV := &Envelope{
			Type: "voice_finished",
			From: "sys",
			To:   "visitor:" + pa.visitorID,
			TS:   NowMS(),
			Extra: map[string]any{
				"call_id":  callID,
				"code":     code,
				"reason":   reason,
				"phase":    phase,
				"duration": 0,
			},
		}
		ctxV, cancelV := context.WithTimeout(context.Background(), 2*time.Second)
		h.fanoutVoice(ctxV, envV)
		cancelV()
	}
	// 2) fanout 给 agent（关来电浮窗）
	if pa.agentFrom != "" {
		envA := &Envelope{
			Type: "voice_finished",
			From: "sys",
			To:   pa.agentFrom,
			TS:   NowMS(),
			Extra: map[string]any{
				"call_id":  callID,
				"code":     code,
				"reason":   reason,
				"phase":    phase,
				"duration": 0,
			},
		}
		ctxA, cancelA := context.WithTimeout(context.Background(), 2*time.Second)
		h.fanoutVoice(ctxA, envA)
		cancelA()
	}
	// 3) 落 sys 消息到会话历史
	if h.sink != nil && pa.visitorID != "" {
		h.sink.OnVoiceCallFinished(pa.visitorID, callID, code, reason, 0)
	}
}

// extractCode 从 envelope.Extra 取 code（voice_end / voice_reject 用，决定 sys 消息文字）
func extractCode(e *Envelope) string {
	if m, ok := e.Extra.(map[string]any); ok {
		if v, ok := m["code"].(string); ok {
			return v
		}
	}
	return ""
}

// extractDurationSec 从 envelope.Extra 取 duration 秒数（voice_end hangup 用）
func extractDurationSec(e *Envelope) int {
	if m, ok := e.Extra.(map[string]any); ok {
		if v, ok := m["duration"]; ok {
			switch x := v.(type) {
			case float64:
				return int(x)
			case int:
				return x
			}
		}
	}
	return 0
}

// extractVisitorID 从 envelope From/To 推涉及的 visitor（fallback 用，buffer 没命中时）
func extractVisitorID(e *Envelope) string {
	if strings.HasPrefix(e.From, "visitor:") {
		return strings.TrimPrefix(e.From, "visitor:")
	}
	if strings.HasPrefix(e.To, "visitor:") {
		return strings.TrimPrefix(e.To, "visitor:")
	}
	return ""
}

// fanoutVoice 语音通话信令路由：根据 To 字段精确转发；To 为空时广播给所有 agent
// (voice_call 场景，让所有客服都收到来电弹窗)。同时 Redis 跨节点广播。
func (h *Hub) fanoutVoice(ctx context.Context, e *Envelope) {
	e.Node = h.cfg.NodeID
	e.Priority = 0
	h.fanoutVoiceLocal(e)
	if h.rdb != nil {
		_ = h.rdb.Publish(ctx, h.pub, mustJSON(e)).Err()
	}
}

// fanoutVoiceLocal 只在本节点投递（被 fanoutFromRedis 跨节点回环时也复用此函数）
func (h *Hub) fanoutVoiceLocal(e *Envelope) {
	if e.To == "" {
		// voice_call: 广播给所有 agent 的所有连接
		h.agents.Range(func(_, v any) bool {
			v.(*sync.Map).Range(func(_, cv any) bool {
				cv.(*Client).Send(e)
				return true
			})
			return true
		})
		return
	}
	if strings.HasPrefix(e.To, "visitor:") {
		vid := strings.TrimPrefix(e.To, "visitor:")
		if v, ok := h.visitors.Load(vid); ok {
			v.(*Client).Send(e)
		}
		return
	}
	if strings.HasPrefix(e.To, "agent:") {
		aid := strings.TrimPrefix(e.To, "agent:")
		if m, ok := h.agents.Load(aid); ok {
			m.(*sync.Map).Range(func(_, cv any) bool {
				cv.(*Client).Send(e)
				return true
			})
		}
		return
	}
}

// FanoutToConv 给会话内所有成员投递；同时通过 Redis 广播到其他节点。
//
// 单节点部署时 Redis 订阅了自己 publish 的频道（环回），
// 所以必须给消息盖节点 ID，订阅端遇到自己节点的回环要跳过 —— 否则消息会在本节点被广播 2 次。
func (h *Hub) FanoutToConv(ctx context.Context, e *Envelope) {
	e.Priority = 0 // 所有实时聊天都走高优队列
	e.Node = h.cfg.NodeID
	h.fanoutLocal(e)
	if h.rdb != nil {
		_ = h.rdb.Publish(ctx, h.pub, mustJSON(e)).Err()
	}
}

// fanoutLocal 给本节点对应连接投递消息。
//
// 投递策略：
//  1. byConv[ConvID] 内所有连接：访客 + 已接管该会话的客服
//  2. 所有在线 agent 的所有连接：让 chat 和 read 都能在双端实时同步
//     - chat 外溢：未接管的客服也能立刻看到「新消息 + unread+1 + 会话上浮」
//     - read 外溢：同账号在另一端（web/app）读了消息时，本端能同步清未读
//  3. typing 不外溢（节省带宽，且只有正在打字的会话才关心）
//
// 用 ConnID 去重，每个连接只收到一份。
func (h *Hub) fanoutLocal(e *Envelope) {
	if e.ConvID == "" {
		return
	}
	sent := make(map[string]struct{}, 8)
	if m, ok := h.byConv.Load(e.ConvID); ok {
		m.(*sync.Map).Range(func(_, v any) bool {
			c := v.(*Client)
			c.Send(e)
			sent[c.ConnID] = struct{}{}
			return true
		})
	}
	if e.Type == "chat" || e.Type == "read" {
		h.agents.Range(func(_, v any) bool {
			v.(*sync.Map).Range(func(_, cv any) bool {
				c := cv.(*Client)
				if _, dup := sent[c.ConnID]; !dup {
					c.Send(e)
				}
				return true
			})
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
			// 跳过本节点自己的回环（已经在 FanoutToConv 里 fanoutLocal 过了）
			if e.Node == h.cfg.NodeID {
				continue
			}
			// voice_* 走专用路由（按 To 字段精确转发），其它（chat/read/typing 等）按 ConvID
			if strings.HasPrefix(e.Type, "voice_") {
				h.fanoutVoiceLocal(&e)
			} else {
				h.fanoutLocal(&e)
			}
		}
	}
}

// OnlineAgentCount 公开 API：当前在线客服数（按 agentID 数，不重复计同账号多端）。
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

// PushToAgent 服务端主动给客服推消息（多端：同 agentID 所有连接都会收到）。
func (h *Hub) PushToAgent(agentID string, e *Envelope) bool {
	if v, ok := h.agents.Load(agentID); ok {
		sent := false
		v.(*sync.Map).Range(func(_, cv any) bool {
			cv.(*Client).Send(e)
			sent = true
			return true
		})
		return sent
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

// BroadcastToAllAgents 给本节点所有在线客服的所有连接发广播（多端：web + app 都收到）。
// 不走 byConv（避免访客自己也收到针对客服的提醒）。
func (h *Hub) BroadcastToAllAgents(e *Envelope) {
	e.Node = h.cfg.NodeID
	h.agents.Range(func(_, v any) bool {
		v.(*sync.Map).Range(func(_, cv any) bool {
			cv.(*Client).Send(e)
			return true
		})
		return true
	})
}

func beijing() *time.Location {
	tz, _ := time.LoadLocation("Asia/Shanghai")
	return tz
}
