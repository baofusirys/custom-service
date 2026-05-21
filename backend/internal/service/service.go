package service

import (
	"context"
	"database/sql"
	"errors"
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

func (s *Service) OnVisitorMessage(ctx context.Context, e *ws.Envelope, c *ws.Client) error {
	// 1) 频率限流（防访客刷消息）
	if ok, _ := s.limiter.AllowVisitorMessage(ctx, c.ID, s.visMsgPM); !ok {
		s.limiter.RecordViolation(ctx, "visitor:"+c.ID, "visitor_msg_flood", c.ConvID)
		c.Send(&ws.Envelope{Type: "error", Content: "发送过于频繁，请稍后再试", TS: ws.NowMS()})
		return errors.New("rate limited")
	}
	// 2) XSS / SQL 注入嫌疑（防御性上报）
	if e.Content != "" {
		if suspicious, pat := security.DetectSQLInjection(e.Content); suspicious {
			s.limiter.RecordViolation(ctx, "visitor:"+c.ID, "sqli_suspect", pat)
		}
		e.Content = security.SanitizeText(e.Content)
	}
	// 3) 持久化
	if e.ConvID == "" {
		conv, err := s.store.OpenOrGetConversation(ctx, c.SiteID, c.ID)
		if err != nil {
			return err
		}
		c.ConvID = conv.ID
		e.ConvID = conv.ID
	}
	m := buildMsg(e, "visitor", c.ID)
	return s.store.InsertMessage(ctx, m)
}

func (s *Service) OnAgentMessage(ctx context.Context, e *ws.Envelope, c *ws.Client) error {
	if e.Content != "" {
		e.Content = security.SanitizeText(e.Content)
	}
	m := buildMsg(e, "agent", c.ID)
	return s.store.InsertMessage(ctx, m)
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
