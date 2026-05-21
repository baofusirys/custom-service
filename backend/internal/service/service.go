package service

import (
	"context"
	"database/sql"
	"time"

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
