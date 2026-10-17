package hall

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/mailer"
	"github.com/custom-service/backend/internal/textfilter"
)

// [122] 敏感词屏蔽与告警邮件的管理能力（仅管理员；处理器只做绑定与审计，规则全部在这里）。

var errMailSendFailed = newErr(http.StatusBadGateway, 50271, "测试邮件发送失败")

func requireAdmin(op Actor) *Error {
	if op.Visitor || !op.Admin {
		return errTargetForbidden
	}
	return nil
}

// FilterSettings 读取敏感词设置。
func (s *Service) FilterSettings(ctx context.Context, op Actor) (FilterSettingsView, error) {
	if e := requireAdmin(op); e != nil {
		return FilterSettingsView{}, e
	}
	m, err := s.settingsMap(ctx, filterKeys)
	if err != nil {
		return FilterSettingsView{}, s.internal("hall_filter_settings_load_failed", err)
	}
	return filterView(parseFilterSettings(m, s.publicDomain)), nil
}

// SaveFilterSettings 保存敏感词设置（整份覆盖）。
func (s *Service) SaveFilterSettings(ctx context.Context, op Actor, in *FilterInput) (FilterSettingsView, error) {
	if e := requireAdmin(op); e != nil {
		return FilterSettingsView{}, e
	}
	cfg, e := ValidateFilterInput(in)
	if e != nil {
		return FilterSettingsView{}, e
	}
	if s.filterStore == nil {
		return FilterSettingsView{}, s.internal("hall_filter_store_missing", errNoFilterStore)
	}
	if err := s.filterStore.SetSettings(ctx, filterKV(cfg)); err != nil {
		return FilterSettingsView{}, s.internal("hall_filter_settings_save_failed", err)
	}
	s.invalidateFilterCaches()
	v := filterView(cfg)
	s.audit.Info("hall_filter_settings_saved", zap.String("event", "hall_filter_settings"), zap.Int64("operator_aid", op.AgentID),
		zap.Bool("enabled", v.Enabled), zap.String("mode", v.Mode), zap.Strings("categories", v.Categories),
		zap.Int("custom_words", len(v.CustomWords)), zap.Int("allow_words", len(v.AllowWords)), zap.Strings("allow_domains", v.AllowDomains))
	return v, nil
}

// FilterTestResult 检测试用结果。
type FilterTestResult struct {
	Filtered   bool                 `json:"filtered"`
	Masked     string               `json:"masked"`
	Categories []string             `json:"categories"`
	Hits       []textfilter.HitText `json:"hits"`
}

// TestFilter 用已保存配置（或请求里的预览配置）检测一段文字。
func (s *Service) TestFilter(ctx context.Context, op Actor, content string, preview *FilterInput) (FilterTestResult, error) {
	if e := requireAdmin(op); e != nil {
		return FilterTestResult{}, e
	}
	if e := s.allow(ctx, "filter_test", 30, minute, actorDimension(op)); e != nil {
		return FilterTestResult{}, e
	}
	if strings.TrimSpace(content) == "" || utf8.RuneCountInString(content) > maxContentRunes {
		return FilterTestResult{}, withMsg(errBadParam, "检测内容为 1-1000 字")
	}
	var f *textfilter.Filter
	if preview != nil {
		cfg, e := ValidateFilterInput(preview)
		if e != nil {
			return FilterTestResult{}, e
		}
		f = textfilter.Compile(cfg)
	} else {
		m, err := s.settingsMap(ctx, filterKeys)
		if err != nil {
			return FilterTestResult{}, s.internal("hall_filter_settings_load_failed", err)
		}
		f = textfilter.Compile(parseFilterSettings(m, s.publicDomain))
	}
	res := f.Check(content)
	out := FilterTestResult{Filtered: res.Filtered, Masked: res.Masked, Categories: []string{}, Hits: res.Hits}
	if out.Hits == nil {
		out.Hits = []textfilter.HitText{}
	}
	for _, c := range res.Categories {
		out.Categories = append(out.Categories, string(c))
	}
	return out, nil
}

// FilterEventView 拦截记录（管理员可见原文与访客编号）。
type FilterEventView struct {
	ID           int64    `json:"id"`
	CreatedAt    string   `json:"created_at"`
	Sender       string   `json:"sender"`
	Name         string   `json:"name"`
	Identity     string   `json:"identity"`
	VID          string   `json:"vid"`
	Categories   []string `json:"categories"`
	Original     string   `json:"original"`
	Masked       string   `json:"masked"`
	MailStatus   string   `json:"mail_status"`
	MailAttempts int      `json:"mail_attempts"`
	MailError    string   `json:"mail_error"`
}

// FilterEvents 拦截记录分页（新到旧）。
func (s *Service) FilterEvents(ctx context.Context, op Actor, beforeID int64, limit int) ([]FilterEventView, error) {
	if e := requireAdmin(op); e != nil {
		return nil, e
	}
	if beforeID < 0 || limit < 0 || limit > 50 {
		return nil, withMsg(errBadParam, "limit 必须是 1-50")
	}
	if limit == 0 {
		limit = 20
	}
	if s.filterStore == nil {
		return nil, s.internal("hall_filter_store_missing", errNoFilterStore)
	}
	rows, err := s.filterStore.ListHallFilterEvents(ctx, beforeID, limit)
	if err != nil {
		return nil, s.internal("hall_filter_events_load_failed", err)
	}
	out := make([]FilterEventView, 0, len(rows))
	for _, r := range rows {
		orig, err := s.origCipher.Decrypt(r.OriginalEnc)
		if err != nil {
			orig = "（原文无法解密）"
		}
		cats := []string{}
		for _, c := range strings.Split(r.Categories, ",") {
			if c != "" {
				cats = append(cats, c)
			}
		}
		v := FilterEventView{ID: r.ID, CreatedAt: formatTime(r.CreatedAt), Sender: r.Sender, Name: r.Name, Identity: r.Identity,
			Categories: cats, Original: orig, Masked: r.Masked, MailStatus: r.MailStatus, MailAttempts: r.MailAttempts, MailError: r.MailError}
		if r.Sender == "visitor" {
			v.VID = r.SenderRef
		}
		out = append(out, v)
	}
	return out, nil
}

// AlertMail 读取告警邮件设置（不含密码）。
func (s *Service) AlertMail(ctx context.Context, op Actor) (AlertMailView, error) {
	if e := requireAdmin(op); e != nil {
		return AlertMailView{}, e
	}
	c, err := s.alertSettings(ctx, true)
	if err != nil {
		return AlertMailView{}, s.internal("hall_alert_settings_load_failed", err)
	}
	return c.view(), nil
}

// SaveAlertMail 保存告警邮件设置；密码为空表示不修改。
func (s *Service) SaveAlertMail(ctx context.Context, op Actor, in *AlertMailInput) (AlertMailView, error) {
	if e := requireAdmin(op); e != nil {
		return AlertMailView{}, e
	}
	kv, pwChanged, e := s.validateAlertInput(in)
	if e != nil {
		return AlertMailView{}, e
	}
	if s.filterStore == nil {
		return AlertMailView{}, s.internal("hall_filter_store_missing", errNoFilterStore)
	}
	if err := s.filterStore.SetSettings(ctx, kv); err != nil {
		return AlertMailView{}, s.internal("hall_alert_settings_save_failed", err)
	}
	s.invalidateFilterCaches()
	c, err := s.alertSettings(ctx, true)
	if err != nil {
		return AlertMailView{}, s.internal("hall_alert_settings_load_failed", err)
	}
	v := c.view()
	s.audit.Info("hall_alert_settings_saved", zap.String("event", "hall_alert_settings"), zap.Int64("operator_aid", op.AgentID),
		zap.Bool("enabled", v.Enabled), zap.Strings("to", v.To), zap.String("from", v.From), zap.String("smtp_host", v.SMTPHost),
		zap.Int("smtp_port", v.SMTPPort), zap.String("smtp_security", v.SMTPSecurity), zap.Bool("password_changed", pwChanged))
	return v, nil
}

// TestAlertMail 用已保存配置立即发一封测试邮件。
func (s *Service) TestAlertMail(ctx context.Context, op Actor) ([]string, error) {
	if e := requireAdmin(op); e != nil {
		return nil, e
	}
	if e := s.allow(ctx, "alert_mail_test", 5, minute, actorDimension(op)); e != nil {
		return nil, e
	}
	c, err := s.alertSettings(ctx, true)
	if err != nil {
		return nil, s.internal("hall_alert_settings_load_failed", err)
	}
	if !c.configured() {
		return nil, invalid(errAlertInvalid, "邮件设置不完整（收件邮箱、发件邮箱、SMTP 服务器、端口、用户名与密码）")
	}
	now := s.now()
	msg := mailer.Message{
		To:      c.To,
		Subject: "[客服大厅] 敏感词告警测试邮件 · " + now.In(beijing).Format("2006-01-02 15:04"),
		Text: "这是一封测试邮件：多人大厅敏感词屏蔽的告警邮件配置可以正常发信。\n\n发送时间（北京时间）：" + now.In(beijing).Format("2006-01-02 15:04:05 -07:00") +
			"\n发件邮箱：" + c.SMTP.From + "\n\n——\n此邮件由客服系统自动发送。\n",
	}
	sctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := s.sendMail(sctx, c.SMTP, msg); err != nil {
		s.biz.Warn("hall_alert_test_mail_failed", zap.String("event", "hall_alert_test_mail"), zap.Int64("operator_aid", op.AgentID),
			zap.String("reason", mailer.ReasonOf(err)), zap.String("error", sanitizeMailErr(err, c.SMTP.Password)))
		return nil, withMsg(errMailSendFailed, "测试邮件发送失败："+mailer.ReasonOf(err))
	}
	s.audit.Info("hall_alert_test_mail_sent", zap.String("event", "hall_alert_test_mail"), zap.Int64("operator_aid", op.AgentID), zap.Strings("to", c.To))
	return c.To, nil
}
