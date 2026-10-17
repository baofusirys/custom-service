package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// [122] 最小 SMTP 发信（标准库 net/smtp），供大厅敏感消息告警等系统通知复用。
//   - 加密：ssl（465 隐式 TLS）、starttls（587）、none（仅允许本机地址，测试用；防止明文传输密码）；
//   - TLS 一律校验证书与主机名，最低 TLS 1.2；整次发送受 ctx 截止时间约束（连接级 deadline）；
//   - 邮件头防注入：地址用 net/mail 解析，主题与显示名去掉 CR/LF 后按 RFC 2047 编码；正文 UTF-8 + base64；
//   - 错误分类为中文原因（不含服务器原始响应），原始错误只供服务端日志使用（调用方负责脱敏）。

// Security 传输加密方式。
type Security string

const (
	SecuritySSL      Security = "ssl"
	SecuritySTARTTLS Security = "starttls"
	SecurityNone     Security = "none"
)

// Config SMTP 配置。Password 只在内存中使用。
type Config struct {
	Host     string
	Port     int
	Security Security
	Username string
	Password string
	From     string
	FromName string
	// TLSConfig 仅测试注入自签名根证书用；生产为 nil（使用系统根证书并校验主机名）。
	TLSConfig *tls.Config
}

// Message 一封纯文本邮件。
type Message struct {
	To      []string
	Subject string
	Text    string
}

// Kind 失败类别。
type Kind string

const (
	KindConfig    Kind = "config"
	KindConnect   Kind = "connect"
	KindTLS       Kind = "tls"
	KindAuth      Kind = "auth"
	KindSender    Kind = "sender"
	KindRecipient Kind = "recipient"
	KindData      Kind = "data"
	KindTimeout   Kind = "timeout"
)

// Error 分类后的发信错误。
type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string { return string(e.Kind) + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Reason 给管理员看的中文原因。
func (e *Error) Reason() string {
	switch e.Kind {
	case KindConfig:
		return "邮件配置不完整或不合法"
	case KindConnect:
		return "无法连接邮件服务器，请检查服务器地址与端口"
	case KindTLS:
		return "与邮件服务器建立加密连接失败，请检查加密方式与端口"
	case KindAuth:
		return "邮箱登录失败，请检查用户名与密码/授权码"
	case KindSender:
		return "发件邮箱被邮件服务器拒绝（通常需要与登录用户名一致）"
	case KindRecipient:
		return "收件邮箱被邮件服务器拒绝"
	case KindTimeout:
		return "连接邮件服务器超时"
	default:
		return "邮件服务器拒绝了这封邮件"
	}
}

// ReasonOf 任意错误的中文原因。
func ReasonOf(err error) string {
	var me *Error
	if errors.As(err, &me) {
		return me.Reason()
	}
	return "邮件发送失败"
}

func wrap(kind Kind, err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		kind = KindTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		kind = KindTimeout
	}
	return &Error{Kind: kind, Err: err}
}

// IsLoopbackHost 是否本机地址（none 加密方式只允许这些地址）。
func IsLoopbackHost(host string) bool {
	h := strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ParseAddress 校验单个邮箱地址（只接受纯地址，不接受显示名、换行与多个地址）。
func ParseAddress(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n") {
		return "", errors.New("邮箱格式不正确")
	}
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 254 || strings.ContainsAny(s, "\r\n<>,;\"") {
		return "", errors.New("邮箱格式不正确")
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" {
		return "", errors.New("邮箱格式不正确")
	}
	at := strings.LastIndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || !strings.Contains(s[at+1:], ".") {
		return "", errors.New("邮箱格式不正确")
	}
	return s, nil
}

// Validate 检查配置完整与合法。
func (c Config) Validate() error {
	if strings.TrimSpace(c.Host) == "" || strings.ContainsAny(c.Host, " \r\n/") {
		return &Error{Kind: KindConfig, Err: errors.New("smtp host invalid")}
	}
	if c.Port < 1 || c.Port > 65535 {
		return &Error{Kind: KindConfig, Err: errors.New("smtp port invalid")}
	}
	switch c.Security {
	case SecuritySSL, SecuritySTARTTLS:
	case SecurityNone:
		if !IsLoopbackHost(c.Host) {
			return &Error{Kind: KindConfig, Err: errors.New("plaintext smtp only allowed for loopback")}
		}
	default:
		return &Error{Kind: KindConfig, Err: errors.New("smtp security invalid")}
	}
	if _, err := ParseAddress(c.From); err != nil {
		return &Error{Kind: KindConfig, Err: errors.New("from invalid")}
	}
	if strings.ContainsAny(c.Username, "\r\n") || strings.ContainsAny(c.Password, "\r\n") {
		return &Error{Kind: KindConfig, Err: errors.New("credential contains newline")}
	}
	return nil
}

var beijing = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}()

func cleanHeader(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	return strings.TrimSpace(s)
}

// Build 生成 RFC 5322 邮件内容（导出供测试检查）。
func Build(c Config, m Message, now time.Time) ([]byte, error) {
	var b bytes.Buffer
	from := c.From
	if name := cleanHeader(c.FromName); name != "" {
		from = mime.BEncoding.Encode("UTF-8", name) + " <" + c.From + ">"
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	domain := c.From[strings.LastIndexByte(c.From, '@')+1:]
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(m.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.BEncoding.Encode("UTF-8", cleanHeader(m.Subject)))
	fmt.Fprintf(&b, "Date: %s\r\n", now.In(beijing).Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s.%d@%s>\r\n", hex.EncodeToString(nonce), now.UnixNano(), domain)
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(m.Text, "\n", "\r\n")))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes(), nil
}

// Send 发送一封邮件。ctx 必须带截止时间（未带时默认 30 秒）。
func Send(ctx context.Context, c Config, m Message) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if len(m.To) == 0 {
		return &Error{Kind: KindConfig, Err: errors.New("no recipients")}
	}
	for _, to := range m.To {
		if _, err := ParseAddress(to); err != nil {
			return &Error{Kind: KindConfig, Err: errors.New("recipient invalid")}
		}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	deadline, _ := ctx.Deadline()
	body, err := Build(c, m, time.Now())
	if err != nil {
		return &Error{Kind: KindData, Err: err}
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if c.TLSConfig != nil {
		tlsCfg = c.TLSConfig.Clone()
		if tlsCfg.ServerName == "" {
			tlsCfg.ServerName = c.Host
		}
	}
	d := &net.Dialer{Timeout: time.Until(deadline)}
	var conn net.Conn
	if c.Security == SecuritySSL {
		raw, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return wrap(KindConnect, err)
		}
		_ = raw.SetDeadline(deadline)
		tc := tls.Client(raw, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return wrap(KindTLS, err)
		}
		conn = tc
	} else {
		raw, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return wrap(KindConnect, err)
		}
		conn = raw
	}
	_ = conn.SetDeadline(deadline)
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		_ = conn.Close()
		return wrap(KindConnect, err)
	}
	defer cl.Close()
	if c.Security == SecuritySTARTTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return &Error{Kind: KindTLS, Err: errors.New("server does not support STARTTLS")}
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return wrap(KindTLS, err)
		}
	}
	if c.Username != "" {
		if ok, params := cl.Extension("AUTH"); ok {
			var auth smtp.Auth
			if strings.Contains(strings.ToUpper(params), "PLAIN") {
				auth = smtp.PlainAuth("", c.Username, c.Password, c.Host)
			} else if strings.Contains(strings.ToUpper(params), "LOGIN") {
				auth = &loginAuth{user: c.Username, pass: c.Password, host: c.Host}
			}
			if auth == nil {
				return &Error{Kind: KindAuth, Err: errors.New("no supported auth mechanism")}
			}
			if err := cl.Auth(auth); err != nil {
				return wrap(KindAuth, err)
			}
		} else {
			return &Error{Kind: KindAuth, Err: errors.New("server does not support AUTH")}
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return wrap(KindSender, err)
	}
	for _, to := range m.To {
		if err := cl.Rcpt(to); err != nil {
			return wrap(KindRecipient, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return wrap(KindData, err)
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return wrap(KindData, err)
	}
	if err := w.Close(); err != nil {
		return wrap(KindData, err)
	}
	_ = cl.Quit()
	return nil
}

// loginAuth AUTH LOGIN（部分邮箱服务只支持此方式）；与 PlainAuth 一样要求 TLS 或本机地址。
type loginAuth struct{ user, pass, host string }

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !IsLoopbackHost(server.Name) {
		return "", nil, errors.New("unencrypted connection")
	}
	if server.Name != a.host {
		return "", nil, errors.New("wrong host name")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.pass), nil
	}
	return nil, errors.New("unexpected server challenge")
}
