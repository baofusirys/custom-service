package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/push"
	"github.com/custom-service/backend/internal/security"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

// Service 把 WS Hub、Store、Cipher、Logger 编织起来。
// 它实现了 ws.MessageSink，因此 Hub 收到消息时回调到这里持久化 + 风控。
type Service struct {
	store    *store.Store
	cipher   *security.Cipher
	bizLog   *zap.Logger
	secLog   *zap.Logger
	auditLog *zap.Logger
	limiter  *security.RateLimiter
	visMsgPM int
	hub      *ws.Hub      // 由 main.go 创建 Hub 后通过 SetHub 注入（解决 Hub<->Service 循环依赖）
	push     *push.Client // luckfast APNs 推送客户端，全局单例
}

func New(st *store.Store, cipher *security.Cipher, biz, sec, audit *zap.Logger, lim *security.RateLimiter, visMsgPM int) *Service {
	return &Service{
		store:    st,
		cipher:   cipher,
		bizLog:   biz,
		secLog:   sec,
		auditLog: audit,
		limiter:  lim,
		visMsgPM: visMsgPM,
		push:     push.NewClient(biz),
	}
}

// SetHub 在 main.go 创建 Hub 后回填，解决 Hub<->Service 循环依赖。
func (s *Service) SetHub(h *ws.Hub) { s.hub = h }

// ============ MessageSink 实现 ============

func (s *Service) OnVisitorConnect(ctx context.Context, c *ws.Client) error {
	s.bizLog.Info("visitor_ws_connect",
		zap.String("vid", c.ID), zap.String("conv", c.ConvID), zap.String("conn", c.ConnID))
	return nil
}

func (s *Service) OnVisitorDisconnect(ctx context.Context, c *ws.Client) error {
	s.bizLog.Info("visitor_ws_disconnect",
		zap.String("vid", c.ID), zap.String("conv", c.ConvID), zap.String("conn", c.ConnID))
	return nil
}

func (s *Service) OnAgentConnect(ctx context.Context, c *ws.Client) error {
	s.bizLog.Info("agent_ws_connect", zap.String("aid", c.ID), zap.String("conn", c.ConnID))
	return nil
}

func (s *Service) OnAgentDisconnect(ctx context.Context, c *ws.Client) error {
	s.bizLog.Info("agent_ws_disconnect", zap.String("aid", c.ID), zap.String("conn", c.ConnID))
	return nil
}

// PreprocessVisitorMessage 同步阶段：限流 + 内容清洗 + 注入检测。
// 返回 false 表示消息被拒（如限流），Hub 不再广播。
func (s *Service) PreprocessVisitorMessage(ctx context.Context, e *ws.Envelope, c *ws.Client) bool {
	if ok, _ := s.limiter.AllowVisitorMessage(ctx, c.ID, s.visMsgPM); !ok {
		s.limiter.RecordViolation(ctx, "visitor:"+c.ID, "visitor_msg_flood", c.ConvID)
		c.Send(&ws.Envelope{Type: "error", Content: "发送过于频繁，请稍后再试", TS: ws.NowMS()})
		return false
	}
	if e.Content != "" {
		if suspicious, pat := security.DetectSQLInjection(e.Content); suspicious {
			s.limiter.RecordViolation(ctx, "visitor:"+c.ID, "sqli_suspect", pat)
		}
		e.Content = security.SanitizeText(e.Content)
	}
	return true
}

// PreprocessAgentMessage 同步阶段：[077] conv 强制校验（杜绝 agent 孤儿消息）+ 内容清洗。
func (s *Service) PreprocessAgentMessage(ctx context.Context, e *ws.Envelope, c *ws.Client) bool {
	// [077] 根因：客服 WSS 刚建连时 c.ConvID 为空，必须点开会话(AssignSelf→AttachAgentToConv)才填上。
	//   在「建连→接管」窗口内发消息，空 c.ConvID 会被原样入库 → 孤儿消息（按会话 conv_id 查不到、界面"消失"）。
	//   visitor 路径有兜底(PersistMessageAsync OpenOrGetConversation)，但 agent 不能自动补会话（会错关联到别的会话），
	//   只能拒绝并提示客服先点开会话；同时防越权写入他人会话（[069] 遗留 TODO 一并落地）。
	if c.ConvID == "" {
		c.Send(&ws.Envelope{Type: "error", Content: "请先点开一个会话再发送消息", TS: ws.NowMS()})
		s.bizLog.Warn("agent_msg_no_conv", zap.String("agent", c.ID), zap.String("msg_id", e.ID))
		return false
	}
	ok, err := s.store.AgentOwnsConversation(ctx, c.ConvID, c.ID)
	if err != nil {
		s.bizLog.Error("agent_conv_check_err", zap.Error(err), zap.String("conv", c.ConvID))
		c.Send(&ws.Envelope{Type: "error", Content: "会话校验失败，请重试", TS: ws.NowMS()})
		return false
	}
	if !ok {
		c.Send(&ws.Envelope{Type: "error", Content: "会话不存在或您未接管该会话", TS: ws.NowMS()})
		s.bizLog.Warn("agent_conv_forbidden", zap.String("agent", c.ID), zap.String("conv", c.ConvID))
		return false
	}
	if e.Content != "" {
		e.Content = security.SanitizeText(e.Content)
	}
	return true
}

// OnPageNavigation 访客每打开/跳转一个页面时触发。
//
// 设计（按爷爷要求：不做服务端去重，每次都立即上报展示）：
//  1. 异步落库为一条 sys 消息（sender=sys, sender_ref="page:<url>"）
//     —— 让客服历史也能看到访客的浏览路径
//  2. BroadcastToAllAgents 发 type=chat from=sys + extra.kind=page_navigation
//     —— 客服端收到后渲染为「橙色横幅」（不是普通气泡），参考 Crisp 风格
//
// 注：客户端 chat.html 内部有 pageReported 状态，同 URL 在同一会话实例内不会重复触发；
// 跨页面跳转时 chat.html 是新实例，pageReported 重置，会重新上报。
func (s *Service) OnPageNavigation(visitorID, convID, url, title string) {
	if convID == "" {
		return
	}
	// 清洗 + 限长（防 XSS / 防超长内容）
	url = security.SanitizeText(url)
	title = security.SanitizeText(title)
	if len(url) > 1024 {
		url = url[:1024]
	}
	if len(title) > 256 {
		title = title[:256]
	}
	if url == "" && title == "" {
		return
	}
	now := time.Now()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.bizLog.Error("on_page_navigation panic", zap.Any("err", r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		display := title
		if display == "" {
			display = url
		}
		content := "访客访问了「" + display + "」"

		msg := &store.Message{
			ID:          uuid.NewString(),
			ConvID:      convID,
			Sender:      "sys",
			SenderRef:   "page:" + url,
			Content:     content,
			CreatedAt:   now,
			DeliveredWS: true,
		}
		if err := s.store.InsertMessage(ctx, msg); err != nil {
			s.bizLog.Error("page_navigation insert err", zap.Error(err))
			return
		}
		env := &ws.Envelope{
			Type:    "chat",
			ID:      msg.ID,
			From:    "sys",
			ConvID:  convID,
			Content: content,
			TS:      ws.NowMS(),
			Extra: map[string]any{
				"kind":  "page_navigation",
				"url":   url,
				"title": title,
			},
		}
		if s.hub != nil {
			s.hub.BroadcastToAllAgents(env)
		}
		s.bizLog.Info("page_navigation broadcast",
			zap.String("vid", visitorID), zap.String("conv", convID),
			zap.String("url", url), zap.String("title", title))
	}()
}

// PersistReadAsync 异步落库已读时刻：把对应 role 的 last_read_*_at 推到 time.Now()。
// 失败仅记日志，不影响 WSS 广播给对端。
func (s *Service) PersistReadAsync(e *ws.Envelope, c *ws.Client, role string) {
	convID := e.ConvID
	if convID == "" {
		convID = c.ConvID
	}
	if convID == "" {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.bizLog.Error("persist_read panic", zap.Any("err", r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.store.UpdateLastRead(ctx, convID, role, time.Now()); err != nil {
			s.bizLog.Error("persist read err", zap.Error(err),
				zap.String("conv", convID), zap.String("role", role))
		}
	}()
}

// PersistMessageAsync 异步阶段：后台 goroutine 入库 + 兜底 conv。
// 失败只记日志（原始报文已落 raw_ws.log，可重放）。
func (s *Service) PersistMessageAsync(e *ws.Envelope, c *ws.Client, sender string) {
	// 拷贝出当前 envelope 的快照，避免外部修改影响异步写入
	snap := *e
	convID := c.ConvID
	siteID := c.SiteID
	clientID := c.ID
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.bizLog.Error("persist_message panic", zap.Any("err", r), zap.String("id", snap.ID))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// 兜底：visitor 首次发消息但 client.ConvID 还没填
		if convID == "" && sender == "visitor" {
			conv, err := s.store.OpenOrGetConversation(ctx, siteID, clientID)
			if err != nil {
				s.bizLog.Error("persist open conv err", zap.Error(err), zap.String("vid", clientID))
				return
			}
			convID = conv.ID
			c.ConvID = conv.ID
			snap.ConvID = conv.ID
		}
		snap.ConvID = convID
		m := buildMsg(&snap, sender, clientID)
		if err := s.store.InsertMessage(ctx, m); err != nil {
			s.bizLog.Error("persist insert msg err",
				zap.Error(err), zap.String("id", snap.ID), zap.String("conv", convID))
		}
		// [085] 客服回复 → 标记会话 agent_replied=1（「已联系」口径按访客聚合此标记，
		//   解决会话超时重建后「已联系」丢失：只要该客户被回复过，名下任一会话都算已联系）
		if sender == "agent" && convID != "" {
			if err := s.store.MarkAgentReplied(ctx, convID); err != nil {
				s.bizLog.Warn("mark_agent_replied err", zap.Error(err), zap.String("conv", convID))
			}
		}
		// 仅访客消息触发 APNs 推送（让客服 iPhone 锁屏时也能收到）
		if sender == "visitor" {
			preview := buildPushPreview(&snap)
			if preview != "" {
				go s.pushVisitorMessageAPNs(preview, clientID)
			}
		}
	}()
}

// buildPushPreview 从 envelope 构造推送内容预览（文本优先；图片/文件用占位符）
func buildPushPreview(e *ws.Envelope) string {
	if e.Content != "" {
		return e.Content
	}
	if e.MediaKind == "image" {
		return "[图片]"
	}
	if e.MediaURL != "" {
		return "[文件]"
	}
	return ""
}

// shortVid 截访客 ID 前 8 位做副标题展示（完整 UUID 太长）
func shortVid(vid string) string {
	if len(vid) <= 8 {
		return vid
	}
	return vid[:8]
}

// pushAPNsCommon 是两种推送场景的公共部分：检查配置 + 调 luckfast。
// scene 表示场景（用于读对应的 sound 设置），sound 直接给 push。
func (s *Service) pushAPNsCommon(title, subtitle, message, soundSettingKey, defaultSound string) {
	defer func() {
		if r := recover(); r != nil {
			s.bizLog.Error("apns_push_panic", zap.Any("err", r))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	uid := s.SettingStr(ctx, "push_user_id", "")
	key := s.SettingStr(ctx, "push_user_key", "")
	if uid == "" || key == "" {
		return // 未配置，禁用推送
	}
	sound := s.SettingStr(ctx, soundSettingKey, defaultSound)
	err := s.push.Send(ctx, push.Options{
		UserID:   uid,
		UserKey:  key,
		Title:    title,
		Subtitle: subtitle,
		Message:  message,
		Sound:    sound,
		// 默认 maihaocs:// URL Scheme，让 iOS 点击推送时拉起 Custom Service App。
		// 管理员可在 admin Settings 通过 push_jump_url 覆盖。
		JumpURL: s.SettingStr(ctx, "push_jump_url", "maihaocs://open"),
	})
	if err != nil {
		s.bizLog.Warn("apns_push_failed",
			zap.Error(err), zap.String("title", title), zap.String("preview", subtitle))
	}
}

// pushVisitorMessageAPNs 访客发新消息时触发，sound 走 push_sound_message。
func (s *Service) pushVisitorMessageAPNs(content, vid string) {
	s.pushAPNsCommon(
		"客服系统 · 新消息",
		"访客 "+shortVid(vid),
		content,
		"push_sound_message",
		"9", // 默认提示音
	)
}

// [069] codeToText 把通话结束 (code, reason, durSec) 三元组翻译成 sys 消息显示文字。
//
// 优先级：reason > code。
//   - 当 reason 是细化原因 enum（agent_no_answer_5s / mic_permission_denied / ...）时，
//     直接渲染该 reason 对应中文，让用户立刻看到"为什么挂了"。
//   - 当 reason="normal_hangup" / "" 时（旧路径、客户端没传），fallback 到 code 的旧文案。
//
// reason enum（与 ws/hub.go pendingAccepts 上方注释、mobile_app voice_controller 三端对齐）：
//   - agent_no_answer_5s   客服 accept 后 5s 内未回 voice_answer（hub 看门狗触发）
//   - mic_permission_denied 客服麦克风权限被拒
//   - mic_busy             麦克风被占用
//   - mic_hardware_error   麦克风硬件异常
//   - no_audio_tracks      未能获取音频通道
//   - signal_exception     WebRTC 信令异常（agent 上报 voice_signal_error）
//   - no_answer_sdp        应答 SDP 解析失败
//   - no_ice_candidate     ICE 候选超时
//   - ice_disconnected     网络中断
//   - normal_hangup / ""   正常挂断（走 code 旧文案）
func codeToText(code, reason string, durSec int) string {
	switch reason {
	case "agent_no_answer_5s":
		return "客服 5 秒未应答自动挂断"
	case "mic_permission_denied":
		return "对方麦克风权限被拒，通话失败"
	case "mic_busy":
		return "对方麦克风被占用，通话失败"
	case "mic_hardware_error":
		return "对方麦克风硬件异常，通话失败"
	case "no_audio_tracks":
		return "对方未能获取音频通道，通话失败"
	case "signal_exception":
		return "信令异常，通话已中止"
	case "no_answer_sdp":
		return "应答 SDP 解析失败，通话失败"
	case "no_ice_candidate":
		return "ICE 候选超时，通话失败"
	case "ice_disconnected":
		return "网络中断，通话结束"
	case "normal_hangup", "":
		// 走下面 code 渲染
	default:
		// 未知 reason 不直接返回兜底文本——继续走 code，能给出更具体的 "对方已拒绝/呼叫未接听" 等
	}
	switch code {
	case "no_answer":
		return "呼叫未接听"
	case "rejected":
		return "对方已拒绝"
	case "busy":
		return "对方忙线中"
	case "cancel":
		return "已取消"
	case "failed":
		return "连接失败"
	case "hangup":
		mm := durSec / 60
		ss := durSec % 60
		if mm > 0 {
			return fmt.Sprintf("通话结束（%d 分 %d 秒）", mm, ss)
		}
		return fmt.Sprintf("通话结束（%d 秒）", ss)
	default:
		// 走到这里说明 reason 也不是已知 enum，code 也不是已知枚举；兜底但带上 reason 让排查更容易
		if reason != "" {
			return fmt.Sprintf("通话已结束（%s）", reason)
		}
		return "通话已结束"
	}
}

// OnVoiceCallFinished 实现 ws.MessageSink 接口：通话终结时写一条 sys 消息到 conv +
// FanoutToConv 广播给会话所有客户端（访客 + 客服）实时显示。
// 用 visitor 自己的 site_id 找他的 open conversation。
//
// [069] 签名扩展：新增 reason 参数（细化原因 enum，详见 codeToText 上方注释）。
//   - reason 传入 hub 侧的细化原因（agent_no_answer_5s / mic_permission_denied / ...）；
//     正常挂断老路径传 "normal_hangup" 或 ""。
//   - SenderRef 改为 "voice:" + reason / code（eg "voice:agent_no_answer_5s"），让
//     admin REST 历史消息列表 + 统计能基于 SenderRef 前缀精准识别"未接 / 麦克风失败 /
//     信令异常"分布，不再依赖正则解析中文 content。
//   - Envelope.Extra 新增 reason 字段透传给在线访客 / 客服 UI 渲染特殊图标。
func (s *Service) OnVoiceCallFinished(visitorID, callID, code, reason string, durSec int) {
	if visitorID == "" || code == "" {
		return
	}
	text := codeToText(code, reason, durSec)
	if text == "" {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.bizLog.Error("voice_finished panic", zap.Any("err", r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// 找访客对应的 site_id（visitors 表）
		visitor, err := s.store.GetVisitor(ctx, visitorID)
		if err != nil || visitor == nil {
			s.bizLog.Warn("voice_finished get_visitor failed",
				zap.String("vid", visitorID), zap.Error(err))
			return
		}
		// 找访客当前 open 会话
		conv, err := s.store.OpenOrGetConversation(ctx, visitor.SiteID, visitorID)
		if err != nil {
			s.bizLog.Warn("voice_finished open_conv failed",
				zap.String("vid", visitorID), zap.Error(err))
			return
		}
		// [069] SenderRef = "voice:" + reason（细化原因），让 REST 历史拉取 + admin 统计
		// 能基于 SenderRef 前缀精准识别。reason 为空 / normal_hangup 时退回 "voice:" + code。
		senderRef := "voice"
		switch {
		case reason != "" && reason != "normal_hangup":
			senderRef = "voice:" + reason
		case code != "":
			senderRef = "voice:" + code
		}
		msg := &store.Message{
			ID:        uuid.NewString(),
			ConvID:    conv.ID,
			Sender:    "sys",
			SenderRef: senderRef,
			Content:   text,
			CreatedAt: time.Now(),
		}
		if err := s.store.InsertMessage(ctx, msg); err != nil {
			s.bizLog.Error("voice_finished insert msg",
				zap.Error(err), zap.String("conv", conv.ID))
			return
		}
		// 广播给会话内所有客户端，让聊天区实时多出一条
		if s.hub != nil {
			env := &ws.Envelope{
				Type:    "chat",
				ID:      msg.ID,
				From:    "sys",
				ConvID:  conv.ID,
				Content: text,
				TS:      ws.NowMS(),
				Extra: map[string]any{
					"kind":     "voice_finished",
					"code":     code,
					"reason":   reason, // [069] 透传给在线 UI 渲染特殊图标
					"duration": durSec,
					"call_id":  callID,
				},
			}
			s.hub.FanoutToConv(ctx, env)
		}
		s.bizLog.Info("voice_finished",
			zap.String("vid", visitorID), zap.String("conv", conv.ID),
			zap.String("code", code), zap.String("reason", reason),
			zap.Int("dur", durSec))
	}()
}

// OnVisitorVoiceCall 实现 ws.MessageSink 接口：访客发起语音呼叫时被 hub 回调。
// 走异步 push（不阻塞 hub 处理后续信令）。
// 推送策略：让客服 iPhone 锁屏/后台时也能看到来电通知，点击拉起 App 接听。
func (s *Service) OnVisitorVoiceCall(visitorID, callID string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.bizLog.Error("apns_push_call_panic", zap.Any("err", r))
			}
		}()
		s.pushAPNsCommon(
			"客服系统 · 来电",
			"访客 "+shortVid(visitorID),
			"语音来电！请立即点开 App 接听",
			"push_sound_call",
			"4", // 默认提示音 4（跟 enter/message 区分）
		)
	}()
}

// humanizeDuration 把 duration 转人类化字符串（用于 push 显示"首次访问 N 前"）。
func humanizeDuration(d time.Duration) string {
	if d < time.Minute {
		return "刚刚"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	}
	return fmt.Sprintf("%d 天", int(d.Hours()/24))
}

// ============ HTTP 业务方法（暴露给 handler） ============

func (s *Service) Store() *store.Store            { return s.store }
func (s *Service) Cipher() *security.Cipher       { return s.cipher }
func (s *Service) Limiter() *security.RateLimiter { return s.limiter }
func (s *Service) BizLog() *zap.Logger            { return s.bizLog }
func (s *Service) AuditLog() *zap.Logger          { return s.auditLog }

// ============ Settings ============

// SettingBool 读 bool 类型 setting（值为 "true" 视为 true，其他/缺失返回 def）。
func (s *Service) SettingBool(ctx context.Context, key string, def bool) bool {
	v := s.store.GetSetting(ctx, key, "")
	if v == "" {
		return def
	}
	return v == "true" || v == "1" || v == "yes"
}

// SettingStr 读 string 类型 setting。
func (s *Service) SettingStr(ctx context.Context, key, def string) string {
	v := s.store.GetSetting(ctx, key, "")
	if v == "" {
		return def
	}
	return v
}

// SettingInt [085] 读 int 类型 setting（缺失/解析失败返回 def）。
// 用于「会话超时重建」阈值 session_fresh_minutes 可配置项。边界由调用方 clamp。
func (s *Service) SettingInt(ctx context.Context, key string, def int) int {
	v := s.store.GetSetting(ctx, key, "")
	if v == "" {
		return def
	}
	n := def
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

// ============ 访客进入通知 + 自动问候 ============

// GreetingTextIfEnabled 返回当前问候文本；未开启返回 ""。
// handler.VisitorSession 在 HTTP 响应里把这段文本回给访客，访客 bootstrap 后立即渲染 —— 不依赖 WSS 时序。
func (s *Service) GreetingTextIfEnabled(ctx context.Context) string {
	if !s.SettingBool(ctx, "greeting_enabled", true) {
		return ""
	}
	return s.SettingStr(ctx, "greeting_text", "您好，欢迎光临！请问有什么可以帮您？")
}

// OnVisitorEnter 异步执行三件事（不阻塞访客主流程，goroutine 内运行）：
//  1. 立即广播 visitor_enter sys 通知给所有在线客服（触发客服端 ElNotification + 播声）
//  2. 把 greeting 落库（DB 持久化，客服端历史能查到）
//  3. 等访客 WSS 上线后 PushToVisitor greeting（type=chat, from=sys），
//     让访客端走完整的 onmessage 逻辑：播提示音 / 累计未读 / 显示「已读」机制
//     同时给所有在线客服广播一份（客服端能在左侧列表立即看到这条 sys 消息）
//
// 为啥不用之前那套「HTTP response 里返回 greeting 文本，访客端直接 render」的旧方案：
// 旧方案虽然简单，但访客端是"自己绘制"消息，没走 WSS onmessage 通道，所以：
//   - 没触发提示音 playNotify
//   - 没累计未读（widget 收起时 badge 不动）
//   - 没已读机制
//
// 新方案让 greeting 完全等同于"客服真实发的消息"。
func (s *Service) OnVisitorEnter(visitor *store.Visitor, conv *store.Conversation, hub *ws.Hub) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.bizLog.Error("on_visitor_enter panic", zap.Any("err", r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		identifier := visitor.Identifier
		if identifier == "" {
			identifier = "访客 " + visitor.ID[:6]
		}

		// 1) 立即通知客服「访客进入」
		if s.SettingBool(ctx, "notify_visitor_enter", true) {
			hub.BroadcastToAllAgents(&ws.Envelope{
				Type:    "sys",
				ID:      uuid.NewString(),
				ConvID:  conv.ID,
				TS:      ws.NowMS(),
				Content: identifier + " 进入了网站",
				Extra: map[string]any{
					"kind":       "visitor_enter",
					"visitor_id": visitor.ID,
					"site_id":    visitor.SiteID,
					"country":    visitor.Country,
					"city":       visitor.City,
					"referer":    visitor.Referer,
					"last_page":  visitor.LastPage,
				},
			})
			s.bizLog.Info("visitor_enter notified",
				zap.String("vid", visitor.ID), zap.String("conv", conv.ID))
			// 同时给 iPhone APNs 推送（如果配置了 push_user_id/key），客服可在锁屏看到。
			// 关键：handler 传过来的 visitor.FirstSeen 总是 now（UpsertVisitor 的 SQL
			// 不更新 first_seen，但 Go 对象里的 FirstSeen 还是 now），所以这里必须
			// 重新 GetVisitor 拿 DB 里真实 first_seen，才能区分"真新 vs 老回访"。
			go func() {
				defer func() {
					if r := recover(); r != nil {
						s.bizLog.Error("apns_push_enter_panic", zap.Any("err", r))
					}
				}()
				pctx, pcancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer pcancel()
				var pushTitle, pushMsg string
				if real, err := s.store.GetVisitor(pctx, visitor.ID); err == nil && real != nil &&
					time.Since(real.FirstSeen) > 10*time.Second {
					// 老客户回访：首次访问时间在 10 秒前 = 之前来过
					pushTitle = "客服系统 · 老客户回访"
					pushMsg = fmt.Sprintf("%s 又来了，首次访问 %s前",
						identifier, humanizeDuration(time.Since(real.FirstSeen)))
				} else {
					// 真新访客：first_seen 在 10 秒内 = 数据库刚为他生成记录
					pushTitle = "客服系统 · 新访客"
					pushMsg = "有新访客打开了客服窗口，快去看看吧"
				}
				s.pushAPNsCommon(pushTitle, identifier, pushMsg, "push_sound_enter", "1")
			}()
		}

		// 2) 自动问候
		if !s.SettingBool(ctx, "greeting_enabled", true) {
			return
		}
		text := s.SettingStr(ctx, "greeting_text", "您好，欢迎光临！请问有什么可以帮您？")
		msg := &store.Message{
			ID:          uuid.NewString(),
			ConvID:      conv.ID,
			Sender:      "sys",
			SenderRef:   "system",
			Content:     text,
			CreatedAt:   time.Now(),
			DeliveredWS: true,
		}
		if err := s.store.InsertMessage(ctx, msg); err != nil {
			s.bizLog.Error("greeting insert err", zap.Error(err))
			return
		}
		env := &ws.Envelope{
			Type:    "chat",
			ID:      msg.ID,
			From:    "sys",
			ConvID:  conv.ID,
			Content: text,
			TS:      ws.NowMS(),
		}
		// 立即广播给所有在线客服（客服端能在左侧列表立即看到「新会话+这条 sys 消息」）
		hub.BroadcastToAllAgents(env)

		// 轮询等访客 WSS 上线（最多 8 秒），上线立即 PushToVisitor。
		// PushToVisitor 返回 false 表示访客还没在 hub.visitors 注册（WSS 还没建立完成）。
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if hub.PushToVisitor(visitor.ID, env) {
				s.bizLog.Info("greeting pushed to visitor via WSS",
					zap.String("vid", visitor.ID), zap.String("msg", msg.ID))
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
		s.bizLog.Warn("greeting WSS push timeout (visitor not online within 8s)",
			zap.String("vid", visitor.ID), zap.String("msg", msg.ID))
	}()
}

// EnsureBootstrapAdmin 启动时确保至少存在一个超管账号。
func (s *Service) EnsureBootstrapAdmin(ctx context.Context, username, password string) error {
	existing, err := s.store.GetAgentByUsername(ctx, username)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.store.CreateAgent(ctx, &store.Agent{
		Username: username,
		PassHash: hash,
		Role:     "admin",
		Nickname: "超级管理员",
	})
	if err == nil {
		s.bizLog.Info("bootstrap admin created", zap.String("username", username))
	}
	return err
}

// ============ helpers ============

func buildMsg(e *ws.Envelope, sender, senderRef string) *store.Message {
	m := &store.Message{
		ID:          e.ID,
		ConvID:      e.ConvID,
		Sender:      sender,
		SenderRef:   senderRef,
		Content:     e.Content,
		CreatedAt:   time.Now(),
		DeliveredWS: true,
	}
	if e.MediaURL != "" {
		m.MediaURL = sql.NullString{String: e.MediaURL, Valid: true}
		m.MediaKind = sql.NullString{String: e.MediaKind, Valid: true}
		m.MediaName = sql.NullString{String: e.MediaName, Valid: true}
		if e.MediaSize > 0 {
			m.MediaSize = sql.NullInt64{Int64: e.MediaSize, Valid: true}
		}
	}
	return m
}
