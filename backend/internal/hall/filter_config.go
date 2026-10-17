package hall

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"golang.org/x/net/idna"

	"github.com/custom-service/backend/internal/mailer"
	"github.com/custom-service/backend/internal/security"
	"github.com/custom-service/backend/internal/textfilter"
)

// [122] 大厅敏感词屏蔽与告警邮件的配置（契约第 9 节）。
// 存储：settings 表专用键（不在通用 /api/admin/settings 白名单里，避免密文与词表从通用接口泄露或被绕过校验）。
// 邮件配置逐项取值：后台保存值 → 环境变量默认值（HALL_ALERT_*）→ 空；SMTP 密码用 DATA_AES_KEY 派生的独立子密钥加密存储。

const (
	KeyFilterEnabled        = "hall_filter_enabled"
	KeyFilterMode           = "hall_filter_mode"
	KeyFilterCategories     = "hall_filter_categories"
	KeyFilterCustomWords    = "hall_filter_custom_words"
	KeyFilterAllowWords     = "hall_filter_allow_words"
	KeyFilterAllowDomains   = "hall_filter_allow_domains"
	KeyAlertEnabled         = "hall_alert_enabled"
	KeyAlertTo              = "hall_alert_to"
	KeyAlertFrom            = "hall_alert_from"
	KeyAlertSMTPHost        = "hall_alert_smtp_host"
	KeyAlertSMTPPort        = "hall_alert_smtp_port"
	KeyAlertSMTPSecurity    = "hall_alert_smtp_security"
	KeyAlertSMTPUsername    = "hall_alert_smtp_username"
	KeyAlertSMTPPasswordEnc = "hall_alert_smtp_password_enc"

	smtpKeyInfo     = "custom-service/hall/alert-smtp/v1"
	originalKeyInfo = "custom-service/hall/filter-original/v1"
	maxAlertTo      = 5
	filterCacheTTL  = 5 * time.Second
	alertMailFrom   = "客服大厅告警"
)

var filterKeys = []string{KeyFilterEnabled, KeyFilterMode, KeyFilterCategories, KeyFilterCustomWords, KeyFilterAllowWords, KeyFilterAllowDomains}
var alertKeys = []string{KeyAlertEnabled, KeyAlertTo, KeyAlertFrom, KeyAlertSMTPHost, KeyAlertSMTPPort, KeyAlertSMTPSecurity, KeyAlertSMTPUsername, KeyAlertSMTPPasswordEnc}

var (
	errFilterInvalid = newErr(http.StatusBadRequest, 40062, "敏感词设置不合法")
	errAlertInvalid  = newErr(http.StatusBadRequest, 40063, "邮件设置不合法")
)

// AlertDefaults 告警邮件的环境变量默认值（生产由 compose 注入主站发货邮箱 kelili 的 SMTP 配置）。
type AlertDefaults struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     string
	To       []string
}

func deriveCipher(dataKey []byte, info string) (*security.Cipher, error) {
	key, err := hkdf.Key(sha256.New, dataKey, nil, info, 32)
	if err != nil {
		return nil, err
	}
	return security.NewCipher(key)
}

// ============ 敏感词设置 ============

// FilterSettingsView 管理接口返回结构。
type FilterSettingsView struct {
	Enabled      bool                `json:"enabled"`
	Mode         string              `json:"mode"`
	Categories   []string            `json:"categories"`
	CustomWords  []string            `json:"custom_words"`
	AllowWords   []string            `json:"allow_words"`
	AllowDomains []string            `json:"allow_domains"`
	Builtin      map[string][]string `json:"builtin"`
	Limits       map[string]int      `json:"limits"`
}

// FilterInput 保存 / 预览请求体（全部必填）。
type FilterInput struct {
	Enabled      *bool    `json:"enabled"`
	Mode         *string  `json:"mode"`
	Categories   []string `json:"categories"`
	CustomWords  []string `json:"custom_words"`
	AllowWords   []string `json:"allow_words"`
	AllowDomains []string `json:"allow_domains"`
}

func filterLimits() map[string]int {
	return map[string]int{"custom_words": textfilter.MaxCustomWords, "allow_words": textfilter.MaxAllowWords,
		"allow_domains": textfilter.MaxAllowDomains, "word_max_runes": textfilter.MaxWordRunes}
}

func invalid(base *Error, format string, args ...any) *Error {
	c := *base
	c.Msg = base.Msg + "：" + fmt.Sprintf(format, args...)
	return &c
}

func cleanWords(list []string, max int, label string) ([]string, *Error) {
	seen := map[string]bool{}
	out := []string{}
	for i, raw := range list {
		w := strings.TrimSpace(raw)
		if w == "" {
			continue
		}
		if strings.ContainsAny(w, "\r\n") || utf8.RuneCountInString(w) > textfilter.MaxWordRunes {
			return nil, invalid(errFilterInvalid, "%s第 %d 项超过 %d 字或含换行", label, i+1, textfilter.MaxWordRunes)
		}
		n := textfilter.NormalizeWord(w)
		if n == "" {
			return nil, invalid(errFilterInvalid, "%s第 %d 项只有符号或空白", label, i+1)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, w)
	}
	if len(out) > max {
		return nil, invalid(errFilterInvalid, "%s最多 %d 项", label, max)
	}
	return out, nil
}

var hostnamePattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// normalizeDomain 放行域名：去协议/路径/端口/前导 "*." 与 "."，中文转 punycode，校验主机名格式。
func normalizeDomain(raw string) (string, bool) {
	d := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(d, "://"); i >= 0 {
		d = d[i+3:]
	}
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	if h, _, err := net.SplitHostPort(d); err == nil {
		d = h
	}
	d = strings.TrimPrefix(d, "*.")
	d = strings.Trim(d, ".")
	if d == "" || len(d) > 253 {
		return "", false
	}
	a, err := idna.Lookup.ToASCII(d)
	if err != nil {
		return "", false
	}
	a = strings.ToLower(a)
	if !hostnamePattern.MatchString(a) || !strings.Contains(a, ".") {
		return "", false
	}
	return a, true
}

// ValidateFilterInput 校验并规范化（返回可直接保存/编译的配置）。
func ValidateFilterInput(in *FilterInput) (textfilter.Config, *Error) {
	var cfg textfilter.Config
	if in == nil || in.Enabled == nil || in.Mode == nil || in.Categories == nil || in.CustomWords == nil || in.AllowWords == nil || in.AllowDomains == nil {
		return cfg, invalid(errFilterInvalid, "缺少必填项（enabled、mode、categories、custom_words、allow_words、allow_domains）")
	}
	cfg.Enabled = *in.Enabled
	switch textfilter.Mode(*in.Mode) {
	case textfilter.ModeMask, textfilter.ModeWhole:
		cfg.Mode = textfilter.Mode(*in.Mode)
	default:
		return cfg, invalid(errFilterInvalid, "处理方式只能是 mask 或 whole")
	}
	cfg.Categories = map[textfilter.Category]bool{}
	valid := map[string]bool{}
	for _, c := range textfilter.AllCategories {
		valid[string(c)] = true
	}
	for _, c := range in.Categories {
		if !valid[c] {
			return cfg, invalid(errFilterInvalid, "未知的拦截类别 %q", c)
		}
		cfg.Categories[textfilter.Category(c)] = true
	}
	var e *Error
	if cfg.CustomWords, e = cleanWords(in.CustomWords, textfilter.MaxCustomWords, "自定义屏蔽词"); e != nil {
		return cfg, e
	}
	if cfg.AllowWords, e = cleanWords(in.AllowWords, textfilter.MaxAllowWords, "放行词"); e != nil {
		return cfg, e
	}
	seen := map[string]bool{}
	cfg.AllowDomains = []string{}
	for i, raw := range in.AllowDomains {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		d, ok := normalizeDomain(raw)
		if !ok {
			return cfg, invalid(errFilterInvalid, "放行域名第 %d 项格式不正确", i+1)
		}
		if !seen[d] {
			seen[d] = true
			cfg.AllowDomains = append(cfg.AllowDomains, d)
		}
	}
	if len(cfg.AllowDomains) > textfilter.MaxAllowDomains {
		return cfg, invalid(errFilterInvalid, "放行域名最多 %d 项", textfilter.MaxAllowDomains)
	}
	return cfg, nil
}

// defaultAllowDomains 未保存过放行域名时，默认放行本站主域（由 PUBLIC_DOMAIN 推出，不写死）。
func defaultAllowDomains(publicDomain string) []string {
	d, ok := normalizeDomain(publicDomain)
	if !ok || net.ParseIP(d) != nil {
		return []string{}
	}
	labels := strings.Split(d, ".")
	n := 2
	if len(labels) >= 3 && len(labels[len(labels)-1]) == 2 {
		switch labels[len(labels)-2] {
		case "com", "net", "org", "gov", "edu", "ac", "co":
			n = 3
		}
	}
	if len(labels) < n {
		return []string{d}
	}
	return []string{strings.Join(labels[len(labels)-n:], ".")}
}

func jsonList(s string) ([]string, bool) {
	var out []string
	if strings.TrimSpace(s) == "" {
		return []string{}, true
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, false
	}
	return out, true
}

// parseFilterSettings 从 settings 读出配置；缺失或存量非法值回退默认（不因脏数据让大厅不可用）。
func parseFilterSettings(m map[string]string, publicDomain string) textfilter.Config {
	cfg := textfilter.Config{Enabled: true, Mode: textfilter.ModeMask, Categories: map[textfilter.Category]bool{},
		CustomWords: []string{}, AllowWords: []string{}, AllowDomains: defaultAllowDomains(publicDomain)}
	for _, c := range textfilter.AllCategories {
		cfg.Categories[c] = true
	}
	if v, ok := m[KeyFilterEnabled]; ok && (v == "true" || v == "false") {
		cfg.Enabled = v == "true"
	}
	if v, ok := m[KeyFilterMode]; ok && (v == string(textfilter.ModeMask) || v == string(textfilter.ModeWhole)) {
		cfg.Mode = textfilter.Mode(v)
	}
	if v, ok := m[KeyFilterCategories]; ok {
		cats := map[textfilter.Category]bool{}
		for _, c := range strings.Split(v, ",") {
			for _, k := range textfilter.AllCategories {
				if strings.TrimSpace(c) == string(k) {
					cats[k] = true
				}
			}
		}
		cfg.Categories = cats
	}
	in := &FilterInput{Enabled: &cfg.Enabled, Mode: (*string)(&cfg.Mode), Categories: []string{}, CustomWords: []string{}, AllowWords: []string{}, AllowDomains: cfg.AllowDomains}
	if v, ok := m[KeyFilterCustomWords]; ok {
		if l, ok := jsonList(v); ok {
			in.CustomWords = l
		}
	}
	if v, ok := m[KeyFilterAllowWords]; ok {
		if l, ok := jsonList(v); ok {
			in.AllowWords = l
		}
	}
	if v, ok := m[KeyFilterAllowDomains]; ok {
		if l, ok := jsonList(v); ok {
			in.AllowDomains = l
		}
	}
	if w, e := cleanWords(in.CustomWords, textfilter.MaxCustomWords, ""); e == nil {
		cfg.CustomWords = w
	}
	if w, e := cleanWords(in.AllowWords, textfilter.MaxAllowWords, ""); e == nil {
		cfg.AllowWords = w
	}
	doms := []string{}
	for _, raw := range in.AllowDomains {
		if d, ok := normalizeDomain(raw); ok {
			doms = append(doms, d)
		}
	}
	cfg.AllowDomains = doms
	return cfg
}

func filterView(cfg textfilter.Config) FilterSettingsView {
	cats := []string{}
	for _, c := range textfilter.AllCategories {
		if cfg.Categories[c] {
			cats = append(cats, string(c))
		}
	}
	nz := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	return FilterSettingsView{Enabled: cfg.Enabled, Mode: string(cfg.Mode), Categories: cats, CustomWords: nz(cfg.CustomWords),
		AllowWords: nz(cfg.AllowWords), AllowDomains: nz(cfg.AllowDomains), Builtin: textfilter.BuiltinWords(), Limits: filterLimits()}
}

func filterKV(cfg textfilter.Config) map[string]string {
	v := filterView(cfg)
	enc := func(l []string) string { b, _ := json.Marshal(l); return string(b) }
	return map[string]string{
		KeyFilterEnabled: strconv.FormatBool(cfg.Enabled), KeyFilterMode: string(cfg.Mode), KeyFilterCategories: strings.Join(v.Categories, ","),
		KeyFilterCustomWords: enc(v.CustomWords), KeyFilterAllowWords: enc(v.AllowWords), KeyFilterAllowDomains: enc(v.AllowDomains),
	}
}

// ============ 告警邮件设置 ============

// AlertMailView 管理接口返回结构（永不包含密码）。
type AlertMailView struct {
	Enabled      bool     `json:"enabled"`
	To           []string `json:"to"`
	From         string   `json:"from"`
	SMTPHost     string   `json:"smtp_host"`
	SMTPPort     int      `json:"smtp_port"`
	SMTPSecurity string   `json:"smtp_security"`
	SMTPUsername string   `json:"smtp_username"`
	PasswordSet  bool     `json:"password_set"`
	Configured   bool     `json:"configured"`
}

// AlertMailInput 保存请求体；SMTPPassword 省略或空串表示不修改。
type AlertMailInput struct {
	Enabled      *bool    `json:"enabled"`
	To           []string `json:"to"`
	From         *string  `json:"from"`
	SMTPHost     *string  `json:"smtp_host"`
	SMTPPort     *int     `json:"smtp_port"`
	SMTPSecurity *string  `json:"smtp_security"`
	SMTPUsername *string  `json:"smtp_username"`
	SMTPPassword *string  `json:"smtp_password"`
}

// alertConfig 生效中的告警邮件配置（含解密后的密码，只在内存中使用）。
type alertConfig struct {
	Enabled bool
	To      []string
	SMTP    mailer.Config
}

func (c alertConfig) configured() bool {
	return len(c.To) > 0 && c.SMTP.Validate() == nil && (c.SMTP.Username == "" || c.SMTP.Password != "")
}

func (c alertConfig) view() AlertMailView {
	to := c.To
	if to == nil {
		to = []string{}
	}
	return AlertMailView{Enabled: c.Enabled, To: to, From: c.SMTP.From, SMTPHost: c.SMTP.Host, SMTPPort: c.SMTP.Port,
		SMTPSecurity: string(c.SMTP.Security), SMTPUsername: c.SMTP.Username, PasswordSet: c.SMTP.Password != "", Configured: c.configured()}
}

func cleanAddrList(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, a := range list {
		if v, err := mailer.ParseAddress(a); err == nil && !seen[strings.ToLower(v)] {
			seen[strings.ToLower(v)] = true
			out = append(out, v)
		}
	}
	return out
}

func defaultSecurity(port int) mailer.Security {
	if port == 587 || port == 25 {
		return mailer.SecuritySTARTTLS
	}
	return mailer.SecuritySSL
}

// loadAlertConfig 逐项：后台保存值 → 环境变量默认值。
func (s *Service) loadAlertConfig(m map[string]string) alertConfig {
	d := s.alertDefaults
	c := alertConfig{Enabled: true, To: cleanAddrList(d.To)}
	c.SMTP = mailer.Config{Host: strings.TrimSpace(d.Host), Port: d.Port, Security: mailer.Security(strings.ToLower(strings.TrimSpace(d.Security))),
		Username: strings.TrimSpace(d.Username), Password: d.Password, From: strings.TrimSpace(d.From), FromName: alertMailFrom}
	if v, ok := m[KeyAlertEnabled]; ok && (v == "true" || v == "false") {
		c.Enabled = v == "true"
	}
	if v, ok := m[KeyAlertTo]; ok {
		if l, ok := jsonList(v); ok {
			c.To = cleanAddrList(l)
		}
	}
	if v, ok := m[KeyAlertFrom]; ok {
		c.SMTP.From = strings.TrimSpace(v)
	}
	if v, ok := m[KeyAlertSMTPHost]; ok {
		c.SMTP.Host = strings.TrimSpace(v)
	}
	if v, ok := m[KeyAlertSMTPPort]; ok {
		if p, err := strconv.Atoi(v); err == nil {
			c.SMTP.Port = p
		}
	}
	if v, ok := m[KeyAlertSMTPSecurity]; ok {
		c.SMTP.Security = mailer.Security(v)
	}
	if v, ok := m[KeyAlertSMTPUsername]; ok {
		c.SMTP.Username = v
	}
	if v, ok := m[KeyAlertSMTPPasswordEnc]; ok && v != "" {
		if pw, err := s.smtpCipher.Decrypt(v); err == nil {
			c.SMTP.Password = pw
		} else {
			s.biz.Error("hall_alert_password_decrypt_failed", zap.Error(err))
			c.SMTP.Password = ""
		}
	}
	if c.SMTP.Security == "" && c.SMTP.Port > 0 {
		c.SMTP.Security = defaultSecurity(c.SMTP.Port)
	}
	return c
}

// validateAlertInput 校验后台提交；返回要写入 settings 的键值（密码已加密，未修改则不含该键）。
func (s *Service) validateAlertInput(in *AlertMailInput) (map[string]string, bool, *Error) {
	if in == nil || in.Enabled == nil || in.To == nil || in.From == nil || in.SMTPHost == nil || in.SMTPPort == nil || in.SMTPSecurity == nil || in.SMTPUsername == nil {
		return nil, false, invalid(errAlertInvalid, "缺少必填项")
	}
	to := []string{}
	seen := map[string]bool{}
	for i, a := range in.To {
		if strings.TrimSpace(a) == "" {
			continue
		}
		v, err := mailer.ParseAddress(a)
		if err != nil {
			return nil, false, invalid(errAlertInvalid, "收件邮箱第 %d 个格式不正确", i+1)
		}
		if !seen[strings.ToLower(v)] {
			seen[strings.ToLower(v)] = true
			to = append(to, v)
		}
	}
	if len(to) > maxAlertTo {
		return nil, false, invalid(errAlertInvalid, "收件邮箱最多 %d 个", maxAlertTo)
	}
	if *in.Enabled && len(to) == 0 {
		return nil, false, invalid(errAlertInvalid, "开启邮件通知时至少填写 1 个收件邮箱")
	}
	from, err := mailer.ParseAddress(*in.From)
	if err != nil {
		return nil, false, invalid(errAlertInvalid, "发件邮箱格式不正确")
	}
	host := strings.ToLower(strings.TrimSpace(*in.SMTPHost))
	if net.ParseIP(strings.Trim(host, "[]")) == nil {
		if _, ok := normalizeDomain(host); !ok || strings.ContainsAny(host, "/:") {
			if host != "localhost" {
				return nil, false, invalid(errAlertInvalid, "SMTP 服务器地址格式不正确")
			}
		}
	}
	if *in.SMTPPort < 1 || *in.SMTPPort > 65535 {
		return nil, false, invalid(errAlertInvalid, "SMTP 端口必须是 1-65535")
	}
	sec := mailer.Security(*in.SMTPSecurity)
	switch sec {
	case mailer.SecuritySSL, mailer.SecuritySTARTTLS:
	case mailer.SecurityNone:
		if !mailer.IsLoopbackHost(host) {
			return nil, false, invalid(errAlertInvalid, "不加密只允许本机邮件服务器（防止明文传输密码）")
		}
	default:
		return nil, false, invalid(errAlertInvalid, "加密方式只能是 ssl、starttls 或 none")
	}
	user := strings.TrimSpace(*in.SMTPUsername)
	if utf8.RuneCountInString(user) > 128 || strings.ContainsAny(user, "\r\n") {
		return nil, false, invalid(errAlertInvalid, "SMTP 用户名过长或含换行")
	}
	toJSON, _ := json.Marshal(to)
	kv := map[string]string{
		KeyAlertEnabled: strconv.FormatBool(*in.Enabled), KeyAlertTo: string(toJSON), KeyAlertFrom: from,
		KeyAlertSMTPHost: host, KeyAlertSMTPPort: strconv.Itoa(*in.SMTPPort), KeyAlertSMTPSecurity: string(sec), KeyAlertSMTPUsername: user,
	}
	pwChanged := false
	if in.SMTPPassword != nil && *in.SMTPPassword != "" {
		pw := *in.SMTPPassword
		if utf8.RuneCountInString(pw) > 256 || strings.ContainsAny(pw, "\r\n") {
			return nil, false, invalid(errAlertInvalid, "SMTP 密码过长或含换行")
		}
		enc, err := s.smtpCipher.Encrypt(pw)
		if err != nil {
			return nil, false, errInternal
		}
		kv[KeyAlertSMTPPasswordEnc] = enc
		pwChanged = true
	}
	return kv, pwChanged, nil
}

// errNoFilterStore 数据层不支持拦截记录（不应发生）。
var errNoFilterStore = errors.New("hall: filter store unavailable")

func (s *Service) settingsMap(ctx context.Context, keys []string) (map[string]string, error) {
	return s.store.GetSettingsMap(ctx, keys)
}
