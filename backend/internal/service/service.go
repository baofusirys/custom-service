package service

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

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
	}
}

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

// PreprocessAgentMessage 同步阶段：内容清洗。
func (s *Service) PreprocessAgentMessage(ctx context.Context, e *ws.Envelope, c *ws.Client) bool {
	if e.Content != "" {
		e.Content = security.SanitizeText(e.Content)
	}
	return true
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
	}()
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

// ============ 访客进入通知 + 自动问候 ============

// GreetingTextIfEnabled 返回当前问候文本；未开启返回 ""。
// handler.VisitorSession 在 HTTP 响应里把这段文本回给访客，访客 bootstrap 后立即渲染 —— 不依赖 WSS 时序。
func (s *Service) GreetingTextIfEnabled(ctx context.Context) string {
	if !s.SettingBool(ctx, "greeting_enabled", true) {
		return ""
	}
	return s.SettingStr(ctx, "greeting_text", "您好，欢迎光临！请问有什么可以帮您？")
}

// OnVisitorEnter 异步执行两件事（不阻塞访客主流程）：
//   1. 给所有在线客服广播 visitor_enter 通知（带画像）
//   2. 把 greeting 消息落库（让客服端历史能看到这条 sys 消息）
//
// 注：问候消息不通过 WSS 推给访客 —— 访客已经从 HTTP 响应直接拿到 greeting 文本本地渲染。
// 这样规避了「访客 WSS 还没建立时服务端就广播」的时序丢消息问题。
func (s *Service) OnVisitorEnter(visitor *store.Visitor, conv *store.Conversation, hub *ws.Hub) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.bizLog.Error("on_visitor_enter panic", zap.Any("err", r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		identifier := visitor.Identifier
		if identifier == "" {
			identifier = "访客 " + visitor.ID[:6]
		}

		// 1) 给所有在线客服推「访客进入」通知
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
		}

		// 2) 把 greeting 落库（客服端拉历史时能看到）
		if s.SettingBool(ctx, "greeting_enabled", true) {
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
			s.bizLog.Info("greeting persisted", zap.String("conv", conv.ID), zap.String("msg", msg.ID))
		}
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
