package livekit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testKey    = "APIhallTestKey"
	testSecret = "0123456789abcdefghijklmnopqrstuvwxyzABCD"
)

func decodePayload(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("不是 JWT：%d 段", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// 访客令牌：可订阅、不可发布、不可发 data、不可改 metadata、无 roomAdmin；声明字段对照 livekit/protocol grants.go。
func TestVisitorTokenClaimsAndPermissions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok, exp, err := IssueParticipantToken(testKey, testSecret, ParticipantToken{
		Room: "hall_default", Identity: "v_0123456789abcdef0123", Name: "访客 0042",
		Metadata: `{"role":"visitor","code":"0042"}`, TTL: 6 * time.Hour,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(6 * time.Hour)) {
		t.Fatalf("exp 错误：%v", exp)
	}
	p := decodePayload(t, tok)
	if p["iss"] != testKey || p["sub"] != "v_0123456789abcdef0123" || p["name"] != "访客 0042" {
		t.Fatalf("iss/sub/name 错误：%v", p)
	}
	if p["metadata"] != `{"role":"visitor","code":"0042"}` {
		t.Fatalf("metadata 错误：%v", p["metadata"])
	}
	if int64(p["exp"].(float64))-int64(p["nbf"].(float64)) != int64((6 * time.Hour).Seconds()) {
		t.Fatalf("TTL 不是 6 小时：%v", p)
	}
	v := p["video"].(map[string]any)
	if v["room"] != "hall_default" || v["roomJoin"] != true || v["canSubscribe"] != true {
		t.Fatalf("video 授权错误：%v", v)
	}
	if v["canPublish"] != false || v["canPublishData"] != false || v["canUpdateOwnMetadata"] != false {
		t.Fatalf("访客必须显式禁止发布/发 data/改 metadata：%v", v)
	}
	for _, forbidden := range []string{"roomAdmin", "roomCreate", "roomList", "canPublishSources", "hidden"} {
		if _, ok := v[forbidden]; ok {
			t.Fatalf("访客令牌不应含 %s：%v", forbidden, v)
		}
	}
	// 签名必须能用同一密钥验证，且换密钥失败
	if _, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return []byte(testSecret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(func() time.Time { return now })); err != nil {
		t.Fatalf("验签失败：%v", err)
	}
	if _, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return []byte(testSecret + "x"), nil }, jwt.WithTimeFunc(func() time.Time { return now })); err == nil {
		t.Fatal("错误密钥不应验签通过")
	}
}

func TestAgentTokenMicrophoneOnly(t *testing.T) {
	tok, _, err := IssueParticipantToken(testKey, testSecret, ParticipantToken{
		Room: "hall_default", Identity: "a_7", Name: "客服·小王", TTL: time.Hour, PublishSources: []string{GrantSourceMicrophone},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v := decodePayload(t, tok)["video"].(map[string]any)
	srcs, _ := v["canPublishSources"].([]any)
	if v["canPublish"] != true || len(srcs) != 1 || srcs[0] != "microphone" {
		t.Fatalf("客服只应能发布麦克风：%v", v)
	}
	if _, ok := v["roomAdmin"]; ok {
		t.Fatal("客服客户端令牌不应含 roomAdmin")
	}
}

func TestIssueTokenRejectsMissingInput(t *testing.T) {
	if _, _, err := IssueParticipantToken("", testSecret, ParticipantToken{Room: "r", Identity: "i", TTL: time.Minute}, time.Now()); !errors.Is(err, ErrKeysMissing) {
		t.Fatalf("缺 key 应报错：%v", err)
	}
	if _, _, err := IssueParticipantToken(testKey, testSecret, ParticipantToken{Room: "r", TTL: time.Minute}, time.Now()); err == nil {
		t.Fatal("缺 identity 应报错")
	}
}

func TestAdminTokenScopedToRoom(t *testing.T) {
	tok, err := issueAdminToken(testKey, testSecret, "hall_default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := decodePayload(t, tok)
	v := p["video"].(map[string]any)
	if v["roomAdmin"] != true || v["room"] != "hall_default" || len(v) != 2 {
		t.Fatalf("管理令牌只应含 roomAdmin+room：%v", v)
	}
	if int64(p["exp"].(float64))-int64(p["iat"].(float64)) > 120 {
		t.Fatal("管理令牌有效期过长")
	}
}

// ============ Webhook 验签 ============

func TestVerifyWebhook(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"event":"participant_left","id":"EV_abc","room":{"name":"hall_default"},"participant":{"identity":"v_0123456789abcdef0123","joinedAt":"1799999000"},"createdAt":"1800000000"}`)
	good, err := SignWebhook(testKey, testSecret, body, now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := VerifyWebhook(testKey, testSecret, good, body, now)
	if err != nil {
		t.Fatalf("正确签名应通过：%v", err)
	}
	if ev.Event != "participant_left" || ev.ID != "EV_abc" || ev.RoomName != "hall_default" || ev.Participant.Identity != "v_0123456789abcdef0123" || ev.Participant.JoinedAt != 1799999000 || ev.CreatedAt != 1800000000 {
		t.Fatalf("事件解析错误：%+v", ev)
	}
	if _, err := VerifyWebhook(testKey, testSecret, "Bearer "+good, body, now); err != nil {
		t.Fatalf("应兼容 Bearer 前缀：%v", err)
	}
	// 篡改请求体
	tampered := []byte(strings.Replace(string(body), "participant_left", "participant_joined", 1))
	if _, err := VerifyWebhook(testKey, testSecret, good, tampered, now); !errors.Is(err, ErrWebhookChecksum) {
		t.Fatalf("篡改请求体应失败（checksum）：%v", err)
	}
	// 过期（超过 60 秒宽限）
	if _, err := VerifyWebhook(testKey, testSecret, good, body, now.Add(5*time.Minute+2*time.Minute)); !errors.Is(err, ErrWebhookToken) {
		t.Fatalf("过期令牌应失败：%v", err)
	}
	// 错误密钥签名
	forged, _ := SignWebhook(testKey, testSecret+"-wrong", body, now, 5*time.Minute)
	if _, err := VerifyWebhook(testKey, testSecret, forged, body, now); !errors.Is(err, ErrWebhookToken) {
		t.Fatalf("错误密钥应失败：%v", err)
	}
	// 错误 key（iss 不符）
	otherIss, _ := SignWebhook("APIotherKey", testSecret, body, now, 5*time.Minute)
	if _, err := VerifyWebhook(testKey, testSecret, otherIss, body, now); !errors.Is(err, ErrWebhookToken) {
		t.Fatalf("iss 不符应失败：%v", err)
	}
	// 缺 Authorization
	if _, err := VerifyWebhook(testKey, testSecret, "", body, now); !errors.Is(err, ErrWebhookNoAuth) {
		t.Fatalf("缺头应失败：%v", err)
	}
	// 无 exp 的令牌（手搓永久令牌）必须拒绝
	noExp := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: testKey}, Sha256: claimsSha(body)})
	noExpTok, _ := noExp.SignedString([]byte(testSecret))
	if _, err := VerifyWebhook(testKey, testSecret, noExpTok, body, now); !errors.Is(err, ErrWebhookToken) {
		t.Fatalf("无 exp 应失败：%v", err)
	}
	// alg=none 必须拒绝
	none := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: testKey, ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}, Sha256: claimsSha(body)})
	noneTok, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := VerifyWebhook(testKey, testSecret, noneTok, body, now); !errors.Is(err, ErrWebhookToken) {
		t.Fatalf("alg=none 应失败：%v", err)
	}
	// 缺 sha256 声明
	noSha := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: testKey, ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}})
	noShaTok, _ := noSha.SignedString([]byte(testSecret))
	if _, err := VerifyWebhook(testKey, testSecret, noShaTok, body, now); !errors.Is(err, ErrWebhookChecksum) {
		t.Fatalf("缺 sha256 应失败：%v", err)
	}
}

func claimsSha(body []byte) string {
	tok, _ := SignWebhook(testKey, testSecret, body, time.Now(), time.Minute)
	p := strings.Split(tok, ".")[1]
	raw, _ := base64.RawURLEncoding.DecodeString(p)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m["sha256"].(string)
}

func TestParseWebhookSnakeCaseAndTrack(t *testing.T) {
	ev, err := ParseWebhookEvent([]byte(`{"event":"track_published","id":"EV_1","created_at":1800000000,"room":{"name":"hall_default"},"participant":{"identity":"a_3","joined_at":5},"track":{"sid":"TR_1","type":1,"source":1,"muted":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Track == nil || ev.Track.Source != SourceCamera || ev.Track.Type != "VIDEO" || ev.Participant.JoinedAt != 5 || ev.CreatedAt != 1800000000 {
		t.Fatalf("snake_case/数字枚举解析错误：%+v %+v", ev, ev.Track)
	}
	if _, err := ParseWebhookEvent([]byte(`{"id":"x"}`)); !errors.Is(err, ErrWebhookPayload) {
		t.Fatal("缺 event 应失败")
	}
}

// ============ Twirp 客户端（httptest 假服务） ============

type fakeTwirp struct {
	mu       sync.Mutex
	calls    []string
	bodies   map[string]map[string]any
	auth     []string
	handler  func(method string, body map[string]any) (int, string)
	inflight atomic.Int32
	maxSeen  atomic.Int32
	delay    time.Duration
}

func (f *fakeTwirp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cur := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		old := f.maxSeen.Load()
		if cur <= old || f.maxSeen.CompareAndSwap(old, cur) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	method := strings.TrimPrefix(r.URL.Path, "/twirp/livekit.RoomService/")
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, method)
	if f.bodies == nil {
		f.bodies = map[string]map[string]any{}
	}
	f.bodies[method] = body
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	status, resp := http.StatusOK, "{}"
	if f.handler != nil {
		status, resp = f.handler(method, body)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(resp))
}

func newTestClient(t *testing.T, f *fakeTwirp, timeout time.Duration, conc int) *Client {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return NewClient(Config{APIKey: testKey, APISecret: testSecret, InternalURL: srv.URL + "/", Timeout: timeout, Concurrency: conc}, nil)
}

func TestClientListParticipantsDecodesBothNamings(t *testing.T) {
	f := &fakeTwirp{handler: func(method string, _ map[string]any) (int, string) {
		return 200, `{"participants":[{"sid":"PA_1","identity":"v_0123456789abcdef0123","state":"ACTIVE","joinedAt":"1800000000","tracks":[{"sid":"TR_a","type":"AUDIO","source":"MICROPHONE"},{"sid":"TR_v","type":"VIDEO","source":"CAMERA","muted":true}],"permission":{"canSubscribe":true,"canPublish":true,"canPublishSources":["MICROPHONE"]}},{"identity":"a_1","joined_at":12,"permission":{"can_publish":true,"can_publish_sources":[2,1]}}]}`
	}}
	c := newTestClient(t, f, time.Second, 2)
	parts, err := c.ListParticipants(context.Background(), "hall_default")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("参与者数量错误：%d", len(parts))
	}
	v := parts[0]
	if !v.Publishing(SourceMicrophone) || v.Publishing(SourceCamera) || !v.HasTrack(SourceCamera) || v.JoinedAt != 1800000000 {
		t.Fatalf("轨道/时间解析错误：%+v", v)
	}
	if !v.CanPublishSource(SourceMicrophone) || v.CanPublishSource(SourceCamera) {
		t.Fatalf("权限解析错误：%+v", v.Permission)
	}
	a := parts[1]
	if a.JoinedAt != 12 || !a.CanPublishSource(SourceCamera) || !a.CanPublishSource(SourceMicrophone) {
		t.Fatalf("snake_case 解析错误：%+v %+v", a, a.Permission)
	}
	if f.calls[0] != "ListParticipants" || f.bodies["ListParticipants"]["room"] != "hall_default" {
		t.Fatalf("请求路径/体错误：%v %v", f.calls, f.bodies)
	}
	// Authorization 为 roomAdmin + room 的短期令牌
	tok := strings.TrimPrefix(f.auth[0], "Bearer ")
	video := decodePayload(t, tok)["video"].(map[string]any)
	if video["roomAdmin"] != true || video["room"] != "hall_default" {
		t.Fatalf("服务端 API 令牌授权错误：%v", video)
	}
}

func TestClientUpdatePermissionBody(t *testing.T) {
	f := &fakeTwirp{handler: func(method string, _ map[string]any) (int, string) {
		return 200, `{"identity":"v_0123456789abcdef0123","tracks":[]}`
	}}
	c := newTestClient(t, f, time.Second, 2)
	if _, err := c.UpdatePermission(context.Background(), "hall_default", "v_0123456789abcdef0123", PermissionFor([]string{SourceMicrophone})); err != nil {
		t.Fatal(err)
	}
	perm := f.bodies["UpdateParticipant"]["permission"].(map[string]any)
	srcs := perm["can_publish_sources"].([]any)
	if perm["can_publish"] != true || perm["can_subscribe"] != true || perm["can_publish_data"] != false || perm["can_update_metadata"] != false || len(srcs) != 1 || srcs[0] != "MICROPHONE" {
		t.Fatalf("授予麦克风的请求体错误：%v", perm)
	}
	if _, err := c.UpdatePermission(context.Background(), "hall_default", "v_0123456789abcdef0123", PermissionFor(nil)); err != nil {
		t.Fatal(err)
	}
	perm = f.bodies["UpdateParticipant"]["permission"].(map[string]any)
	if perm["can_publish"] != false || len(perm["can_publish_sources"].([]any)) != 0 {
		t.Fatalf("收回全部发布权限时 can_publish 必须为 false（空来源 + true 等于允许全部）：%v", perm)
	}
	if err := c.RemoveParticipant(context.Background(), "hall_default", "v_0123456789abcdef0123", time.Unix(1800000000, 0)); err != nil {
		t.Fatal(err)
	}
	if f.bodies["RemoveParticipant"]["revoke_token_ts"] != "1800000000" || f.bodies["RemoveParticipant"]["identity"] != "v_0123456789abcdef0123" {
		t.Fatalf("移出请求体错误：%v", f.bodies["RemoveParticipant"])
	}
	if err := c.MutePublishedTrack(context.Background(), "hall_default", "a_1", "TR_v", true); err != nil {
		t.Fatal(err)
	}
	if f.bodies["MutePublishedTrack"]["track_sid"] != "TR_v" || f.bodies["MutePublishedTrack"]["muted"] != true {
		t.Fatalf("静音请求体错误：%v", f.bodies["MutePublishedTrack"])
	}
}

func TestClientErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{404, `{"code":"not_found","msg":"participant not found"}`, ErrNotFound},
		{401, `{"code":"unauthenticated"}`, ErrUnauthorized},
		{400, `{"code":"invalid_argument"}`, ErrBadRequest},
		{503, `{"code":"unavailable"}`, ErrUnavailable},
		{500, `not json`, ErrUnavailable},
	}
	for _, tc := range cases {
		f := &fakeTwirp{handler: func(string, map[string]any) (int, string) { return tc.status, tc.body }}
		c := newTestClient(t, f, time.Second, 1)
		_, err := c.UpdatePermission(context.Background(), "hall_default", "a_1", PermissionFor(nil))
		if !errors.Is(err, tc.want) {
			t.Fatalf("status=%d 期望 %v 实际 %v", tc.status, tc.want, err)
		}
		if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), "Bearer") {
			t.Fatal("错误信息不得包含密钥或令牌")
		}
	}
	// ListParticipants 房间不存在视为空
	f := &fakeTwirp{handler: func(string, map[string]any) (int, string) { return 404, `{"code":"not_found"}` }}
	c := newTestClient(t, f, time.Second, 1)
	parts, err := c.ListParticipants(context.Background(), "hall_default")
	if err != nil || len(parts) != 0 {
		t.Fatalf("房间不存在应返回空列表：%v %v", parts, err)
	}
}

func TestClientTimeoutAndUnreachable(t *testing.T) {
	f := &fakeTwirp{delay: 300 * time.Millisecond}
	c := newTestClient(t, f, 100*time.Millisecond, 1)
	if _, err := c.ListParticipants(context.Background(), "hall_default"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("超时应归类为不可用：%v", err)
	}
	down := NewClient(Config{APIKey: testKey, APISecret: testSecret, InternalURL: "http://127.0.0.1:1", Timeout: 300 * time.Millisecond}, nil)
	if _, err := down.ListParticipants(context.Background(), "hall_default"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("连不上应归类为不可用：%v", err)
	}
	disabled := NewClient(Config{}, nil)
	if _, err := disabled.ListParticipants(context.Background(), "hall_default"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("未配置应返回 ErrDisabled：%v", err)
	}
}

func TestClientConcurrencyLimit(t *testing.T) {
	f := &fakeTwirp{delay: 50 * time.Millisecond}
	c := newTestClient(t, f, 5*time.Second, 3)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.MutePublishedTrack(context.Background(), "hall_default", "a_1", "TR", true)
		}()
	}
	wg.Wait()
	if got := f.maxSeen.Load(); got > 3 {
		t.Fatalf("并发上限 3，实际同时 %d", got)
	}
}
