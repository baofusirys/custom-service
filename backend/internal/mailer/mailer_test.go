package mailer

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP 本机最小 ESMTP 服务器（AUTH PLAIN，收件写入内存）。
type fakeSMTP struct {
	ln       net.Listener
	user     string
	pass     string
	mu       sync.Mutex
	mails    []string
	rcpts    [][]string
	rejectTo string
}

func startFake(t *testing.T, user, pass string) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, user: user, pass: pass}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	authed := false
	var rcpts []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			w("250-fake")
			w("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(up, "AUTH PLAIN "):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[11:]))
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && parts[1] == f.user && parts[2] == f.pass {
				authed = true
				w("235 ok")
			} else {
				w("535 auth failed")
			}
		case strings.HasPrefix(up, "MAIL FROM:"):
			if !authed {
				w("530 auth required")
				continue
			}
			w("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			addr := strings.Trim(cmd[8:], "<> ")
			if addr == f.rejectTo {
				w("550 no such user")
				continue
			}
			rcpts = append(rcpts, addr)
			w("250 ok")
		case up == "DATA":
			w("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.mails = append(f.mails, b.String())
			f.rcpts = append(f.rcpts, rcpts)
			f.mu.Unlock()
			w("250 queued")
		case up == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func cfgFor(f *fakeSMTP) Config {
	return Config{Host: "127.0.0.1", Port: f.port(), Security: SecurityNone, Username: "qa", Password: "qa-pass", From: "kelili-test@example.test", FromName: "客服大厅告警"}
}

func TestSendOK(t *testing.T) {
	f := startFake(t, "qa", "qa-pass")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Send(ctx, cfgFor(f), Message{To: []string{"a@example.test", "b@example.test"}, Subject: "测试\r\nBcc: evil@x.com", Text: "原文：加我微信\n第二行"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.mails) != 1 || len(f.rcpts[0]) != 2 {
		t.Fatalf("应投递 1 封给 2 人：%v", f.rcpts)
	}
	m := f.mails[0]
	if strings.Contains(m, "Bcc:") {
		t.Fatal("主题换行注入未被清除")
	}
	if !strings.Contains(m, "=?UTF-8?b?") && !strings.Contains(m, "=?UTF-8?B?") {
		t.Fatal("中文主题应按 RFC 2047 编码")
	}
	if !strings.Contains(m, "+0800") {
		t.Fatal("Date 应为北京时间")
	}
	body := m[strings.Index(m, "\r\n\r\n")+4:]
	dec, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(body, "\r\n", ""))
	if !strings.Contains(string(dec), "加我微信") {
		t.Fatalf("正文应完整：%q", dec)
	}
}

func TestSendErrorsClassified(t *testing.T) {
	f := startFake(t, "qa", "qa-pass")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bad := cfgFor(f)
	bad.Password = "wrong"
	var me *Error
	if err := Send(ctx, bad, Message{To: []string{"a@example.test"}, Subject: "x", Text: "x"}); !errors.As(err, &me) || me.Kind != KindAuth {
		t.Fatalf("应为认证失败：%v", err)
	}
	f.rejectTo = "nobody@example.test"
	if err := Send(ctx, cfgFor(f), Message{To: []string{"nobody@example.test"}, Subject: "x", Text: "x"}); !errors.As(err, &me) || me.Kind != KindRecipient {
		t.Fatalf("应为收件人被拒：%v", err)
	}
	closed := cfgFor(f)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed.Port = ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	if err := Send(ctx, closed, Message{To: []string{"a@example.test"}, Subject: "x", Text: "x"}); !errors.As(err, &me) || me.Kind != KindConnect {
		t.Fatalf("应为连接失败：%v", err)
	}
	if ReasonOf(err1()) == "" || !strings.Contains((&Error{Kind: KindAuth}).Reason(), "登录") {
		t.Fatal("中文原因缺失")
	}
}

func err1() error { return &Error{Kind: KindTLS, Err: errors.New("x")} }

func TestValidate(t *testing.T) {
	base := Config{Host: "smtp.163.com", Port: 465, Security: SecuritySSL, Username: "u", Password: "p", From: "a@163.com"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(c *Config){
		"none 非本机": func(c *Config) { c.Security = SecurityNone },
		"端口":       func(c *Config) { c.Port = 0 },
		"加密方式":     func(c *Config) { c.Security = "tls1" },
		"发件人换行":    func(c *Config) { c.From = "a@163.com\r\nBcc: x@y.z" },
		"主机含斜杠":    func(c *Config) { c.Host = "smtp.163.com/x" },
		"密码换行":     func(c *Config) { c.Password = "p\r\nRSET" },
		"发件人带显示名":  func(c *Config) { c.From = "Kelili <a@163.com>" },
	} {
		c := base
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
	for _, bad := range []string{"", "abc", "a@b", "a@b.c,d@e.f", "<a@b.c>", "a@b.c\n"} {
		if _, err := ParseAddress(bad); err == nil {
			t.Errorf("邮箱 %q 应被拒绝", bad)
		}
	}
	if !IsLoopbackHost("127.0.0.1") || !IsLoopbackHost("localhost") || IsLoopbackHost("smtp.163.com") {
		t.Fatal("本机地址判断错误")
	}
}
