package hall

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/livekit"
	"github.com/custom-service/backend/internal/ws"
)

// Start 启动 30 秒有界对账（以 LiveKit 为权威）：
//   - 刷新在线集合；释放已离开者的名额；续租「正在发布且未静音」的名额；租约过期即释放；
//   - 没有名额却有发布权限（含视频关闭、被拉黑、被管理员静音）→ 收回权限；
//   - 大厅关闭 → 移出全部参与者；禁入期内仍在房间 → 移出。
//
// 多副本时借 Redis 计数键保证同一窗口只有一个节点执行。
func (s *Service) Start(ctx context.Context) {
	s.startAlertWorker(ctx) // [122] 敏感消息告警邮件发件箱
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.biz.Error("hall_reconcile_loop_panic", zap.Any("err", r), zap.Stack("stack"))
			}
		}()
		t := time.NewTicker(reconcileInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !s.media.Enabled() {
					continue
				}
				s.reconcileGuarded(ctx)
			}
		}
	}()
}

func (s *Service) reconcileGuarded(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.biz.Error("hall_reconcile_panic", zap.Any("err", r), zap.Stack("stack"))
		}
	}()
	if s.limiter != nil {
		ok, err := s.limiter.AllowAction(ctx, "job_hall_reconcile", 1, reconcileInterval-5*time.Second, "global")
		if err != nil || !ok {
			return
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s.ReconcileOnce(rctx)
}

// ReconcileStats 一轮对账的统计（日志与测试用）。
type ReconcileStats struct {
	Participants int
	Released     int
	Renewed      int
	Revoked      int
	Removed      int
}

// ReconcileOnce 执行一轮对账；单轮修改类 LiveKit 调用最多 maxReconcileCalls 次（超出留到下一轮）。
func (s *Service) ReconcileOnce(ctx context.Context) ReconcileStats {
	runID := uuid.NewString()
	start := s.now()
	var stats ReconcileStats
	parts, err := s.media.ListParticipants(ctx, s.room)
	if err != nil {
		s.biz.Warn("hall_reconcile_failed", zap.String("run_id", runID), zap.String("stage", "list"), zap.Error(err))
		return stats
	}
	stats.Participants = len(parts)
	ids := make([]string, 0, len(parts))
	inRoom := make(map[string]*livekit.ParticipantInfo, len(parts))
	for i := range parts {
		if parts[i].Identity == "" {
			continue
		}
		ids = append(ids, parts[i].Identity)
		inRoom[parts[i].Identity] = &parts[i]
	}
	if err := s.state.replaceOnline(ctx, ids); err != nil {
		s.biz.Warn("hall_reconcile_failed", zap.String("run_id", runID), zap.String("stage", "online"), zap.Error(err))
		return stats
	}
	_ = s.state.touchParticipants(ctx, ids)
	st := s.loadSettings(ctx)
	calls := 0
	if !st.Enabled {
		for _, id := range ids {
			if calls >= maxReconcileCalls {
				break
			}
			calls++
			if err := s.media.RemoveParticipant(ctx, s.room, id, s.now()); err == nil {
				stats.Removed++
			}
		}
		_ = s.state.clearSlots(ctx, slotSpeaker)
		_ = s.state.clearSlots(ctx, slotCamera)
		s.logReconcile(runID, start, stats)
		return stats
	}
	banned, err := s.state.bannedAmong(ctx, ids)
	if err != nil {
		banned = map[string]bool{}
	}
	// 被拉黑访客：按档案里的 vid 批量查询
	recs, _ := s.state.participants(ctx, ids)
	var vids []string
	for _, id := range ids {
		if r, ok := recs[id]; ok && r.VID != "" && isVisitorIdentity(id) {
			vids = append(vids, r.VID)
		}
	}
	blocked := map[string]bool{}
	if len(vids) > 0 {
		if b, err := s.store.BlockedVisitorIDs(ctx, vids); err == nil {
			blocked = b
		}
	}
	// 1) 释放已不在房间的名额持有者
	for _, kind := range []slotKind{slotSpeaker, slotCamera} {
		members, err := s.state.slotMembers(ctx, kind)
		if err != nil {
			continue
		}
		for _, m := range members {
			if inRoom[m] == nil {
				if ok, _ := s.state.releaseSlot(ctx, kind, m); ok {
					stats.Released++
				}
			}
		}
	}
	if !st.VideoEnabled {
		if members, err := s.state.slotMembers(ctx, slotCamera); err == nil {
			stats.Released += len(members)
		}
		_ = s.state.clearSlots(ctx, slotCamera)
	}
	// 2) 逐个参与者：禁入移出 / 拉黑释放 / 续租 / 收回多余权限
	for _, id := range ids {
		p := inRoom[id]
		if !ValidIdentity(id) {
			continue
		}
		if banned[id] {
			if calls < maxReconcileCalls {
				calls++
				if err := s.media.RemoveParticipant(ctx, s.room, id, s.now()); err == nil {
					stats.Removed++
				}
			}
			continue
		}
		if isVisitorIdentity(id) && blocked[recs[id].VID] {
			for _, kind := range []slotKind{slotSpeaker, slotCamera} {
				if ok, _ := s.state.releaseSlot(ctx, kind, id); ok {
					stats.Released++
				}
			}
		}
		if p.Publishing(livekit.SourceMicrophone) && isVisitorIdentity(id) {
			if held, _ := s.state.holdsSlot(ctx, slotSpeaker, id); held {
				_ = s.state.renewSlot(ctx, slotSpeaker, id)
				stats.Renewed++
			}
		}
		if st.VideoEnabled && p.Publishing(livekit.SourceCamera) {
			if held, _ := s.state.holdsSlot(ctx, slotCamera, id); held {
				_ = s.state.renewSlot(ctx, slotCamera, id)
				stats.Renewed++
			}
		}
		desired, err := s.desiredSources(ctx, id, st)
		if err != nil {
			continue
		}
		want := toSet(desired)
		excess := (p.CanPublishSource(livekit.SourceMicrophone) && !want[livekit.SourceMicrophone]) ||
			(p.CanPublishSource(livekit.SourceCamera) && !want[livekit.SourceCamera]) ||
			p.CanPublishSource("SCREEN_SHARE") || p.CanPublishSource("SCREEN_SHARE_AUDIO") ||
			(p.Permission != nil && (p.Permission.CanPublishData || p.Permission.CanUpdateMetadata))
		if excess && calls < maxReconcileCalls {
			calls++
			if _, err := s.syncPermission(ctx, id, st); err == nil {
				stats.Revoked++
				s.sec.Info("hall_reconcile_permission_revoked", zap.String("run_id", runID), zap.String("identity", id))
			}
		}
	}
	s.logReconcile(runID, start, stats)
	return stats
}

func (s *Service) logReconcile(runID string, start time.Time, st ReconcileStats) {
	s.biz.Info("hall_reconcile_done", zap.String("event", "hall_reconcile"), zap.String("run_id", runID),
		zap.Int("participants", st.Participants), zap.Int("released", st.Released), zap.Int("renewed", st.Renewed),
		zap.Int("revoked", st.Revoked), zap.Int("removed", st.Removed), zap.Int64("duration_ms", s.now().Sub(start).Milliseconds()))
	if st.Revoked > 0 || st.Removed > 0 {
		// 对账真正执行了强制动作（收回发布权限 / 移出）才写审计，避免每 30 秒刷屏
		s.auditSystem("hall_system_reconcile_enforce", runID, zap.Int("revoked", st.Revoked), zap.Int("removed", st.Removed),
			zap.Int("released", st.Released))
	}
}

// auditSystem 系统（非人工）触发的强制动作审计：操作者固定为 system，关联 ID 用于串联业务日志。
// 只记录 identity 与计数，不记录 vid、令牌等凭证类字段。
func (s *Service) auditSystem(action, runID string, fields ...zap.Field) {
	base := []zap.Field{zap.String("event", action), zap.String("actor", "system"), zap.String("room", s.room), zap.String("run_id", runID)}
	s.audit.Info(action, append(base, fields...)...)
}

// ============ 联动钩子 ============

// OnSettingsChanged 管理员保存设置后调用：清缓存、广播 hall_settings，并在后台执行「关大厅移出全部 / 关视频收回摄像头」。
func (s *Service) OnSettingsChanged(ctx context.Context, before Settings) Settings {
	s.InvalidateSettings()
	after := s.loadSettings(ctx)
	s.hub.Broadcast(ctx, &ws.Envelope{Type: ws.HallTypeSettings, TS: ws.NowMS(), Extra: after.BroadcastExtra()})
	s.biz.Info("hall_settings_changed", zap.String("event", "hall_settings_changed"),
		zap.Bool("enabled_before", before.Enabled), zap.Bool("enabled", after.Enabled),
		zap.Bool("video_before", before.VideoEnabled), zap.Bool("video", after.VideoEnabled),
		zap.Int("max_speakers", after.MaxSpeakers), zap.Int("max_cameras", after.MaxCameras), zap.Int("max_participants", after.MaxParticipants))
	if !s.media.Enabled() {
		return after
	}
	if before.Enabled && !after.Enabled {
		s.runBackground("hall_disable_remove_all", 2*time.Minute, func(bctx context.Context) { s.removeAll(bctx) })
	} else if before.VideoEnabled && !after.VideoEnabled {
		s.runBackground("hall_video_disable_revoke", time.Minute, func(bctx context.Context) { s.revokeAllCameras(bctx, after) })
	}
	return after
}

// LoadSettingsFresh 绕过缓存读取当前设置（保存前取旧值用）。
func (s *Service) LoadSettingsFresh(ctx context.Context) Settings { return s.loadSettings(ctx) }

// removeAll 大厅关闭：移出全部参与者并清空名额。
func (s *Service) removeAll(ctx context.Context) {
	parts, err := s.media.ListParticipants(ctx, s.room)
	removed, failed := 0, 0
	if err == nil {
		for _, p := range parts {
			if ctx.Err() != nil {
				break
			}
			if rerr := s.media.RemoveParticipant(ctx, s.room, p.Identity, s.now()); rerr == nil {
				removed++
			} else {
				failed++
			}
		}
	}
	_ = s.state.clearSlots(ctx, slotSpeaker)
	_ = s.state.clearSlots(ctx, slotCamera)
	_ = s.state.clearOnline(ctx)
	s.biz.Info("hall_disabled_remove_all", zap.String("event", "hall_disabled_remove_all"), zap.Int("removed", removed), zap.Int("failed", failed), zap.Error(err))
	s.auditSystem("hall_system_remove_all", uuid.NewString(), zap.Int("removed", removed), zap.Int("failed", failed), zap.Bool("list_ok", err == nil))
}

// revokeAllCameras 视频关闭：收回所有人的摄像头权限并清空摄像头名额。
func (s *Service) revokeAllCameras(ctx context.Context, st Settings) {
	targets := map[string]bool{}
	if members, err := s.state.slotMembers(ctx, slotCamera); err == nil {
		for _, m := range members {
			targets[m] = true
		}
	}
	_ = s.state.clearSlots(ctx, slotCamera)
	parts, err := s.media.ListParticipants(ctx, s.room)
	for _, p := range parts {
		if p.HasTrack(livekit.SourceCamera) || p.CanPublishSource(livekit.SourceCamera) {
			targets[p.Identity] = true
		}
	}
	revoked, failed := 0, 0
	for _, id := range sortedKeys(targets) {
		if !ValidIdentity(id) {
			continue
		}
		if _, serr := s.syncPermission(ctx, id, st); serr == nil {
			revoked++
			s.notifyTarget(ctx, id, map[string]any{"action": "camera_off"})
		} else {
			failed++
		}
	}
	s.biz.Info("hall_video_disabled_revoke", zap.String("event", "hall_video_disabled_revoke"), zap.Int("revoked", revoked), zap.Int("failed", failed), zap.Error(err))
	s.auditSystem("hall_system_revoke_cameras", uuid.NewString(), zap.Int("revoked", revoked), zap.Int("failed", failed), zap.Bool("list_ok", err == nil))
}

// OnVisitorBlocked 拉黑生效：若该访客正占用开麦/摄像头名额，立即释放并收回权限（静默，不通知访客）。
func (s *Service) OnVisitorBlocked(vid string, blocked bool) {
	if !blocked || vid == "" || !s.media.Enabled() {
		return
	}
	identity := s.VisitorIdentity(vid)
	s.runBackground("hall_block_revoke", 15*time.Second, func(ctx context.Context) {
		spk, _ := s.state.releaseSlot(ctx, slotSpeaker, identity)
		cam, _ := s.state.releaseSlot(ctx, slotCamera, identity)
		if !spk && !cam {
			return
		}
		_, err := s.syncPermission(ctx, identity, s.Settings(ctx))
		s.biz.Info("hall_blocked_media_revoked", zap.String("event", "hall_blocked_media_revoked"), zap.String("identity", identity),
			zap.Bool("mic", spk), zap.Bool("camera", cam), zap.Error(err))
		s.auditSystem("hall_system_block_revoke", uuid.NewString(), zap.String("identity", identity),
			zap.Bool("mic", spk), zap.Bool("camera", cam), zap.Bool("permission_synced", err == nil))
	})
}

// Wait 等待后台任务结束（测试用）。
func (s *Service) Wait() { s.bgWG.Wait() }

// WaitTimeout 优雅退出时最多等待 d；超时返回 false（后台任务各自有超时，不会无限挂起）。
func (s *Service) WaitTimeout(d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.bgWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
