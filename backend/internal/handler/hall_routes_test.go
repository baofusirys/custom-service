package handler

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/config"
	"github.com/custom-service/backend/internal/hall"
	"github.com/custom-service/backend/internal/livekit"
	"github.com/custom-service/backend/internal/security"
	"github.com/custom-service/backend/internal/service"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

const (
	rtSecret = "route-test-jwt-0123456789abcdefghijklmnop"
	rtLKKey  = "APIrouteKey"
	rtLKSec  = "route-livekit-0123456789abcdefghijklmnopq"
	rtVID    = "11111111-2222-4333-8444-555555555555"
)

type routeEnv struct {
	r       *gin.Engine
	h       *HTTP
	db      *fakeDB
	hallSvc *hall.Service
	mr      *miniredis.Miniredis
	agentV  map[int64]int64 // agent id -> token_version
}

// newRouteEnv 用真实路由表（RegisterRoutes）+ 假数据库 + miniredis 组装，LiveKit 指向不可达地址（这些用例不需要媒体服务）。
func newRouteEnv(t *testing.T) *routeEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	env := &routeEnv{agentV: map[int64]int64{1: 0, 2: 0}}
	settings := map[string]string{"hall_enabled": "true", "hall_video_enabled": "false", "hall_max_speakers": "6", "hall_max_cameras": "2", "hall_max_participants": "100"}
	db, f := openFakeDB(t, func(q string, args []driver.Value) fakeRes {
		switch {
		case strings.Contains(q, "FROM agents WHERE id=?"):
			id, _ := args[0].(int64)
			ver, ok := env.agentV[id]
			if !ok {
				return fakeRes{cols: make([]string, 8)}
			}
			role := "agent"
			if id == 1 {
				role = "admin"
			}
			return fakeRes{cols: make([]string, 8), rows: [][]driver.Value{{id, fmt.Sprintf("user%d", id), role, "昵称", true, ver, time.Now(), nil}}}
		case strings.Contains(q, "FROM settings WHERE key_name IN"):
			var rows [][]driver.Value
			for _, a := range args {
				if v, ok := settings[a.(string)]; ok {
					rows = append(rows, []driver.Value{a, v})
				}
			}
			return fakeRes{cols: []string{"k", "v"}, rows: rows}
		case strings.HasPrefix(strings.TrimSpace(q), "INSERT"), strings.HasPrefix(strings.TrimSpace(q), "UPDATE"):
			return fakeRes{affected: 1}
		}
		return fakeRes{cols: []string{"c"}}
	})
	env.db = f
	st := store.New(db)
	mr := miniredis.RunT(t)
	env.mr = mr
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	log := zap.NewNop()
	cipher, _ := security.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	limiter := security.NewRateLimiter(rdb, log)
	svc := service.New(st, cipher, log, log, log, limiter, 20)
	hub := ws.NewHub(ws.HubConfig{NodeID: "n1", BizLog: log, RawLog: log, SecLog: log, Cipher: cipher, Redis: rdb, Sink: svc})
	svc.SetHub(hub)
	lk := livekit.NewClient(livekit.Config{APIKey: rtLKKey, APISecret: rtLKSec, InternalURL: "http://127.0.0.1:1", Timeout: 200 * time.Millisecond}, log)
	hs, err := hall.New(hall.Deps{Room: store.HallDefaultRoom, Store: st, Redis: rdb, Media: lk, Hub: hall.NewHubBroadcaster(hub),
		Limiter: limiter, DataKey: []byte("0123456789abcdef0123456789abcdef"), PublicURL: "wss://cs.example.com/lk"})
	if err != nil {
		t.Fatal(err)
	}
	env.hallSvc = hs
	cfg := &config.Config{JWTSecret: []byte(rtSecret), Timezone: time.FixedZone("CST", 8*3600), MaxUploadMB: 1, CORSAllowedOrigins: []string{"https://cs.example.com"}}
	env.h = NewHTTP(cfg, svc, hub, t.TempDir())
	env.h.SetHall(hs)
	env.r = gin.New()
	RegisterRoutes(env.r, env.h, st)
	return env
}

func (e *routeEnv) do(method, path, token string, body any, hdr map[string]string) (int, map[string]any) {
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func code(m map[string]any) int {
	if v, ok := m["code"].(float64); ok {
		return int(v)
	}
	return -1
}

func visitorTok(t *testing.T, secret string) string {
	tok, err := security.IssueVisitorToken([]byte(secret), rtVID, "default", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func agentTok(t *testing.T, id int64, role string, ver int64) string {
	tok, err := security.IssueAgentTokenWithVersion([]byte(rtSecret), id, fmt.Sprintf("user%d", id), role, ver, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// 越权与绕过：访客接口只认访客令牌；客服接口拒绝访客令牌；管理设置拒绝普通客服。
func TestHallRoutesAuthorization(t *testing.T) {
	env := newRouteEnv(t)
	vt := visitorTok(t, rtSecret)
	at := agentTok(t, 2, "agent", 0)
	for _, p := range []string{"/api/visitor/hall/join", "/api/visitor/hall/mic", "/api/visitor/hall/camera"} {
		if st, m := env.do("POST", p, "", map[string]bool{"on": true}, nil); st != 401 || code(m) != 40110 {
			t.Fatalf("%s 无令牌应 401/40110：%d %v", p, st, m)
		}
		if st, m := env.do("POST", p, at, map[string]bool{"on": true}, nil); st != 401 || code(m) != 40110 {
			t.Fatalf("%s 客服令牌冒充访客应 401/40110：%d %v", p, st, m)
		}
		if st, _ := env.do("POST", p, visitorTok(t, rtSecret+"-forged"), map[string]bool{"on": true}, nil); st != 401 {
			t.Fatalf("%s 伪造访客令牌应 401", p)
		}
	}
	// vid 只能来自令牌：URL 里塞 vid 无效
	if st, _ := env.do("GET", "/api/visitor/hall/messages?vid="+rtVID, "", nil, nil); st != 401 {
		t.Fatal("URL 携带 vid 不得绕过鉴权")
	}
	for _, p := range []string{"/api/agent/hall/state", "/api/agent/hall/messages"} {
		if st, _ := env.do("GET", p, vt, nil, nil); st != 401 {
			t.Fatalf("%s 访客令牌应 401", p)
		}
	}
	if st, _ := env.do("POST", "/api/agent/hall/moderate", vt, map[string]any{"action": "mute_all"}, nil); st != 401 {
		t.Fatal("访客调用客服管理接口应 401")
	}
	if st, m := env.do("POST", "/api/admin/settings", at, map[string]string{"hall_enabled": "false"}, nil); st != 403 || code(m) != 40301 {
		t.Fatalf("普通客服改设置应 403：%d %v", st, m)
	}
	// 令牌版本吊销后（改密/退出）立即失效
	env.agentV[2] = 5
	if st, m := env.do("GET", "/api/agent/hall/state", at, nil, nil); st != 401 || code(m) != 40108 {
		t.Fatalf("吊销后的客服令牌应 401/40108：%d %v", st, m)
	}
	env.agentV[2] = 0
	// 普通客服不能移出其他客服；不能对自己操作
	if st, m := env.do("POST", "/api/agent/hall/moderate", at, map[string]any{"action": "kick", "identity": "a_1", "minutes": 5}, nil); st != 403 || code(m) != 40376 {
		t.Fatalf("普通客服管理客服应 403/40376：%d %v", st, m)
	}
	if st, m := env.do("POST", "/api/agent/hall/moderate", at, map[string]any{"action": "kick", "identity": "a_2"}, nil); st != 403 || code(m) != 40376 {
		t.Fatalf("对自己操作应 403/40376：%d %v", st, m)
	}
	if len(env.db.execsMatching("INSERT INTO audit_logs")) < 2 {
		t.Fatal("被拒绝的管理动作也必须写审计")
	}
	if st, m := env.do("POST", "/api/agent/hall/moderate", at, map[string]any{"action": "kick", "identity": "1' OR '1'='1"}, nil); st != 400 || code(m) != 40060 {
		t.Fatalf("注入式 identity 应 400：%d %v", st, m)
	}
}

func TestHallVisitorJoinViaRoute(t *testing.T) {
	env := newRouteEnv(t)
	st, m := env.do("POST", "/api/visitor/hall/join", visitorTok(t, rtSecret), nil, nil)
	if st != 200 || code(m) != 0 {
		t.Fatalf("访客加入应成功：%d %v", st, m)
	}
	data := m["data"].(map[string]any)
	raw, _ := json.Marshal(data)
	if strings.Contains(string(raw), rtVID) {
		t.Fatal("加入响应不得包含 vid")
	}
	for _, k := range []string{"url", "token", "room", "identity", "name", "video_enabled", "max_speakers", "max_cameras", "ice_servers", "history"} {
		if _, ok := data[k]; !ok {
			t.Fatalf("缺字段 %s：%v", k, data)
		}
	}
	if !hall.ValidIdentity(data["identity"].(string)) || data["room"] != "hall_default" {
		t.Fatalf("identity/room 错误：%v", data)
	}
	// 访客公开设置：布尔
	st, m = env.do("GET", "/api/visitor/settings", "", nil, nil)
	d := m["data"].(map[string]any)
	if st != 200 || d["hall_enabled"] != true || d["hall_video_enabled"] != false {
		t.Fatalf("访客公开设置错误：%v", d)
	}
	// 开麦请求体校验
	if st, m := env.do("POST", "/api/visitor/hall/mic", visitorTok(t, rtSecret), map[string]string{"on": "yes"}, nil); st != 400 || code(m) != 40060 {
		t.Fatalf("非法请求体应 400/40060：%d %v", st, m)
	}
	// LiveKit 不可达：明确中文错误，而不是 500 堆栈
	st, m = env.do("POST", "/api/visitor/hall/mic", visitorTok(t, rtSecret), map[string]bool{"on": true}, nil)
	if st != 503 || code(m) != 50370 || !strings.Contains(m["msg"].(string), "语音服务") {
		t.Fatalf("LiveKit 不可用应 503/50370：%d %v", st, m)
	}
}

func TestHallSettingsValidationViaAdminRoute(t *testing.T) {
	env := newRouteEnv(t)
	admin := agentTok(t, 1, "admin", 0)
	for _, body := range []map[string]string{
		{"hall_max_speakers": "21"}, {"hall_max_speakers": "0"}, {"hall_max_cameras": "7"},
		{"hall_max_participants": "9"}, {"hall_enabled": "yes"}, {"hall_video_enabled": "1"},
		{"greeting_text": "合法", "hall_max_participants": "501"},
	} {
		st, m := env.do("POST", "/api/admin/settings", admin, body, nil)
		if st != 400 || code(m) != 40059 {
			t.Fatalf("%v 应 400/40059：%d %v", body, st, m)
		}
	}
	if n := len(env.db.execsMatching("INSERT INTO settings")); n != 0 {
		t.Fatalf("任一大厅键不合法时不得部分保存：%d", n)
	}
	st, m := env.do("POST", "/api/admin/settings", admin, map[string]string{"hall_max_speakers": " 8 ", "hall_video_enabled": "true"}, nil)
	if st != 200 || code(m) != 0 {
		t.Fatalf("合法设置应保存：%d %v", st, m)
	}
	saved := env.db.execsMatching("INSERT INTO settings")
	found := false
	for _, e := range saved {
		if e.args[0] == "hall_max_speakers" && e.args[1] == "8" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应保存规范化值 8：%v", saved)
	}
	if len(env.db.execsMatching("INSERT INTO audit_logs")) == 0 {
		t.Fatal("修改大厅设置应写审计")
	}
}

func TestLiveKitWebhookRoute(t *testing.T) {
	env := newRouteEnv(t)
	body := []byte(`{"event":"participant_joined","id":"EV_route1","room":{"name":"hall_default"},"participant":{"identity":"a_1"}}`)
	good, _ := livekit.SignWebhook(rtLKKey, rtLKSec, body, time.Now(), 5*time.Minute)
	post := func(auth string, b []byte, hdr map[string]string) int {
		req := httptest.NewRequest("POST", "/api/livekit/webhook", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/webhook+json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		env.r.ServeHTTP(w, req)
		return w.Code
	}
	if c := post(good, body, map[string]string{"X-Forwarded-For": "203.0.113.9"}); c != 404 {
		t.Fatalf("经反代转发的回调应 404：%d", c)
	}
	if c := post(good, body, map[string]string{"X-Real-IP": "172.18.0.2"}); c != 404 {
		t.Fatalf("带 X-Real-IP 的回调应 404：%d", c)
	}
	if c := post("", body, nil); c != 401 {
		t.Fatalf("缺签名应 401：%d", c)
	}
	forged, _ := livekit.SignWebhook(rtLKKey, rtLKSec+"x", body, time.Now(), 5*time.Minute)
	if c := post(forged, body, nil); c != 401 {
		t.Fatalf("伪造签名应 401：%d", c)
	}
	if c := post(good, append([]byte(nil), bytes.Replace(body, []byte("a_1"), []byte("a_2"), 1)...), nil); c != 401 {
		t.Fatalf("篡改请求体应 401：%d", c)
	}
	expired, _ := livekit.SignWebhook(rtLKKey, rtLKSec, body, time.Now().Add(-10*time.Minute), 5*time.Minute)
	if c := post(expired, body, nil); c != 401 {
		t.Fatalf("过期签名应 401：%d", c)
	}
	if c := post(good, body, nil); c != 200 {
		t.Fatalf("合法回调应 200：%d", c)
	}
	if c := post(good, body, nil); c != 200 {
		t.Fatalf("重放应幂等返回 200：%d", c)
	}
	big := bytes.Repeat([]byte("a"), (1<<20)+10)
	if c := post(good, big, nil); c != 413 {
		t.Fatalf("超大请求体应 413：%d", c)
	}
	env.hallSvc.Wait()
}

func multipartUpload(t *testing.T, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

// 修复后：网页后台只带 cs_session Cookie 也能上传；被吊销/过期令牌按中间件同口径拒绝；scope=hall 不需要 conv_id。
func TestUploadAgentCookieAuthAfterFix(t *testing.T) {
	env := newRouteEnv(t)
	call := func(cookie, scope string) (string, string, error) {
		body, ctype := multipartUpload(t, map[string]string{"uploader": "agent", "scope": scope})
		req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
		req.Header.Set("Content-Type", ctype)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "cs_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req
		return env.h.authorizeUpload(c, "agent", scope == "hall")
	}
	ref, conv, err := call(agentTok(t, 2, "agent", 0), "hall")
	if err != nil || ref != "2" || conv != store.HallDefaultRoom {
		t.Fatalf("Cookie 鉴权的大厅上传应通过：%q %q %v", ref, conv, err)
	}
	env.agentV[2] = 3
	if _, _, err := call(agentTok(t, 2, "agent", 0), "hall"); err == nil || !strings.Contains(err.Error(), "会话已失效") {
		t.Fatalf("吊销的令牌应拒绝：%v", err)
	}
	env.agentV[2] = 0
	expired, _ := security.IssueAgentTokenWithVersion([]byte(rtSecret), 2, "user2", "agent", 0, time.Second)
	time.Sleep(1100 * time.Millisecond)
	_, _, err = call(expired, "")
	ue, ok := err.(*uploadAuthError)
	if !ok || ue.code != 40102 {
		t.Fatalf("过期令牌应返回 40102 以便前端续期：%v", err)
	}
	if _, _, err := call("", "hall"); err == nil || err.Error() != "缺少 token" {
		t.Fatalf("无令牌应拒绝：%v", err)
	}
	// 访客令牌不认 Cookie
	body, ctype := multipartUpload(t, map[string]string{"uploader": "visitor", "scope": "hall"})
	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ctype)
	req.AddCookie(&http.Cookie{Name: "cs_session", Value: visitorTok(t, rtSecret)})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	if _, _, err := env.h.authorizeUpload(c, "visitor", true); err == nil {
		t.Fatal("访客上传不得从 Cookie 取令牌")
	}
	// 被移出的访客不能上传大厅附件
	_ = env.mr.Set("hall:hall_default:ban:"+env.hallSvc.VisitorIdentity(rtVID), "1")
	env.mr.SetTTL("hall:hall_default:ban:"+env.hallSvc.VisitorIdentity(rtVID), time.Minute)
	body, ctype = multipartUpload(t, map[string]string{"uploader": "visitor", "scope": "hall"})
	req = httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+visitorTok(t, rtSecret))
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = req
	_, _, err = env.h.authorizeUpload(c, "visitor", true)
	if ue, ok := err.(*uploadAuthError); !ok || ue.code != 40372 {
		t.Fatalf("被移出访客上传大厅附件应 40372：%v", err)
	}
	// 非法 scope
	body, ctype = multipartUpload(t, map[string]string{"uploader": "visitor", "scope": "admin"})
	req = httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ctype)
	w = httptest.NewRecorder()
	env.r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("非法 scope 应 400：%d", w.Code)
	}
}

// [122] 敏感词与告警邮件管理接口：只允许管理员；访客令牌 401、普通客服 403；GET 不返回任何密码字段；SQL 注入式参数被拒。
func TestHallFilterRoutesAuthorization(t *testing.T) {
	env := newRouteEnv(t)
	vt := visitorTok(t, rtSecret)
	at := agentTok(t, 2, "agent", 0)
	admin := agentTok(t, 1, "admin", 0)
	paths := []struct{ m, p string }{
		{"GET", "/api/admin/hall/filter"}, {"POST", "/api/admin/hall/filter"}, {"POST", "/api/admin/hall/filter/test"},
		{"GET", "/api/admin/hall/filter/events"}, {"GET", "/api/admin/hall/alert-mail"}, {"POST", "/api/admin/hall/alert-mail"},
		{"POST", "/api/admin/hall/alert-mail/test"},
	}
	for _, p := range paths {
		if st, _ := env.do(p.m, p.p, "", map[string]any{}, nil); st != 401 {
			t.Fatalf("%s %s 无令牌应 401：%d", p.m, p.p, st)
		}
		if st, _ := env.do(p.m, p.p, vt, map[string]any{}, nil); st != 401 {
			t.Fatalf("%s %s 访客令牌应 401：%d", p.m, p.p, st)
		}
		if st, m := env.do(p.m, p.p, at, map[string]any{}, nil); st != 403 || code(m) != 40301 {
			t.Fatalf("%s %s 普通客服应 403：%d %v", p.m, p.p, st, m)
		}
	}
	st, m := env.do("GET", "/api/admin/hall/filter", admin, nil, nil)
	if st != 200 || code(m) != 0 {
		t.Fatalf("管理员读取敏感词设置应成功：%d %v", st, m)
	}
	st, m = env.do("GET", "/api/admin/hall/alert-mail", admin, nil, nil)
	if st != 200 {
		t.Fatalf("管理员读取邮件设置应成功：%d %v", st, m)
	}
	data, _ := m["data"].(map[string]any)
	for k := range data {
		if strings.Contains(strings.ToLower(k), "password") && k != "password_set" {
			t.Fatalf("邮件设置不得返回密码字段：%s", k)
		}
	}
	if st, m := env.do("POST", "/api/admin/hall/filter", admin, map[string]any{"enabled": true}, nil); st != 400 || code(m) != 40062 {
		t.Fatalf("缺字段应 400/40062：%d %v", st, m)
	}
	if st, m := env.do("GET", "/api/admin/hall/filter/events?before=1%20OR%201=1", admin, nil, nil); st != 400 || code(m) != 40060 {
		t.Fatalf("非法游标应 400：%d %v", st, m)
	}
	if st, m := env.do("POST", "/api/admin/hall/alert-mail", admin, map[string]any{"enabled": true, "to": []string{"a@b.cn"}, "from": "k@b.cn",
		"smtp_host": "smtp.163.com", "smtp_port": 25, "smtp_security": "none", "smtp_username": "u"}, nil); st != 400 || code(m) != 40063 {
		t.Fatalf("向外网明文发信应 400/40063：%d %v", st, m)
	}
	// 通用设置接口不能写入敏感词/邮件键（白名单外，静默忽略后无可更新字段）
	if st, m := env.do("POST", "/api/admin/settings", admin, map[string]string{"hall_alert_smtp_password_enc": "x", "hall_filter_enabled": "false"}, nil); st != 400 || code(m) != 40031 {
		t.Fatalf("通用设置接口不得写入敏感词/邮件键：%d %v", st, m)
	}
}
