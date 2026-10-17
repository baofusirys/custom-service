package hall

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/mailer"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/textfilter"
)

// [122] 敏感词屏蔽运行时：编译结果缓存、消息屏蔽、拦截记录、告警邮件发件箱（合并发送 + 重试）。

// FilterStore 拦截记录与设置写入所需的数据能力（*store.Store 实现）。
type FilterStore interface {
	SetSettings(ctx context.Context, kv map[string]string) error
	InsertHallFilterEvent(ctx context.Context, e *store.HallFilterEvent) error
	CountPendingHallFilterMail(ctx context.Context, now time.Time) (int, error)
	ListPendingHallFilterMail(ctx context.Context, now time.Time, limit int) ([]store.HallFilterEvent, error)
	MarkHallFilterMailSent(ctx context.Context, ids []int64, at time.Time) error
	MarkHallFilterMailRetry(ctx context.Context, ids []int64, maxAttempts int, nextAt time.Time, reason string) error
	ListHallFilterEvents(ctx context.Context, beforeID int64, limit int) ([]store.HallFilterEvent, error)
}

const (
	alertTickInterval = 15 * time.Second
	alertBatch        = 50
	alertMaxAttempts  = 5
	alertSendTimeout  = 40 * time.Second
)

// alertBackoff 第 n 次失败后的重试间隔。
var alertBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

type cachedFilter struct {
	f   *textfilter.Filter
	exp time.Time
}

type cachedAlert struct {
	c   alertConfig
	exp time.Time
}

// textFilter 生效中的过滤器（5 秒缓存；读取失败时沿用上一份，首读失败则不过滤并记错误日志）。
func (s *Service) textFilter(ctx context.Context) *textfilter.Filter {
	now := s.now()
	if c := s.filterCache.Load(); c != nil && now.Before(c.exp) {
		return c.f
	}
	m, err := s.settingsMap(ctx, filterKeys)
	if err != nil {
		s.biz.Error("hall_filter_settings_load_failed", zap.Error(err))
		if c := s.filterCache.Load(); c != nil {
			return c.f
		}
		return nil
	}
	f := textfilter.Compile(parseFilterSettings(m, s.publicDomain))
	s.filterCache.Store(&cachedFilter{f: f, exp: now.Add(filterCacheTTL)})
	return f
}

func (s *Service) alertSettings(ctx context.Context, fresh bool) (alertConfig, error) {
	now := s.now()
	if c := s.alertCache.Load(); !fresh && c != nil && now.Before(c.exp) {
		return c.c, nil
	}
	m, err := s.settingsMap(ctx, alertKeys)
	if err != nil {
		return alertConfig{}, err
	}
	c := s.loadAlertConfig(m)
	s.alertCache.Store(&cachedAlert{c: c, exp: now.Add(filterCacheTTL)})
	return c, nil
}

// invalidateFilterCaches 后台保存设置后立即生效。
func (s *Service) invalidateFilterCaches() {
	s.filterCache.Store(nil)
	s.alertCache.Store(nil)
}

func categoryList(cats []textfilter.Category) string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, string(c))
	}
	return strings.Join(out, ",")
}

// recordFilterEvent 写拦截记录（原文加密）；告警邮件由后台发件箱合并发送。失败只记日志，不影响消息本身。
func (s *Service) recordFilterEvent(ctx context.Context, msg *store.HallMessage, original string, res textfilter.Result, silenced bool) {
	status := store.HallMailPending
	if silenced {
		status = store.HallMailSilenced
	} else if c, err := s.alertSettings(ctx, false); err != nil || !c.Enabled || !c.configured() {
		status = store.HallMailDisabled
	}
	fields := []zap.Field{zap.String("event", "hall_chat_filtered"), zap.String("msg_id", msg.ID), zap.String("identity", msg.Identity),
		zap.String("categories", categoryList(res.Categories)), zap.Int("hits", len(res.Hits)), zap.String("mail_status", status)}
	s.sec.Warn("hall_chat_filtered", fields...)
	if s.filterStore == nil {
		s.biz.Error("hall_filter_event_store_missing", zap.String("msg_id", msg.ID))
		return
	}
	enc, err := s.origCipher.Encrypt(original)
	if err != nil {
		s.biz.Error("hall_filter_event_encrypt_failed", zap.String("msg_id", msg.ID), zap.Error(err))
		return
	}
	ev := &store.HallFilterEvent{MessageID: msg.ID, Room: msg.Room, Sender: msg.Sender, SenderRef: msg.SenderRef, Identity: msg.Identity,
		Name: msg.Name, Categories: categoryList(res.Categories), OriginalEnc: enc, Masked: msg.Content, MailStatus: status, CreatedAt: msg.CreatedAt}
	if err := s.filterStore.InsertHallFilterEvent(ctx, ev); err != nil {
		s.biz.Error("hall_filter_event_persist_failed", zap.String("msg_id", msg.ID), zap.Error(err), zap.Stack("stack"))
	}
}

// startAlertWorker 告警邮件发件箱：每 15 秒检查一次；多副本借 Redis 计数键保证全局每 55 秒最多发一封。
func (s *Service) startAlertWorker(ctx context.Context) {
	go func() {
		t := time.NewTicker(alertTickInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.alertTickGuarded(ctx)
			}
		}
	}()
}

func (s *Service) alertTickGuarded(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.biz.Error("hall_alert_worker_panic", zap.Any("err", r), zap.Stack("stack"))
		}
	}()
	s.AlertTick(ctx)
}

// AlertTick 执行一次发件箱检查（导出供测试）。返回本次发送的记录数。
func (s *Service) AlertTick(ctx context.Context) int {
	if s.filterStore == nil {
		return 0
	}
	cfg, err := s.alertSettings(ctx, true)
	if err != nil || !cfg.Enabled || !cfg.configured() {
		return 0
	}
	now := s.now()
	n, err := s.filterStore.CountPendingHallFilterMail(ctx, now)
	if err != nil || n == 0 {
		if err != nil {
			s.biz.Error("hall_alert_pending_count_failed", zap.Error(err))
		}
		return 0
	}
	if s.limiter != nil {
		ok, err := s.limiter.AllowAction(ctx, "job_hall_alert_mail", 1, 55*time.Second, "global")
		if err != nil || !ok {
			return 0
		}
	}
	events, err := s.filterStore.ListPendingHallFilterMail(ctx, now, alertBatch)
	if err != nil || len(events) == 0 {
		if err != nil {
			s.biz.Error("hall_alert_pending_list_failed", zap.Error(err))
		}
		return 0
	}
	ids := make([]int64, 0, len(events))
	maxAttempts := 0
	for _, e := range events {
		ids = append(ids, e.ID)
		if e.MailAttempts > maxAttempts {
			maxAttempts = e.MailAttempts
		}
	}
	msg := s.digestMail(cfg, events, now)
	sctx, cancel := context.WithTimeout(ctx, alertSendTimeout)
	start := time.Now()
	sendErr := s.sendMail(sctx, cfg.SMTP, msg)
	cancel()
	if sendErr == nil {
		if err := s.filterStore.MarkHallFilterMailSent(ctx, ids, s.now()); err != nil {
			s.biz.Error("hall_alert_mark_sent_failed", zap.Error(err), zap.Int("count", len(ids)))
		}
		s.biz.Info("hall_alert_mail_sent", zap.String("event", "hall_alert_mail"), zap.Int("count", len(ids)),
			zap.Int("recipients", len(cfg.To)), zap.Int64("latency_ms", time.Since(start).Milliseconds()))
		return len(ids)
	}
	idx := maxAttempts
	if idx >= len(alertBackoff) {
		idx = len(alertBackoff) - 1
	}
	reason := mailer.ReasonOf(sendErr)
	if err := s.filterStore.MarkHallFilterMailRetry(ctx, ids, alertMaxAttempts, s.now().Add(alertBackoff[idx]), reason); err != nil {
		s.biz.Error("hall_alert_mark_retry_failed", zap.Error(err))
	}
	s.biz.Warn("hall_alert_mail_failed", zap.String("event", "hall_alert_mail"), zap.Int("count", len(ids)), zap.Int("attempt", maxAttempts+1),
		zap.String("reason", reason), zap.String("error", sanitizeMailErr(sendErr, cfg.SMTP.Password)))
	return 0
}

// sanitizeMailErr 日志用的原始错误（防御性去掉密码）。
func sanitizeMailErr(err error, password string) string {
	msg := err.Error()
	if password != "" {
		msg = strings.ReplaceAll(msg, password, "***")
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

var categoryLabels = map[string]string{"ad": "广告", "link": "超链接", "porn": "色情", "gambling": "博彩", "contact": "联系方式", "custom": "自定义词"}

func categoryText(csv string) string {
	var out []string
	for _, c := range strings.Split(csv, ",") {
		if l, ok := categoryLabels[c]; ok {
			out = append(out, l)
		}
	}
	return strings.Join(out, "、")
}

// digestMail 合并多条拦截记录为一封纯文本邮件（北京时间）。
func (s *Service) digestMail(cfg alertConfig, events []store.HallFilterEvent, now time.Time) mailer.Message {
	sort.Slice(events, func(i, j int) bool { return events[i].ID < events[j].ID })
	var b strings.Builder
	fmt.Fprintf(&b, "以下 %d 条大厅消息命中敏感词屏蔽，大厅里显示的是屏蔽后的内容（时间为北京时间）。\n\n", len(events))
	for i, e := range events {
		orig, err := s.origCipher.Decrypt(e.OriginalEnc)
		if err != nil {
			orig = "（原文解密失败）"
		}
		fmt.Fprintf(&b, "[%d] %s\n", i+1, e.CreatedAt.In(beijing).Format("2006-01-02 15:04:05 -07:00"))
		who := e.Name
		if e.Sender == "visitor" && e.SenderRef != "" {
			who += "（访客编号 " + e.SenderRef + "，大厅标识 " + e.Identity + "）"
		} else if e.Identity != "" {
			who += "（" + e.Identity + "）"
		}
		fmt.Fprintf(&b, "发送者：%s\n命中类别：%s\n原文：%s\n屏蔽后：%s\n\n", who, categoryText(e.Categories), orig, e.Masked)
	}
	b.WriteString("——\n此邮件由客服系统自动发送。可在管理后台「系统设置 → 多人大厅 → 敏感词屏蔽」修改收发件邮箱、调整词库或关闭通知。\n")
	return mailer.Message{
		To:      cfg.To,
		Subject: fmt.Sprintf("[客服大厅] 拦截敏感消息 %d 条 · %s", len(events), now.In(beijing).Format("2006-01-02 15:04")),
		Text:    b.String(),
	}
}

// errAlertNotConfigured 测试邮件时配置不完整。
var errAlertNotConfigured = errors.New("hall: alert mail not configured")
