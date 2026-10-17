package hall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/custom-service/backend/internal/livekit"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

var ctx = context.Background()

const vid1 = "11111111-2222-4333-8444-555555555555"
const vid2 = "66666666-7777-4888-9999-aaaaaaaaaaaa"

// identity 稳定、格式固定、不含 vid 的任何片段；不同访客不同；显示码 4 位。
func TestIdentityStableAndHidesVID(t *testing.T) {
	d, err := newIdentityDeriver(testDataKey)
	if err != nil {
		t.Fatal(err)
	}
	id1, code1 := d.visitor(vid1)
	id1b, code1b := d.visitor(vid1)
	id2, _ := d.visitor(vid2)
	if id1 != id1b || code1 != code1b {
		t.Fatal("同一 vid 的 identity/显示码必须稳定")
	}
	if id1 == id2 {
		t.Fatal("不同访客 identity 不应相同")
	}
	if !visitorIdentityPattern.MatchString(id1) || len(code1) != 4 {
		t.Fatalf("格式错误：%s %s", id1, code1)
	}
	plain := strings.ReplaceAll(vid1, "-", "")
	for i := 0; i+6 <= len(plain); i++ {
		if strings.Contains(id1, plain[i:i+6]) {
			t.Fatalf("identity 不得包含 vid 片段：%s", plain[i:i+6])
		}
	}
	// 子密钥与 DATA_AES_KEY 隔离：换主密钥结果不同；直接用主密钥 HMAC 也得不到同样结果
	d2, _ := newIdentityDeriver([]byte("fedcba9876543210fedcba9876543210"))
	if other, _ := d2.visitor(vid1); other == id1 {
		t.Fatal("不同主密钥应得到不同 identity")
	}
	if _, err := newIdentityDeriver([]byte("short")); err == nil {
		t.Fatal("主密钥过短应报错")
	}
	if !ValidIdentity("a_12") || ValidIdentity("a_") || ValidIdentity("v_XYZ") || ValidIdentity("v_"+strings.Repeat("0", 21)) || ValidIdentity(vid1) {
		t.Fatal("identity 白名单校验错误")
	}
}

func TestSettingValidationBounds(t *testing.T) {
	ok := map[string][]string{
		KeyEnabled: {"true", "false"}, KeyVideoEnabled: {"true", "false"},
		KeyMaxSpeakers: {"1", "6", "20"}, KeyMaxCameras: {"0", "2", "6"}, KeyMaxParticipants: {"10", "100", "500"},
	}
	bad := map[string][]string{
		KeyEnabled: {"yes", "1", "TRUE", ""}, KeyVideoEnabled: {"on", "0"},
		KeyMaxSpeakers: {"0", "21", "abc", "6.5", "-1", "06", "+6", "0x6", ""},
		KeyMaxCameras:  {"7", "-1", "2.0"}, KeyMaxParticipants: {"9", "501", "1e2"},
	}
	for k, vals := range ok {
		for _, v := range vals {
			if _, err := ValidateSettingValue(k, v); err != nil {
				t.Fatalf("%s=%q 应合法：%v", k, v, err)
			}
		}
	}
	for k, vals := range bad {
		for _, v := range vals {
			if _, err := ValidateSettingValue(k, v); err == nil {
				t.Fatalf("%s=%q 应不合法", k, v)
			}
		}
	}
	if _, err := ValidateSettingValue("hall_other", "1"); err == nil {
		t.Fatal("未知键应拒绝")
	}
	// 存量非法值回退默认，不把大厅弄坏
	s := ParseSettings(map[string]string{KeyMaxSpeakers: "999", KeyEnabled: "maybe", KeyMaxCameras: "3"})
	if s.MaxSpeakers != 6 || !s.Enabled || s.MaxCameras != 3 {
		t.Fatalf("解析回退错误：%+v", s)
	}
}

// 名额并发：100 个 goroutine 同时抢 6 个开麦名额，成功数必须恰好 6。
func TestSlotConcurrentAcquireNeverExceedsMax(t *testing.T) {
	h := newHarness(t)
	var okCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, _, used, err := h.svc.state.acquireSlot(ctx, slotSpeaker, fmt.Sprintf("v_%020d", i), 6)
			if err != nil {
				t.Error(err)
				return
			}
			if used > 6 {
				t.Errorf("占用数超限：%d", used)
			}
			if ok {
				okCount.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if okCount.Load() != 6 {
		t.Fatalf("应恰好 6 个成功，实际 %d", okCount.Load())
	}
	n, _ := h.svc.state.slotCount(ctx, slotSpeaker)
	if n != 6 {
		t.Fatalf("占用数应为 6：%d", n)
	}
	// 已持有者再次占用 = 续租，不增加占用
	members, _ := h.svc.state.slotMembers(ctx, slotSpeaker)
	ok, newly, used, _ := h.svc.state.acquireSlot(ctx, slotSpeaker, members[0], 6)
	if !ok || newly || used != 6 {
		t.Fatalf("续租语义错误：%v %v %d", ok, newly, used)
	}
	// 租约过期后自动释放
	h.svc.now = func() time.Time { return time.Now().Add(slotLease + time.Second) }
	h.svc.state.now = h.svc.now
	if n, _ := h.svc.state.slotCount(ctx, slotSpeaker); n != 0 {
		t.Fatalf("租约过期应释放：%d", n)
	}
}

func TestVisitorJoinTokenAndHistory(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	visibleID, silencedOwn, silencedOther := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, _ = h.store.InsertHallMessage(ctx, &store.HallMessage{ID: visibleID, Room: store.HallDefaultRoom, Sender: "agent", SenderRef: "1", Identity: "a_1", Name: "客服·管理员", Content: "大家好", CreatedAt: now.Add(-3 * time.Second)})
	_, _ = h.store.InsertHallMessage(ctx, &store.HallMessage{ID: silencedOwn, Room: store.HallDefaultRoom, Sender: "visitor", SenderRef: vid1, Content: "自己的静默消息", CreatedAt: now.Add(-2 * time.Second), DeletedBy: store.HallSilencedBy, DeletedAt: nullTime(now)})
	_, _ = h.store.InsertHallMessage(ctx, &store.HallMessage{ID: silencedOther, Room: store.HallDefaultRoom, Sender: "visitor", SenderRef: vid2, Content: "别人的静默消息", CreatedAt: now.Add(-1 * time.Second), DeletedBy: store.HallSilencedBy, DeletedAt: nullTime(now)})

	res, err := h.svc.Join(ctx, visitorA(vid1))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "wss://cs.example.com/lk" || res.Room != "hall_default" || !strings.HasPrefix(res.Name, "访客 ") || res.VideoEnabled || res.MaxSpeakers != 6 || res.MaxCameras != 2 {
		t.Fatalf("join 字段错误：%+v", res)
	}
	if len(res.ICEServers) != 1 || strings.Contains(res.ICEServers[0].Username, vid1) || !strings.Contains(res.ICEServers[0].Username, res.Identity) {
		t.Fatalf("ICE 用户名应使用 identity 而不是 vid：%+v", res.ICEServers)
	}
	p := jwtPayload(t, res.Token)
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), vid1) || strings.Contains(string(raw), strings.ReplaceAll(vid1, "-", "")[:12]) {
		t.Fatal("令牌不得包含 vid")
	}
	if p["sub"] != res.Identity || p["iss"] != tKey {
		t.Fatalf("sub/iss 错误：%v", p)
	}
	var meta map[string]string
	_ = json.Unmarshal([]byte(p["metadata"].(string)), &meta)
	if meta["role"] != "visitor" || len(meta["code"]) != 4 || "访客 "+meta["code"] != res.Name || len(meta) != 2 {
		t.Fatalf("metadata 错误：%v", meta)
	}
	v := p["video"].(map[string]any)
	if v["canPublish"] != false || v["canSubscribe"] != true || v["canPublishData"] != false || v["canUpdateOwnMetadata"] != false || v["roomAdmin"] != nil {
		t.Fatalf("访客权限错误：%v", v)
	}
	ids := []string{}
	for _, m := range res.History {
		ids = append(ids, m.ID)
		if !strings.HasSuffix(m.CreatedAt, "+08:00") {
			t.Fatalf("created_at 必须带 +08:00：%s", m.CreatedAt)
		}
	}
	if len(ids) != 2 || ids[0] != visibleID || ids[1] != silencedOwn {
		t.Fatalf("历史应含公开消息 + 自己的静默消息（正序），实际 %v", ids)
	}
	// 客服看不到任何静默消息
	agentRes, err := h.svc.Join(ctx, agentA(2, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(agentRes.History) != 1 || agentRes.History[0].ID != visibleID {
		t.Fatalf("客服历史不应含静默消息：%+v", agentRes.History)
	}
	ap := jwtPayload(t, agentRes.Token)["video"].(map[string]any)
	srcs, _ := ap["canPublishSources"].([]any)
	if ap["canPublish"] != true || len(srcs) != 1 || srcs[0] != "microphone" || agentRes.Name != "客服·小王" {
		t.Fatalf("客服令牌/显示名错误：%v %s", ap, agentRes.Name)
	}
}

func nullTime(t time.Time) sql.NullTime { return sql.NullTime{Time: t, Valid: true} }

func TestJoinRejections(t *testing.T) {
	h := newHarness(t)
	h.setSetting(KeyEnabled, "false")
	_, err := h.svc.Join(ctx, visitorA(vid1))
	asErr(t, err, 40371)
	h.setSetting(KeyEnabled, "true")
	h.media.enabled = false
	h.svc.InvalidateSettings()
	_, err = h.svc.Join(ctx, visitorA(vid1))
	asErr(t, err, 40371)
	h.media.enabled = true
	h.svc.InvalidateSettings()

	id := h.svc.VisitorIdentity(vid1)
	_ = h.svc.state.ban(ctx, id, 90*time.Second)
	_, err = h.svc.Join(ctx, visitorA(vid1))
	he := asErr(t, err, 40372)
	if ra, _ := he.Data["retry_after"].(int64); ra < 1 || ra > 90 {
		t.Fatalf("retry_after 错误：%v", he.Data)
	}
	h.mr.FastForward(91 * time.Second)
	if _, err := h.svc.Join(ctx, visitorA(vid1)); err != nil {
		t.Fatalf("禁入到期后应可加入：%v", err)
	}

	h.setSetting(KeyMaxParticipants, "10")
	for i := 0; i < 10; i++ {
		_ = h.svc.state.addOnline(ctx, fmt.Sprintf("v_%020d", i))
	}
	_, err = h.svc.Join(ctx, visitorA(vid2))
	asErr(t, err, 40977)
	// 已在大厅的人重连不受人数限制；客服不受人数限制
	_ = h.svc.state.addOnline(ctx, h.svc.VisitorIdentity(vid1))
	if _, err := h.svc.Join(ctx, visitorA(vid1)); err != nil {
		t.Fatalf("已在大厅者重连不应被拒：%v", err)
	}
	if _, err := h.svc.Join(ctx, agentA(2, false)); err != nil {
		t.Fatalf("客服不受人数上限：%v", err)
	}
}

func TestMicFlowSlotsAndPermissions(t *testing.T) {
	h := newHarness(t)
	h.setSetting(KeyMaxSpeakers, "1")
	id1, id2 := h.svc.VisitorIdentity(vid1), h.svc.VisitorIdentity(vid2)

	// 未在 LiveKit 房间：40374，名额回滚
	_, err := h.svc.SetMic(ctx, visitorA(vid1), true)
	asErr(t, err, 40374)
	if n, _ := h.svc.state.slotCount(ctx, slotSpeaker); n != 0 {
		t.Fatal("失败后名额必须回滚")
	}
	h.media.join(id1, &livekit.Permission{CanSubscribe: true})
	h.media.join(id2, &livekit.Permission{CanSubscribe: true})
	res, err := h.svc.SetMic(ctx, visitorA(vid1), true)
	if err != nil || !res.On || res.Used != 1 || res.Max != 1 {
		t.Fatalf("开麦失败：%+v %v", res, err)
	}
	if p, _ := h.media.perm(id1); !hasSource(p, livekit.SourceMicrophone) || hasSource(p, livekit.SourceCamera) || p.CanPublishData {
		t.Fatalf("应只授予麦克风：%+v", p)
	}
	// 名额满
	_, err = h.svc.SetMic(ctx, visitorA(vid2), true)
	he := asErr(t, err, 40973)
	if he.Data["used"] != 1 || he.Data["max"] != 1 {
		t.Fatalf("满员 data 错误：%v", he.Data)
	}
	// 关麦释放名额并收回权限
	res, err = h.svc.SetMic(ctx, visitorA(vid1), false)
	if err != nil || res.On || res.Used != 0 {
		t.Fatalf("关麦失败：%+v %v", res, err)
	}
	if p, _ := h.media.perm(id1); p.CanPublish {
		t.Fatalf("关麦后应收回发布权限：%+v", p)
	}
	// LiveKit 不可用：503 且回滚
	h.media.updateErr = livekit.ErrUnavailable
	_, err = h.svc.SetMic(ctx, visitorA(vid2), true)
	asErr(t, err, 50370)
	if n, _ := h.svc.state.slotCount(ctx, slotSpeaker); n != 0 {
		t.Fatal("LiveKit 不可用时名额必须回滚")
	}
	h.media.updateErr = nil
	// 拉黑：40374（静默）
	h.store.blocked[vid2] = true
	_, err = h.svc.SetMic(ctx, visitorA(vid2), true)
	asErr(t, err, 40374)
	h.store.blocked[vid2] = false
	// 被移出：40374
	_ = h.svc.state.ban(ctx, id2, time.Minute)
	_, err = h.svc.SetMic(ctx, visitorA(vid2), true)
	asErr(t, err, 40374)
	// 客服没有开麦接口语义
	_, err = h.svc.SetMic(ctx, agentA(2, false), true)
	asErr(t, err, 40376)
	// 大厅关闭
	h.setSetting(KeyEnabled, "false")
	_, err = h.svc.SetMic(ctx, visitorA(vid1), true)
	asErr(t, err, 40371)
}

func TestCameraRules(t *testing.T) {
	h := newHarness(t)
	id1 := h.svc.VisitorIdentity(vid1)
	h.media.join(id1, &livekit.Permission{CanSubscribe: true})
	_, err := h.svc.SetCamera(ctx, visitorA(vid1), true)
	asErr(t, err, 40375) // 视频默认关闭
	h.setSetting(KeyVideoEnabled, "true")
	h.setSetting(KeyMaxCameras, "0")
	_, err = h.svc.SetCamera(ctx, visitorA(vid1), true)
	asErr(t, err, 40976)
	h.setSetting(KeyMaxCameras, "1")
	h.media.join("a_2", &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}})
	if _, err := h.svc.SetCamera(ctx, agentA(2, false), true); err != nil {
		t.Fatalf("客服开摄像头失败：%v", err)
	}
	if p, _ := h.media.perm("a_2"); !hasSource(p, livekit.SourceCamera) || !hasSource(p, livekit.SourceMicrophone) {
		t.Fatalf("客服应同时保留麦克风并获得摄像头：%+v", p)
	}
	_, err = h.svc.SetCamera(ctx, visitorA(vid1), true)
	asErr(t, err, 40976) // 名额含客服
}

// 视频开关关闭：立即撤销所有人的摄像头权限、清空名额、广播 hall_settings。
func TestVideoDisableRevokesAllCameras(t *testing.T) {
	h := newHarness(t)
	h.setSetting(KeyVideoEnabled, "true")
	id1 := h.svc.VisitorIdentity(vid1)
	h.media.join(id1, &livekit.Permission{CanSubscribe: true})
	h.media.join("a_2", &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}})
	if _, err := h.svc.SetCamera(ctx, visitorA(vid1), true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SetCamera(ctx, agentA(2, false), true); err != nil {
		t.Fatal(err)
	}
	h.media.join(id1, &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceCamera}},
		livekit.TrackInfo{Sid: "TR_v", Source: livekit.SourceCamera})
	before := h.svc.LoadSettingsFresh(ctx)
	h.store.set(KeyVideoEnabled, "false")
	after := h.svc.OnSettingsChanged(ctx, before)
	h.svc.Wait()
	if after.VideoEnabled {
		t.Fatal("设置应已关闭视频")
	}
	if p, _ := h.media.perm(id1); hasSource(p, livekit.SourceCamera) {
		t.Fatalf("访客摄像头权限应被撤销：%+v", p)
	}
	if p, _ := h.media.perm("a_2"); hasSource(p, livekit.SourceCamera) || !hasSource(p, livekit.SourceMicrophone) {
		t.Fatalf("客服摄像头应撤销、麦克风保留：%+v", p)
	}
	if n, _ := h.svc.state.slotCount(ctx, slotCamera); n != 0 {
		t.Fatal("摄像头名额应清空")
	}
	if e := h.hub.find(func(e *ws.Envelope) bool { return e.Type == ws.HallTypeSettings }); e == nil || e.Extra.(map[string]any)["video_enabled"] != false {
		t.Fatal("应广播 hall_settings")
	}
	// 之后再开摄像头被拒
	_, err := h.svc.SetCamera(ctx, visitorA(vid1), true)
	asErr(t, err, 40375)
}

func TestHallDisableRemovesEveryone(t *testing.T) {
	h := newHarness(t)
	h.media.join(h.svc.VisitorIdentity(vid1), nil)
	h.media.join("a_2", nil)
	before := h.svc.LoadSettingsFresh(ctx)
	h.store.set(KeyEnabled, "false")
	h.svc.OnSettingsChanged(ctx, before)
	h.svc.Wait()
	if len(h.media.parts) != 0 || len(h.media.removed) != 2 {
		t.Fatalf("关闭大厅应移出全部参与者：%v", h.media.removed)
	}
	if e := h.hub.find(func(e *ws.Envelope) bool { return e.Type == ws.HallTypeSettings }); e == nil || e.Extra.(map[string]any)["enabled"] != false {
		t.Fatal("应广播 hall_settings enabled=false")
	}
}

func TestKickBansAndModerationRules(t *testing.T) {
	h := newHarness(t)
	id1 := h.svc.VisitorIdentity(vid1)
	if _, err := h.svc.Join(ctx, visitorA(vid1)); err != nil {
		t.Fatal(err)
	}
	h.media.join(id1, &livekit.Permission{CanSubscribe: true})
	if _, err := h.svc.SetMic(ctx, visitorA(vid1), true); err != nil {
		t.Fatal(err)
	}
	res, err := h.svc.Moderate(ctx, agentA(2, false), ActionKick, id1, 0)
	if err != nil || res.Affected != 1 || res.Minutes != 30 {
		t.Fatalf("kick 失败：%+v %v", res, err)
	}
	if len(h.media.removed) != 1 || h.media.removed[0] != id1 {
		t.Fatal("应调用 RemoveParticipant")
	}
	if n, _ := h.svc.state.slotCount(ctx, slotSpeaker); n != 0 {
		t.Fatal("被移出者名额应释放")
	}
	if e := h.hub.find(func(e *ws.Envelope) bool {
		return e.Type == ws.HallTypeModeration && e.To == ws.HallTargetVisitorsPrefix+vid1
	}); e == nil ||
		e.Extra.(map[string]any)["action"] != "kicked" || e.Extra.(map[string]any)["minutes"] != 30 {
		t.Fatal("应定向通知被移出者")
	}
	_, err = h.svc.Join(ctx, visitorA(vid1))
	he := asErr(t, err, 40372)
	if ra := he.Data["retry_after"].(int64); ra < 1700 || ra > 1800 {
		t.Fatalf("禁入剩余应约 30 分钟：%d", ra)
	}
	_, err = h.svc.Messages(ctx, visitorA(vid1), "", 50)
	asErr(t, err, 40372)
	// WS：被移出后订阅/发消息都被拒
	p := &fakePeer{visitor: true, id: vid1, conn: "c1"}
	h.svc.handleFrame(p, &ws.Envelope{Type: "hall_subscribe", ID: uuid.NewString()})
	if e := p.wait(t, func(e *ws.Envelope) bool { return e.Type == "error" }); !strings.Contains(e.Content, "移出") {
		t.Fatalf("订阅应被拒：%v", e.Content)
	}
	// 规则：普通客服不能动客服；不能对自己；identity 非法；minutes 越界
	_, err = h.svc.Moderate(ctx, agentA(2, false), ActionMute, "a_3", 0)
	asErr(t, err, 40376)
	_, err = h.svc.Moderate(ctx, agentA(1, true), ActionKick, "a_1", 5)
	asErr(t, err, 40376)
	_, err = h.svc.Moderate(ctx, agentA(2, false), ActionMute, vid1, 0)
	asErr(t, err, 40060)
	_, err = h.svc.Moderate(ctx, agentA(2, false), ActionKick, h.svc.VisitorIdentity(vid2), 1441)
	asErr(t, err, 40060)
	_, err = h.svc.Moderate(ctx, agentA(2, false), "ban_all", "", 0)
	asErr(t, err, 40060)
	_, err = h.svc.Moderate(ctx, visitorA(vid2), ActionMute, id1, 0)
	asErr(t, err, 40376)
	// 管理员可以静音其他客服：收回其麦克风，重新加入后恢复
	h.media.join("a_3", &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}})
	if _, err := h.svc.Moderate(ctx, agentA(1, true), ActionMute, "a_3", 0); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.media.perm("a_3"); hasSource(p, livekit.SourceMicrophone) {
		t.Fatal("被管理员静音的客服应失去麦克风权限")
	}
	j, err := h.svc.Join(ctx, agentA(3, false))
	if err != nil {
		t.Fatal(err)
	}
	if v := jwtPayload(t, j.Token)["video"].(map[string]any); v["canPublish"] != true {
		t.Fatal("重新加入后客服应恢复麦克风")
	}
}

func TestMuteAllAndCameraOff(t *testing.T) {
	h := newHarness(t)
	id1, id2 := h.svc.VisitorIdentity(vid1), h.svc.VisitorIdentity(vid2)
	h.media.join(id1, &livekit.Permission{CanSubscribe: true})
	h.media.join(id2, &livekit.Permission{CanSubscribe: true})
	h.media.join("a_2", &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}})
	for _, v := range []string{vid1, vid2} {
		if _, err := h.svc.SetMic(ctx, visitorA(v), true); err != nil {
			t.Fatal(err)
		}
	}
	res, err := h.svc.Moderate(ctx, agentA(2, false), ActionMuteAll, "", 0)
	if err != nil || res.Affected != 2 {
		t.Fatalf("全员静音失败：%+v %v", res, err)
	}
	for _, id := range []string{id1, id2} {
		if p, _ := h.media.perm(id); p.CanPublish {
			t.Fatalf("%s 应被静音", id)
		}
	}
	if p, ok := h.media.perm("a_2"); ok && !hasSource(p, livekit.SourceMicrophone) {
		t.Fatal("全员静音只对访客")
	}
	if e := h.hub.find(func(e *ws.Envelope) bool { return e.To == ws.HallTargetAllVisitors }); e == nil {
		t.Fatal("应通知全部访客 mute_all")
	}
	h.setSetting(KeyVideoEnabled, "true")
	if _, err := h.svc.SetCamera(ctx, visitorA(vid1), true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Moderate(ctx, agentA(2, false), ActionCameraOff, id1, 0); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.media.perm(id1); hasSource(p, livekit.SourceCamera) {
		t.Fatal("camera_off 应收回摄像头")
	}
	// 对象不在大厅：幂等成功、affected=0
	res, err = h.svc.Moderate(ctx, agentA(2, false), ActionCameraOff, "v_"+strings.Repeat("e", 20), 0)
	if err != nil || res.Affected != 0 {
		t.Fatalf("不在大厅应幂等：%+v %v", res, err)
	}
}

func subscribe(t *testing.T, h *harness, p *fakePeer) {
	t.Helper()
	id := uuid.NewString()
	h.svc.handleFrame(p, &ws.Envelope{Type: "hall_subscribe", ID: id})
	p.wait(t, func(e *ws.Envelope) bool { return e.Type == "hall_subscribed" && e.ID == id })
}

func chat(h *harness, p *fakePeer, id, content, media string) {
	h.svc.handleFrame(p, &ws.Envelope{Type: "hall_chat", ID: id, Content: content, MediaURL: media, MediaKind: "image", MediaName: "伪造.exe", MediaSize: 1, TS: time.Now().UnixMilli()})
}

func replyFor(t *testing.T, p *fakePeer, id string) *ws.Envelope {
	t.Helper()
	return p.wait(t, func(e *ws.Envelope) bool { return (e.Type == "hall_ack" || e.Type == "error") && e.ID == id })
}

// 拉黑静默：仍可发，回 ack，只回显自己，不广播；历史只对自己可见。
func TestBlockedVisitorSilenced(t *testing.T) {
	h := newHarness(t)
	h.store.blocked[vid1] = true
	pa := &fakePeer{visitor: true, id: vid1, conn: "ca"}
	subscribe(t, h, pa)
	id := uuid.NewString()
	chat(h, pa, id, "我被拉黑了吗", "")
	if r := replyFor(t, pa, id); r.Type != "hall_ack" {
		t.Fatalf("静默消息也应回 ack：%+v", r)
	}
	echo := pa.wait(t, func(e *ws.Envelope) bool { return e.Type == ws.HallTypeChat && e.ID == id })
	if echo.Extra.(map[string]any)["identity"] != h.svc.VisitorIdentity(vid1) {
		t.Fatal("回显信息错误")
	}
	time.Sleep(50 * time.Millisecond)
	if h.hub.find(func(e *ws.Envelope) bool { return e.Type == ws.HallTypeChat }) != nil {
		t.Fatal("静默消息不得广播")
	}
	m := h.store.msgs[strings.ToLower(id)]
	if m == nil || m.DeletedBy != store.HallSilencedBy || !m.DeletedAt.Valid {
		t.Fatalf("静默消息应入库并标记：%+v", m)
	}
	own, _ := h.svc.Messages(ctx, visitorA(vid1), "", 50)
	other, _ := h.svc.Messages(ctx, visitorA(vid2), "", 50)
	if len(own) != 1 || len(other) != 0 {
		t.Fatalf("静默消息只对发送者可见：own=%d other=%d", len(own), len(other))
	}
	// 拉黑联动：占用的开麦名额立即释放并收回权限
	id1 := h.svc.VisitorIdentity(vid1)
	h.media.join(id1, &livekit.Permission{CanSubscribe: true})
	_, _, _, _ = h.svc.state.acquireSlot(ctx, slotSpeaker, id1, 6)
	h.svc.OnVisitorBlocked(vid1, true)
	h.svc.Wait()
	if held, _ := h.svc.state.holdsSlot(ctx, slotSpeaker, id1); held {
		t.Fatal("拉黑后名额应释放")
	}
	if p, ok := h.media.perm(id1); !ok || p.CanPublish {
		t.Fatal("拉黑后应收回发布权限")
	}
}

func TestChatValidationMediaAndRateLimit(t *testing.T) {
	h := newHarness(t)
	pa := &fakePeer{visitor: true, id: vid1, conn: "ca"}
	// 未订阅
	id := uuid.NewString()
	chat(h, pa, id, "hi", "")
	if r := replyFor(t, pa, id); r.Type != "error" || !strings.Contains(r.Content, "进入大厅") {
		t.Fatalf("未订阅应拒绝：%+v", r)
	}
	subscribe(t, h, pa)
	// 非 UUID v4
	chat(h, pa, "not-a-uuid", "hi", "")
	pa.wait(t, func(e *ws.Envelope) bool { return e.Type == "error" && strings.Contains(e.Content, "标识") })
	// 空内容 / 超长 / HTML 清洗
	id = uuid.NewString()
	chat(h, pa, id, "   ", "")
	if r := replyFor(t, pa, id); r.Type != "error" {
		t.Fatal("空内容应拒绝")
	}
	id = uuid.NewString()
	chat(h, pa, id, strings.Repeat("长", 1001), "")
	if r := replyFor(t, pa, id); r.Type != "error" {
		t.Fatal("超过 1000 字应拒绝")
	}
	// 附件：别人上传的、私聊上传的都拒绝；自己的大厅附件通过且 kind/name/size 以服务端为准
	otherFile := h.store.addHallFile(t, store.HallDefaultRoom, "visitor", vid2, "image/png", "b.png")
	privateFile := h.store.addHallFile(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "visitor", vid1, "image/png", "私聊.png")
	ownFile := h.store.addHallFile(t, store.HallDefaultRoom, "visitor", vid1, "image/png", "<b>图</b>.png")
	h.mr.FastForward(11 * time.Second) // 失败的请求同样计入限流窗口
	for _, bad := range []string{otherFile, privateFile, "https://evil.example.com/x.png", ownFile[:len(ownFile)-3] + "zzz"} {
		id = uuid.NewString()
		chat(h, pa, id, "看图", bad)
		if r := replyFor(t, pa, id); r.Type != "error" || !strings.Contains(r.Content, "附件") {
			t.Fatalf("非法附件应拒绝：%s -> %+v", bad, r)
		}
	}
	h.mr.FastForward(11 * time.Second) // 前面的失败请求也计入限流窗口，等窗口过去
	id = uuid.NewString()
	chat(h, pa, id, "", ownFile)
	if r := replyFor(t, pa, id); r.Type != "hall_ack" {
		t.Fatalf("自己的大厅附件应通过：%+v", r)
	}
	b := h.hub.find(func(e *ws.Envelope) bool { return e.Type == ws.HallTypeChat && e.ID == id })
	if b == nil || b.MediaKind != "image" || b.MediaName != "图.png" || b.MediaSize != 1234 || b.MediaURL != ownFile {
		t.Fatalf("广播附件字段应以服务端记录为准：%+v", b)
	}
	if b.From != "" || strings.Contains(fmt.Sprint(b.Extra), vid1) {
		t.Fatal("广播不得暴露 vid")
	}
	// 重复 ID 重试：只回 ack，不重复广播；不同内容复用 ID：冲突
	before := len(h.hub.broadcasts)
	chat(h, pa, id, "", ownFile)
	time.Sleep(50 * time.Millisecond)
	if len(h.hub.broadcasts) != before {
		t.Fatal("重试不应重复广播")
	}
	h.mr.FastForward(11 * time.Second)
	chat(h, pa, id, "改了内容", "")
	pa.wait(t, func(e *ws.Envelope) bool {
		return e.Type == "error" && e.ID == id && strings.Contains(e.Content, "冲突")
	})
	// 限流：每访客 5 条/10 秒，第 6 条拒绝（同一窗口）
	h.mr.FastForward(11 * time.Second)
	acks, errs := 0, 0
	for i := 0; i < 6; i++ {
		id = uuid.NewString()
		chat(h, pa, id, fmt.Sprintf("第%d条", i), "")
		r := replyFor(t, pa, id)
		if r.Type == "hall_ack" {
			acks++
		} else if strings.Contains(r.Content, "频繁") {
			errs++
		}
	}
	if acks != 5 || errs != 1 {
		t.Fatalf("限流应为 5 条通过 1 条拒绝：ack=%d err=%d", acks, errs)
	}
	// 客服更宽：20 条/10 秒
	pg := &fakePeer{visitor: false, id: "2", conn: "cg"}
	subscribe(t, h, pg)
	for i := 0; i < 6; i++ {
		id = uuid.NewString()
		chat(h, pg, id, "客服公告", "")
		if r := replyFor(t, pg, id); r.Type != "hall_ack" {
			t.Fatalf("客服第 %d 条不应被限流：%+v", i+1, r)
		}
	}
	// 退订后再发被拒
	h.svc.handleFrame(pa, &ws.Envelope{Type: "hall_unsubscribe", ID: uuid.NewString()})
	pa.wait(t, func(e *ws.Envelope) bool { return e.Type == "hall_unsubscribed" })
	h.mr.FastForward(11 * time.Second)
	id = uuid.NewString()
	chat(h, pa, id, "hi", "")
	if r := replyFor(t, pa, id); r.Type != "error" {
		t.Fatal("退订后应拒绝")
	}
}

func TestDeleteMessageBroadcastsAndHides(t *testing.T) {
	h := newHarness(t)
	id := uuid.NewString()
	_, _ = h.store.InsertHallMessage(ctx, &store.HallMessage{ID: id, Room: store.HallDefaultRoom, Sender: "visitor", SenderRef: vid1, Content: "违规内容", CreatedAt: time.Now()})
	if err := h.svc.DeleteMessage(ctx, agentA(2, false), id); err != nil {
		t.Fatal(err)
	}
	if e := h.hub.find(func(e *ws.Envelope) bool { return e.Type == ws.HallTypeDeleted && e.ID == id }); e == nil {
		t.Fatal("应广播 hall_deleted")
	}
	if list, _ := h.svc.Messages(ctx, visitorA(vid2), "", 50); len(list) != 0 {
		t.Fatal("删除后所有人不可见")
	}
	n := len(h.hub.broadcasts)
	if err := h.svc.DeleteMessage(ctx, agentA(2, false), id); err != nil || len(h.hub.broadcasts) != n {
		t.Fatal("重复删除应幂等且不重复广播")
	}
	asErr(t, h.svc.DeleteMessage(ctx, agentA(2, false), uuid.NewString()), 40477)
	asErr(t, h.svc.DeleteMessage(ctx, agentA(2, false), "1 OR 1=1"), 40060)
	asErr(t, h.svc.DeleteMessage(ctx, visitorA(vid1), id), 40376)
	_, err := h.svc.Messages(ctx, visitorA(vid1), "'; DROP TABLE hall_messages; --", 50)
	asErr(t, err, 40060)
}

func signedWebhook(t *testing.T, body string) string {
	t.Helper()
	tok, err := livekit.SignWebhook(tKey, tSecret, []byte(body), time.Now(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestWebhookHandling(t *testing.T) {
	h := newHarness(t)
	id1 := h.svc.VisitorIdentity(vid1)
	// 伪造签名
	body := `{"event":"participant_left","id":"EV_1","room":{"name":"hall_default"},"participant":{"identity":"` + id1 + `"}}`
	forged, _ := livekit.SignWebhook(tKey, "wrong-secret-0123456789abcdefghijklmnop", []byte(body), time.Now(), time.Minute)
	if err := h.svc.HandleWebhook(ctx, forged, []byte(body)); !errors.Is(err, ErrWebhookUnauthorized) {
		t.Fatalf("伪造签名应拒绝：%v", err)
	}
	if err := h.svc.HandleWebhook(ctx, signedWebhook(t, body), []byte(body+" ")); !errors.Is(err, ErrWebhookUnauthorized) {
		t.Fatal("篡改请求体应拒绝")
	}
	// participant_left 释放名额
	_, _, _, _ = h.svc.state.acquireSlot(ctx, slotSpeaker, id1, 6)
	_ = h.svc.state.addOnline(ctx, id1)
	if err := h.svc.HandleWebhook(ctx, signedWebhook(t, body), []byte(body)); err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	if held, _ := h.svc.state.holdsSlot(ctx, slotSpeaker, id1); held {
		t.Fatal("离开后名额应释放")
	}
	if on, _ := h.svc.state.isOnline(ctx, id1); on {
		t.Fatal("离开后应移出在线集合")
	}
	// 同一事件重放：去重
	_, _, _, _ = h.svc.state.acquireSlot(ctx, slotSpeaker, id1, 6)
	_ = h.svc.HandleWebhook(ctx, signedWebhook(t, body), []byte(body))
	h.svc.Wait()
	if held, _ := h.svc.state.holdsSlot(ctx, slotSpeaker, id1); !held {
		t.Fatal("重放事件不应再次处理")
	}
	// 视频关闭时发布摄像头：立即收回
	h.media.join(id1, &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceCamera}}, livekit.TrackInfo{Sid: "TR_c", Source: livekit.SourceCamera})
	tp := `{"event":"track_published","id":"EV_2","room":{"name":"hall_default"},"participant":{"identity":"` + id1 + `"},"track":{"sid":"TR_c","type":"VIDEO","source":"CAMERA"}}`
	if err := h.svc.HandleWebhook(ctx, signedWebhook(t, tp), []byte(tp)); err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	if p, ok := h.media.perm(id1); !ok || hasSource(p, livekit.SourceCamera) {
		t.Fatalf("视频关闭时摄像头应被收回：%+v", p)
	}
	// 未持有开麦名额却发布麦克风：收回
	h.media.join(id1, &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}}, livekit.TrackInfo{Sid: "TR_m", Source: livekit.SourceMicrophone})
	_, _ = h.svc.state.releaseSlot(ctx, slotSpeaker, id1)
	mp := strings.Replace(strings.Replace(tp, "EV_2", "EV_3", 1), "CAMERA", "MICROPHONE", 1)
	_ = h.svc.HandleWebhook(ctx, signedWebhook(t, mp), []byte(mp))
	h.svc.Wait()
	if p, _ := h.media.perm(id1); p.CanPublish {
		t.Fatal("无名额发布麦克风应被收回")
	}
	// 其它房间事件忽略
	other := `{"event":"room_finished","id":"EV_4","room":{"name":"other_room"}}`
	if err := h.svc.HandleWebhook(ctx, signedWebhook(t, other), []byte(other)); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileReleasesRevokesAndRemoves(t *testing.T) {
	h := newHarness(t)
	id1, id2 := h.svc.VisitorIdentity(vid1), h.svc.VisitorIdentity(vid2)
	ghost := "v_" + strings.Repeat("a", 20)
	// 幽灵名额：持有者不在房间
	_, _, _, _ = h.svc.state.acquireSlot(ctx, slotSpeaker, ghost, 6)
	// id1 持有名额且正在发布：续租
	h.media.join(id1, &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}}, livekit.TrackInfo{Sid: "m1", Source: livekit.SourceMicrophone})
	_, _, _, _ = h.svc.state.acquireSlot(ctx, slotSpeaker, id1, 6)
	// id2 没有名额却有麦克风权限：收回
	h.media.join(id2, &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}}, livekit.TrackInfo{Sid: "m2", Source: livekit.SourceMicrophone})
	// 被禁入者仍在房间：移出
	banned := "v_" + strings.Repeat("b", 20)
	h.media.join(banned, &livekit.Permission{CanSubscribe: true})
	_ = h.svc.state.ban(ctx, banned, time.Minute)
	stats := h.svc.ReconcileOnce(ctx)
	if stats.Released < 1 || stats.Renewed < 1 || stats.Revoked < 1 || stats.Removed != 1 {
		t.Fatalf("对账统计错误：%+v", stats)
	}
	if held, _ := h.svc.state.holdsSlot(ctx, slotSpeaker, ghost); held {
		t.Fatal("幽灵名额应释放")
	}
	if p, _ := h.media.perm(id2); p.CanPublish {
		t.Fatal("无名额者权限应收回")
	}
	if p, ok := h.media.perm(id1); ok && !hasSource(p, livekit.SourceMicrophone) {
		t.Fatal("合法开麦者不应被收回")
	}
	if online, _ := h.svc.state.onlineMembers(ctx); len(online) != 3 {
		t.Fatalf("在线集合应与 LiveKit 一致：%v", online)
	}
	// 租约到期（静音/停止发布超过租期）：释放并收回
	h.media.join(id1, &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}}, livekit.TrackInfo{Sid: "m1", Source: livekit.SourceMicrophone, Muted: true})
	later := time.Now().Add(slotLease + 5*time.Second)
	h.svc.now = func() time.Time { return later }
	h.svc.state.now = h.svc.now
	h.svc.ReconcileOnce(ctx)
	if p, _ := h.media.perm(id1); p.CanPublish {
		t.Fatal("租约过期后应收回麦克风")
	}
	// LiveKit 不可用：不改任何状态
	h.media.listErr = livekit.ErrUnavailable
	if s := h.svc.ReconcileOnce(ctx); s.Participants != 0 {
		t.Fatal("LiveKit 不可用时对账应跳过")
	}
}

func TestStateViewIncludesVIDOnlyForAgents(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.Join(ctx, visitorA(vid1)); err != nil {
		t.Fatal(err)
	}
	id1 := h.svc.VisitorIdentity(vid1)
	h.media.join(id1, &livekit.Permission{CanSubscribe: true})
	h.media.join("a_2", &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}}, livekit.TrackInfo{Source: livekit.SourceMicrophone})
	h.store.blocked[vid1] = true
	res, err := h.svc.State(ctx, agentA(2, false))
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts["participants"] != 2 || res.Degraded || res.Settings["max_participants"] != 100 {
		t.Fatalf("state 统计错误：%+v", res)
	}
	var v, a *StateParticipant
	for i := range res.Participants {
		if res.Participants[i].Identity == id1 {
			v = &res.Participants[i]
		} else {
			a = &res.Participants[i]
		}
	}
	if v == nil || v.VID != vid1 || !v.Blocked || v.Role != "visitor" || !strings.HasSuffix(v.JoinedAt, "+08:00") {
		t.Fatalf("访客视图错误：%+v", v)
	}
	if a == nil || a.AgentID != "2" || a.Role != "agent" || !a.MicOn || a.VID != "" {
		t.Fatalf("客服视图错误：%+v", a)
	}
	h.media.listErr = livekit.ErrUnavailable
	_ = h.svc.state.addOnline(ctx, id1)
	res, err = h.svc.State(ctx, agentA(2, false))
	if err != nil || !res.Degraded {
		t.Fatalf("LiveKit 不可用应降级：%+v %v", res, err)
	}
}

// [119] 集成测试回归：参与者在移出请求到达前已自行离开（媒体服务返回 unavailable 而非 not_found），
// 只要房间里确认已不在，就按成功处理；仍在房间且媒体服务故障时才报 50370。
func TestKickWhenParticipantAlreadyLeft(t *testing.T) {
	h := newHarness(t)
	id1 := h.svc.VisitorIdentity(vid1)
	if _, err := h.svc.Join(ctx, visitorA(vid1)); err != nil {
		t.Fatal(err)
	}
	// 未在 fakeMedia 房间中（已离开），RemoveParticipant 返回非 not_found 的故障
	h.media.removeErr = errors.New("livekit RemoveParticipant failed: status=503 code=unavailable")
	res, err := h.svc.Moderate(ctx, agentA(2, false), ActionKick, id1, 1)
	if err != nil || res.Minutes != 1 {
		t.Fatalf("已离开的参与者被移出应成功：%+v %v", res, err)
	}
	if _, err := h.svc.Join(ctx, visitorA(vid1)); err == nil {
		t.Fatal("移出后应禁止再次进入")
	}
	// 仍在房间且媒体服务故障：报 50370
	id2 := h.svc.VisitorIdentity(vid2)
	if _, err := h.svc.Join(ctx, visitorA(vid2)); err != nil {
		t.Fatal(err)
	}
	h.media.join(id2, &livekit.Permission{CanSubscribe: true})
	_, err = h.svc.Moderate(ctx, agentA(2, false), ActionKick, id2, 1)
	asErr(t, err, 50370)
}
