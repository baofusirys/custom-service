package hall

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/custom-service/backend/internal/mailer"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

func ptr[T any](v T) *T { return &v }

func validFilterInput() *FilterInput {
	return &FilterInput{Enabled: ptr(true), Mode: ptr("mask"), Categories: []string{"ad", "link", "porn", "gambling", "contact"},
		CustomWords: []string{}, AllowWords: []string{}, AllowDomains: []string{}}
}

func alertInput(port int) *AlertMailInput {
	return &AlertMailInput{Enabled: ptr(true), To: []string{"owner@example.test"}, From: ptr("kelili-test@example.test"),
		SMTPHost: ptr("127.0.0.1"), SMTPPort: ptr(port), SMTPSecurity: ptr("none"), SMTPUsername: ptr("qa"), SMTPPassword: ptr("qa-pass")}
}

type mailRecorder struct {
	msgs []mailer.Message
	cfgs []mailer.Config
	err  error
}

func (m *mailRecorder) send(_ context.Context, c mailer.Config, msg mailer.Message) error {
	if m.err != nil {
		return m.err
	}
	m.cfgs = append(m.cfgs, c)
	m.msgs = append(m.msgs, msg)
	return nil
}

func withMail(h *harness) *mailRecorder {
	rec := &mailRecorder{}
	h.svc.sendMail = rec.send
	return rec
}

// 访客发联系方式：落库/广播/回执都是屏蔽后内容；原文加密进拦截记录；客服消息不过滤。
func TestFilterMasksVisitorChatAndRecordsEvent(t *testing.T) {
	h := newHarness(t)
	if h.svc.filterStore == nil {
		t.Fatal("fakeStore 应实现 FilterStore")
	}
	pa := &fakePeer{visitor: true, id: vid1, conn: "ca"}
	subscribe(t, h, pa)
	id := uuid.NewString()
	chat(h, pa, id, "加我微信 abc123456 看片", "")
	ack := replyFor(t, pa, id)
	if ack.Type != "hall_ack" || strings.Contains(ack.Content, "abc123456") || !strings.Contains(ack.Content, "**") {
		t.Fatalf("回执应带屏蔽后内容：%+v", ack)
	}
	if ex, _ := ack.Extra.(map[string]any); ex == nil || ex["filtered"] != true {
		t.Fatalf("回执应标记 filtered：%+v", ack.Extra)
	}
	b := h.hub.find(func(e *ws.Envelope) bool { return e.Type == ws.HallTypeChat && e.ID == id })
	if b == nil || strings.Contains(b.Content, "abc123456") || strings.Contains(b.Content, "看片") {
		t.Fatalf("广播必须是屏蔽后内容：%+v", b)
	}
	if m := h.store.msgs[id]; m == nil || m.Content != b.Content {
		t.Fatal("落库内容应与广播一致（屏蔽后）")
	}
	if len(h.store.fevents) != 1 {
		t.Fatalf("应有 1 条拦截记录：%d", len(h.store.fevents))
	}
	ev := h.store.fevents[0]
	if strings.Contains(ev.OriginalEnc, "abc123456") || ev.SenderRef != vid1 || ev.Sender != "visitor" {
		t.Fatalf("原文必须加密存储、记录访客编号：%+v", ev)
	}
	if orig, err := h.svc.origCipher.Decrypt(ev.OriginalEnc); err != nil || orig != "加我微信 abc123456 看片" {
		t.Fatalf("原文应可由服务端解密：%q %v", orig, err)
	}
	if !strings.Contains(ev.Categories, "contact") || !strings.Contains(ev.Categories, "porn") {
		t.Fatalf("类别应含联系方式与色情：%s", ev.Categories)
	}
	if ev.MailStatus != store.HallMailDisabled {
		t.Fatalf("未配置邮件时应记为 disabled：%s", ev.MailStatus)
	}
	// 客服不过滤
	pg := &fakePeer{id: "1", conn: "cg"}
	subscribe(t, h, pg)
	id2 := uuid.NewString()
	h.svc.handleFrame(pg, &ws.Envelope{Type: "hall_chat", ID: id2, Content: "官方微信 kefu12345", TS: time.Now().UnixMilli()})
	if r := replyFor(t, pg, id2); r.Type != "hall_ack" || r.Content != "" {
		t.Fatalf("客服消息不过滤：%+v", r)
	}
	if h.store.msgs[id2].Content != "官方微信 kefu12345" || len(h.store.fevents) != 1 {
		t.Fatal("客服消息应原样保存且不产生拦截记录")
	}
	// 正常访客消息不受影响
	h.mr.FastForward(11 * time.Second)
	id3 := uuid.NewString()
	chat(h, pa, id3, "可以用微信支付吗？", "")
	if r := replyFor(t, pa, id3); r.Type != "hall_ack" || r.Content != "" || h.store.msgs[id3].Content != "可以用微信支付吗？" {
		t.Fatalf("正常消息不应被屏蔽：%+v", r)
	}
}

func TestFilterSilencedVisitorNoMail(t *testing.T) {
	h := newHarness(t)
	rec := withMail(h)
	if _, err := h.svc.SaveAlertMail(ctx, agentA(1, true), alertInput(2525)); err != nil {
		t.Fatal(err)
	}
	h.store.blocked[vid1] = true
	pa := &fakePeer{visitor: true, id: vid1, conn: "ca"}
	subscribe(t, h, pa)
	id := uuid.NewString()
	chat(h, pa, id, "百家乐 网赌 稳赚", "")
	replyFor(t, pa, id)
	if len(h.store.fevents) != 1 || h.store.fevents[0].MailStatus != store.HallMailSilenced {
		t.Fatalf("拉黑访客应记 silenced：%+v", h.store.fevents)
	}
	if h.svc.AlertTick(ctx) != 0 || len(rec.msgs) != 0 {
		t.Fatal("silenced 不发邮件")
	}
}

func TestAlertWorkerDigestRetryAndFail(t *testing.T) {
	h := newHarness(t)
	rec := withMail(h)
	if _, err := h.svc.SaveAlertMail(ctx, agentA(1, true), alertInput(2525)); err != nil {
		t.Fatal(err)
	}
	pa := &fakePeer{visitor: true, id: vid1, conn: "ca"}
	subscribe(t, h, pa)
	for _, c := range []string{"加我微信 abc123456", "上 www.spam-site.top 领取"} {
		id := uuid.NewString()
		chat(h, pa, id, c, "")
		replyFor(t, pa, id)
	}
	if n := h.svc.AlertTick(ctx); n != 2 || len(rec.msgs) != 1 {
		t.Fatalf("两条应合并成一封：sent=%d mails=%d", n, len(rec.msgs))
	}
	m := rec.msgs[0]
	if m.To[0] != "owner@example.test" || !strings.Contains(m.Subject, "2 条") || !strings.Contains(m.Text, "abc123456") ||
		!strings.Contains(m.Text, "www.spam-site.top") || !strings.Contains(m.Text, vid1) || !strings.Contains(m.Text, "+08:00") {
		t.Fatalf("邮件内容不完整：%s\n%s", m.Subject, m.Text)
	}
	if rec.cfgs[0].Password != "qa-pass" || rec.cfgs[0].From != "kelili-test@example.test" {
		t.Fatal("应使用后台保存（解密后）的 SMTP 配置")
	}
	for _, e := range h.store.fevents {
		if e.MailStatus != store.HallMailSent {
			t.Fatalf("应标记已发送：%+v", e)
		}
	}
	// 全局每 55 秒最多一封；再来一条，窗口内不发
	h.mr.FastForward(11 * time.Second)
	id := uuid.NewString()
	chat(h, pa, id, "qq 12345678", "")
	replyFor(t, pa, id)
	if h.svc.AlertTick(ctx) != 0 {
		t.Fatal("限速窗口内不应再发")
	}
	// 失败：记重试；第 5 次失败记 failed
	h.mr.FastForward(56 * time.Second)
	rec.err = &mailer.Error{Kind: mailer.KindAuth, Err: errors.New("535 auth failed")}
	h.svc.AlertTick(ctx)
	last := h.store.fevents[len(h.store.fevents)-1]
	if last.MailStatus != store.HallMailPending || last.MailAttempts != 1 || !strings.Contains(last.MailError, "登录失败") {
		t.Fatalf("失败应记重试与中文原因：%+v", last)
	}
	if !fakeNextAt[last.ID].After(time.Now()) {
		t.Fatal("应设置下次重试时间")
	}
	h.store.fevents[len(h.store.fevents)-1].MailAttempts = 4
	fakeNextAt[last.ID] = time.Time{}
	h.mr.FastForward(56 * time.Second)
	h.svc.AlertTick(ctx)
	if s := h.store.fevents[len(h.store.fevents)-1].MailStatus; s != store.HallMailFailed {
		t.Fatalf("第 5 次失败应记 failed：%s", s)
	}
}

func TestFilterSettingsValidationAndApply(t *testing.T) {
	h := newHarness(t)
	admin := agentA(1, true)
	if _, err := h.svc.FilterSettings(ctx, agentA(2, false)); err == nil {
		t.Fatal("普通客服不能读取敏感词设置")
	}
	if _, err := h.svc.SaveFilterSettings(ctx, agentA(2, false), validFilterInput()); err == nil {
		t.Fatal("普通客服不能保存")
	}
	v, err := h.svc.FilterSettings(ctx, admin)
	if err != nil || !v.Enabled || v.Mode != "mask" || len(v.Categories) != 5 || len(v.Builtin["gambling"]) == 0 || v.Limits["custom_words"] != 500 {
		t.Fatalf("默认设置不符合契约：%+v %v", v, err)
	}
	bad := map[string]func(in *FilterInput){
		"缺字段":  func(in *FilterInput) { in.CustomWords = nil },
		"方式非法": func(in *FilterInput) { in.Mode = ptr("delete") },
		"未知类别": func(in *FilterInput) { in.Categories = []string{"ad", "politics"} },
		"词太长":  func(in *FilterInput) { in.CustomWords = []string{strings.Repeat("长", 33)} },
		"只有符号": func(in *FilterInput) { in.CustomWords = []string{"，。！"} },
		"词含换行": func(in *FilterInput) { in.AllowWords = []string{"a\nb"} },
		"域名非法": func(in *FilterInput) { in.AllowDomains = []string{"not a domain"} },
		"自定义词过多": func(in *FilterInput) {
			for i := 0; i < 501; i++ {
				in.CustomWords = append(in.CustomWords, "词"+itoa64(int64(i)))
			}
		},
	}
	for name, mut := range bad {
		in := validFilterInput()
		mut(in)
		if _, err := h.svc.SaveFilterSettings(ctx, admin, in); err == nil {
			t.Errorf("%s 应被拒绝", name)
		} else {
			asErr(t, err, 40062)
		}
	}
	in := validFilterInput()
	in.CustomWords = []string{"竞品A", " 竞 品 a ", ""}
	in.AllowDomains = []string{"https://卖号.com/path", "*.Example.COM"}
	saved, err := h.svc.SaveFilterSettings(ctx, admin, in)
	if err != nil || len(saved.CustomWords) != 1 || saved.AllowDomains[0] != "xn--5kroo.com" || saved.AllowDomains[1] != "example.com" {
		t.Fatalf("保存后应去重并规范化域名：%+v %v", saved, err)
	}
	// 立即生效（缓存失效）
	if r := h.svc.textFilter(ctx).Check("推荐竞品A"); !r.Filtered {
		t.Fatal("自定义词保存后应立即生效")
	}
	if r := h.svc.textFilter(ctx).Check("官网 https://www.xn--5kroo.com/a"); r.Filtered {
		t.Fatal("放行域名不应屏蔽")
	}
	// 预览配置检测
	pv := validFilterInput()
	pv.Mode = ptr("whole")
	res, err := h.svc.TestFilter(ctx, admin, "加我微信 abc123456", pv)
	if err != nil || !res.Filtered || res.Masked != "**" || res.Categories[0] != "contact" {
		t.Fatalf("预览检测结果不符：%+v %v", res, err)
	}
	if _, err := h.svc.TestFilter(ctx, admin, "", nil); err == nil {
		t.Fatal("空内容应拒绝")
	}
}

func TestAlertMailSettingsSecrecy(t *testing.T) {
	h := newHarness(t)
	admin := agentA(1, true)
	if _, err := h.svc.AlertMail(ctx, agentA(2, false)); err == nil {
		t.Fatal("普通客服不能读取邮件设置")
	}
	v, err := h.svc.SaveAlertMail(ctx, admin, alertInput(2525))
	if err != nil || !v.PasswordSet || !v.Configured || v.SMTPSecurity != "none" {
		t.Fatalf("保存失败：%+v %v", v, err)
	}
	enc := h.store.settings[KeyAlertSMTPPasswordEnc]
	if enc == "" || strings.Contains(enc, "qa-pass") {
		t.Fatal("密码必须加密存储")
	}
	// 再次保存不带密码：保留原密码
	in := alertInput(2526)
	in.SMTPPassword = nil
	if v, err = h.svc.SaveAlertMail(ctx, admin, in); err != nil || !v.PasswordSet || v.SMTPPort != 2526 {
		t.Fatalf("不传密码应保留：%+v %v", v, err)
	}
	if h.store.settings[KeyAlertSMTPPasswordEnc] != enc {
		t.Fatal("未修改密码时密文不应变化")
	}
	bad := map[string]func(in *AlertMailInput){
		"明文传到外网":  func(in *AlertMailInput) { in.SMTPHost = ptr("smtp.163.com") },
		"收件人非法":   func(in *AlertMailInput) { in.To = []string{"a@b.c\r\nBcc: x@y.z"} },
		"收件人太多":   func(in *AlertMailInput) { in.To = []string{"1@a.cn", "2@a.cn", "3@a.cn", "4@a.cn", "5@a.cn", "6@a.cn"} },
		"开启但无收件人": func(in *AlertMailInput) { in.To = []string{} },
		"发件人非法":   func(in *AlertMailInput) { in.From = ptr("kelili") },
		"端口非法":    func(in *AlertMailInput) { in.SMTPPort = ptr(70000) },
		"加密方式非法":  func(in *AlertMailInput) { in.SMTPSecurity = ptr("tls") },
		"主机非法":    func(in *AlertMailInput) { in.SMTPHost = ptr("smtp.163.com:465/x") },
		"密码含换行":   func(in *AlertMailInput) { in.SMTPPassword = ptr("a\r\nb") },
		"缺字段":     func(in *AlertMailInput) { in.From = nil },
	}
	for name, mut := range bad {
		in := alertInput(2525)
		mut(in)
		if _, err := h.svc.SaveAlertMail(ctx, admin, in); err == nil {
			t.Errorf("%s 应被拒绝", name)
		} else {
			asErr(t, err, 40063)
		}
	}
	// 测试邮件：成功 / 失败中文原因
	rec := withMail(h)
	if to, err := h.svc.TestAlertMail(ctx, admin); err != nil || to[0] != "owner@example.test" || len(rec.msgs) != 1 {
		t.Fatalf("测试邮件应发送：%v %v", to, err)
	}
	rec.err = &mailer.Error{Kind: mailer.KindConnect, Err: errors.New("dial")}
	_, err = h.svc.TestAlertMail(ctx, admin)
	if he := asErr(t, err, 50271); !strings.Contains(he.Msg, "无法连接") {
		t.Fatalf("失败原因应为中文：%s", he.Msg)
	}
}

func TestAlertDefaultsFromEnvAndAllowDomain(t *testing.T) {
	h := newHarness(t)
	h.svc.alertDefaults = AlertDefaults{Host: "smtp.163.com", Port: 465, Username: "kelili@example.test", Password: "env-secret", From: "kelili@example.test", To: []string{"owner@example.test"}}
	h.svc.publicDomain = "k7m2x9p3.xn--5kroo.com"
	h.svc.invalidateFilterCaches()
	v, err := h.svc.AlertMail(ctx, agentA(1, true))
	if err != nil || v.SMTPHost != "smtp.163.com" || v.SMTPSecurity != "ssl" || !v.PasswordSet || !v.Configured || v.From != "kelili@example.test" {
		t.Fatalf("应使用环境变量默认值：%+v %v", v, err)
	}
	f, _ := h.svc.FilterSettings(ctx, agentA(1, true))
	if len(f.AllowDomains) != 1 || f.AllowDomains[0] != "xn--5kroo.com" {
		t.Fatalf("默认放行本站主域：%v", f.AllowDomains)
	}
	for in, want := range map[string]string{"cs.example.com.cn": "example.com.cn", "127.0.0.1:18080": "", "a.b.example.org": "example.org"} {
		got := defaultAllowDomains(in)
		if (want == "" && len(got) != 0) || (want != "" && (len(got) != 1 || got[0] != want)) {
			t.Errorf("defaultAllowDomains(%q)=%v want %q", in, got, want)
		}
	}
}

func TestFilterEventsAdminView(t *testing.T) {
	h := newHarness(t)
	pa := &fakePeer{visitor: true, id: vid1, conn: "ca"}
	subscribe(t, h, pa)
	id := uuid.NewString()
	chat(h, pa, id, "tg @spam_bot99 进群", "")
	replyFor(t, pa, id)
	if _, err := h.svc.FilterEvents(ctx, agentA(2, false), 0, 20); err == nil {
		t.Fatal("普通客服不能查看拦截记录")
	}
	list, err := h.svc.FilterEvents(ctx, agentA(1, true), 0, 20)
	if err != nil || len(list) != 1 || list[0].Original != "tg @spam_bot99 进群" || list[0].VID != vid1 || !strings.Contains(list[0].CreatedAt, "+08:00") {
		t.Fatalf("拦截记录不符：%+v %v", list, err)
	}
	if _, err := h.svc.FilterEvents(ctx, agentA(1, true), 0, 51); err == nil {
		t.Fatal("limit 超过 50 应拒绝")
	}
}
