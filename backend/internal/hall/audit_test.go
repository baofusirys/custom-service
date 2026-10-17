package hall

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/custom-service/backend/internal/livekit"
)

// observeAudit 把大厅服务的审计日志替换为可观测 logger（同包测试）。
func observeAudit(h *harness) *observer.ObservedLogs {
	core, logs := observer.New(zapcore.InfoLevel)
	h.svc.audit = zap.New(core)
	return logs
}

func auditEntry(logs *observer.ObservedLogs, action string) map[string]any {
	for _, e := range logs.FilterMessage(action).All() {
		return e.ContextMap()
	}
	return nil
}

// 系统（非人工）触发的强制动作必须写审计：关闭大厅移出全部、关闭视频收回摄像头、拉黑收回媒体权限、对账强制。
// 审计只含 identity 与计数，绝不含 vid。
func TestSystemEnforcementWritesAudit(t *testing.T) {
	h := newHarness(t)
	logs := observeAudit(h)
	id1 := h.svc.VisitorIdentity(vid1)

	// 关闭视频：收回摄像头
	h.setSetting(KeyVideoEnabled, "true")
	h.media.join(id1, &livekit.Permission{CanSubscribe: true, CanPublish: true, CanPublishSources: []string{livekit.SourceCamera}})
	before := h.svc.LoadSettingsFresh(ctx)
	h.store.set(KeyVideoEnabled, "false")
	h.svc.OnSettingsChanged(ctx, before)
	h.svc.Wait()
	if e := auditEntry(logs, "hall_system_revoke_cameras"); e == nil || e["actor"] != "system" || e["revoked"] != int64(1) {
		t.Fatalf("关闭视频应写审计：%v", e)
	}

	// 拉黑：占用的开麦名额释放并收回权限
	_, _, _, _ = h.svc.state.acquireSlot(ctx, slotSpeaker, id1, 6)
	h.svc.OnVisitorBlocked(vid1, true)
	h.svc.Wait()
	e := auditEntry(logs, "hall_system_block_revoke")
	if e == nil || e["identity"] != id1 || e["mic"] != true {
		t.Fatalf("拉黑收回应写审计：%v", e)
	}

	// 关闭大厅：移出全部
	before = h.svc.LoadSettingsFresh(ctx)
	h.store.set(KeyEnabled, "false")
	h.svc.OnSettingsChanged(ctx, before)
	h.svc.Wait()
	if e := auditEntry(logs, "hall_system_remove_all"); e == nil || e["removed"] != int64(1) {
		t.Fatalf("关闭大厅应写审计：%v", e)
	}

	for _, entry := range logs.All() {
		for k, v := range entry.ContextMap() {
			if s, ok := v.(string); ok && (s == vid1 || k == "vid") {
				t.Fatalf("审计不得包含访客 vid：%s=%v", k, v)
			}
		}
	}
}
