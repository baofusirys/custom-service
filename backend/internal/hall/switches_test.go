package hall

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/custom-service/backend/internal/livekit"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

// [123] 所有客服可切换的大厅开关。

func TestSwitchesAnyAgentCanToggleAndAudit(t *testing.T) {
	h := newHarness(t)
	// 访客不能调用；空请求拒绝
	if _, err := h.svc.UpdateSwitches(ctx, visitorA(vid1), &SwitchesInput{ChatEnabled: ptr(false)}); err == nil {
		t.Fatal("访客不能切换开关")
	}
	asErr(t, func() error { _, err := h.svc.UpdateSwitches(ctx, agentA(2, false), &SwitchesInput{}); return err }(), 40060)
	// 普通客服（非管理员）可切换
	v, err := h.svc.UpdateSwitches(ctx, agentA(2, false), &SwitchesInput{ChatEnabled: ptr(false), VideoEnabled: ptr(true)})
	if err != nil || v.ChatEnabled || !v.VideoEnabled || !v.Enabled || !v.VoiceEnabled {
		t.Fatalf("普通客服应能切换：%+v %v", v, err)
	}
	if v.UpdatedBy != "客服·小王" || !strings.HasSuffix(v.UpdatedAt, "+08:00") {
		t.Fatalf("应记录最后修改人与北京时间：%+v", v)
	}
	b := h.hub.find(func(e *ws.Envelope) bool { return e.Type == ws.HallTypeSettings })
	if b == nil {
		t.Fatal("应广播 hall_settings")
	}
	ex := b.Extra.(map[string]any)
	if ex["chat_enabled"] != false || ex["voice_enabled"] != true || ex["video_enabled"] != true || ex["enabled"] != true {
		t.Fatalf("广播字段不符：%v", ex)
	}
	got, err := h.svc.Switches(ctx, agentA(3, false))
	if err != nil || got.ChatEnabled || got.UpdatedBy != "客服·小王" {
		t.Fatalf("其他客服读取应一致：%+v %v", got, err)
	}
	// 停用的客服不能切换
	h.store.agents[3].Active = false
	if _, err := h.svc.UpdateSwitches(ctx, agentA(3, false), &SwitchesInput{ChatEnabled: ptr(true)}); err == nil {
		t.Fatal("停用账号不能切换")
	}
	// 公开设置：总开关关闭时其余均为 false
	if _, err := h.svc.UpdateSwitches(ctx, agentA(2, false), &SwitchesInput{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	if e, v2, c, vo := h.svc.PublicSwitches(ctx); e || v2 || c || vo {
		t.Fatalf("总开关关闭时公开设置应全为 false：%v %v %v %v", e, v2, c, vo)
	}
	if _, err := h.svc.Join(ctx, visitorA(vid1)); err == nil {
		t.Fatal("总开关关闭后不能入场")
	}
	// 再打开：其余开关保留原值
	v, _ = h.svc.UpdateSwitches(ctx, agentA(2, false), &SwitchesInput{Enabled: ptr(true)})
	if !v.Enabled || v.ChatEnabled || !v.VideoEnabled {
		t.Fatalf("重新打开总开关应保留其余开关：%+v", v)
	}
}

func TestChatSwitchBlocksMessagesAndUploads(t *testing.T) {
	h := newHarness(t)
	pa := &fakePeer{visitor: true, id: vid1, conn: "ca"}
	subscribe(t, h, pa)
	pg := &fakePeer{id: "2", conn: "cg"}
	subscribe(t, h, pg)
	if _, err := h.svc.UpdateSwitches(ctx, agentA(2, false), &SwitchesInput{ChatEnabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	chat(h, pa, id, "你好", "")
	if r := replyFor(t, pa, id); r.Type != "error" || !strings.Contains(r.Content, "聊天已关闭") {
		t.Fatalf("聊天关闭时访客消息应被拒：%+v", r)
	}
	id2 := uuid.NewString()
	h.svc.handleFrame(pg, &ws.Envelope{Type: "hall_chat", ID: id2, Content: "客服也不能发", TS: time.Now().UnixMilli()})
	if r := replyFor(t, pg, id2); r.Type != "error" {
		t.Fatalf("聊天关闭时客服消息也应被拒：%+v", r)
	}
	if h.store.msgs[id] != nil || h.store.msgs[id2] != nil {
		t.Fatal("被拒消息不得落库")
	}
	asErr(t, h.svc.AuthorizeUpload(ctx, visitorA(vid1)), 40378)
	asErr(t, h.svc.AuthorizeUpload(ctx, agentA(2, false)), 40378)
	// 历史仍可查看
	if _, err := h.svc.Messages(ctx, visitorA(vid1), "", 50); err != nil {
		t.Fatalf("聊天关闭时历史仍可查看：%v", err)
	}
	// 打开后恢复
	_, _ = h.svc.UpdateSwitches(ctx, agentA(2, false), &SwitchesInput{ChatEnabled: ptr(true)})
	id3 := uuid.NewString()
	chat(h, pa, id3, "恢复了", "")
	if r := replyFor(t, pa, id3); r.Type != "hall_ack" {
		t.Fatalf("打开后应恢复：%+v", r)
	}
}

func TestVoiceSwitchRevokesMicsAndBlocksMic(t *testing.T) {
	h := newHarness(t)
	id1 := h.svc.VisitorIdentity(vid1)
	if _, err := h.svc.Join(ctx, visitorA(vid1)); err != nil {
		t.Fatal(err)
	}
	h.media.join(id1, &livekit.Permission{CanSubscribe: true})
	if _, err := h.svc.SetMic(ctx, visitorA(vid1), true); err != nil {
		t.Fatal(err)
	}
	h.media.join("a_2", &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}},
		livekit.TrackInfo{Sid: "TR_a", Source: livekit.SourceMicrophone})
	if _, err := h.svc.UpdateSwitches(ctx, agentA(2, false), &SwitchesInput{VoiceEnabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	if n, _ := h.svc.state.slotCount(ctx, slotSpeaker); n != 0 {
		t.Fatalf("语音关闭应清空开麦名额：%d", n)
	}
	for _, id := range []string{id1, "a_2"} {
		if p, ok := h.media.perm(id); !ok || hasSource(p, livekit.SourceMicrophone) {
			t.Fatalf("%s 的麦克风应被收回：%+v", id, p)
		}
	}
	h.mr.FastForward(61 * time.Second)
	_, err := h.svc.SetMic(ctx, visitorA(vid1), true)
	asErr(t, err, 40377)
	// 客服入场令牌不带麦克风
	res, err := h.svc.Join(ctx, agentA(2, false))
	if err != nil {
		t.Fatal(err)
	}
	vg := jwtPayload(t, res.Token)["video"].(map[string]any)
	if srcs, _ := vg["canPublishSources"].([]any); len(srcs) != 0 {
		t.Fatalf("语音关闭时客服令牌不应带麦克风：%v", srcs)
	}
	if res.VoiceEnabled || !res.ChatEnabled {
		t.Fatalf("入场返回应带开关状态：%+v", res)
	}
	// 回调兜底：语音关闭时有人发布麦克风立即收回
	h.media.join(id1, &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceMicrophone}},
		livekit.TrackInfo{Sid: "TR_v", Source: livekit.SourceMicrophone})
	tp := `{"event":"track_published","id":"EV_v1","room":{"name":"hall_default"},"participant":{"identity":"` + id1 + `"},"track":{"sid":"TR_v","type":"AUDIO","source":"MICROPHONE"}}`
	if err := h.svc.HandleWebhook(ctx, signedWebhook(t, tp), []byte(tp)); err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	if p, _ := h.media.perm(id1); hasSource(p, livekit.SourceMicrophone) {
		t.Fatal("语音关闭时发布麦克风应被收回")
	}
	// 打开语音后可以开麦
	_, _ = h.svc.UpdateSwitches(ctx, agentA(2, false), &SwitchesInput{VoiceEnabled: ptr(true)})
	h.svc.Wait()
	if p, _ := h.media.perm("a_2"); !hasSource(p, livekit.SourceMicrophone) {
		t.Fatalf("语音重新打开后，已在大厅的客服应自动恢复麦克风权限：%+v", p)
	}
	h.mr.FastForward(61 * time.Second)
	if _, err := h.svc.SetMic(ctx, visitorA(vid1), true); err != nil {
		t.Fatalf("语音打开后应能开麦：%v", err)
	}
	_ = store.HallDefaultRoom
}
