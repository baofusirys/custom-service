package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

// [113] 删除聊天记录 / 回收站 / 自动清理 / 撤回 / 拉黑 / 资料变更广播。

const (
	deleteBatchSize   = 200
	purgeBatchSize    = 200
	maxDeleteAllLoops = 2000 // 批次上限（约 40 万客户），防止异常数据导致无限循环
	autoCleanHour     = 4
	autoCleanMinute   = 30
)

// SetUploadsDir 注入上传目录（彻底删除时同步删除磁盘文件）。
func (s *Service) SetUploadsDir(dir string) { s.uploadsDir = dir }

// DeleteVisitors 把一批客户的聊天记录移入回收站，在线访客切换新会话并清空访客端，广播给所有客服。
func (s *Service) DeleteVisitors(ctx context.Context, visitorIDs []string, actor string) (store.DeleteResult, error) {
	res, err := s.store.SoftDeleteVisitors(ctx, visitorIDs, actor, time.Now())
	if err != nil {
		return res, err
	}
	for _, vid := range visitorIDs {
		s.resetOnlineVisitor(ctx, vid)
	}
	if s.hub != nil && res.Conversations > 0 {
		s.hub.BroadcastToAllAgents(&ws.Envelope{Type: "conversations_deleted", ID: uuid.NewString(), TS: ws.NowMS(),
			Extra: map[string]any{"visitor_ids": visitorIDs}})
	}
	return res, nil
}

// DeleteAllVisitors 分批把全部聊天记录移入回收站（每批独立短事务，避免长时间锁表）。
func (s *Service) DeleteAllVisitors(ctx context.Context, actor string) (store.DeleteResult, error) {
	var total store.DeleteResult
	now := time.Now()
	for i := 0; i < maxDeleteAllLoops; i++ {
		ids, err := s.store.NextActiveVisitorBatch(ctx, deleteBatchSize)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			break
		}
		r, err := s.store.SoftDeleteVisitors(ctx, ids, actor, now)
		if err != nil {
			return total, err
		}
		total.Visitors += r.Visitors
		total.Conversations += r.Conversations
		total.Messages += r.Messages
	}
	if s.hub != nil {
		for _, vid := range s.hub.OnlineVisitorIDs() {
			s.resetOnlineVisitor(ctx, vid)
		}
		s.hub.BroadcastToAllAgents(&ws.Envelope{Type: "conversations_deleted", ID: uuid.NewString(), TS: ws.NowMS(),
			Extra: map[string]any{"all": true}})
	}
	return total, nil
}

// resetOnlineVisitor 在线访客：通知访客端清空界面与本地缓存（离线访客下次打开时按 history_reset_at 清空）。
// 不立即新建会话：否则在线访客会以空会话立刻重新出现在列表里；访客再发消息时由消息流水线自动开新会话。
func (s *Service) resetOnlineVisitor(_ context.Context, visitorID string) {
	if s.hub == nil {
		return
	}
	if s.hub.RebindVisitor(visitorID, "", true) {
		s.bizLog.Info("visitor_history_cleared_notified", zap.String("vid", visitorID))
	}
}

// RestoreVisitors 从回收站恢复，并通知客服刷新列表。
func (s *Service) RestoreVisitors(ctx context.Context, visitorIDs []string) (int, int, error) {
	v, c, err := s.store.RestoreVisitors(ctx, visitorIDs)
	if err == nil && c > 0 && s.hub != nil {
		s.hub.BroadcastToAllAgents(&ws.Envelope{Type: "profile_updated", ID: uuid.NewString(), TS: ws.NowMS(),
			Extra: map[string]any{"restored": true}})
	}
	return v, c, err
}

// PurgeVisitors 彻底删除回收站中指定客户（all=true 时清空回收站）的数据与磁盘文件。
func (s *Service) PurgeVisitors(ctx context.Context, visitorIDs []string, all bool) (store.PurgeResult, error) {
	var total store.PurgeResult
	if len(visitorIDs) == 0 && !all {
		return total, nil
	}
	for i := 0; i < maxDeleteAllLoops; i++ {
		var ids []string
		var err error
		if all {
			ids, err = s.store.DeletedConversationIDs(ctx, nil, time.Time{}, purgeBatchSize)
		} else {
			ids, err = s.store.DeletedConversationIDs(ctx, visitorIDs, time.Time{}, purgeBatchSize)
		}
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			break
		}
		r, err := s.purgeBatch(ctx, ids)
		if err != nil {
			return total, err
		}
		total.Conversations += r.Conversations
		total.Messages += r.Messages
		total.Files += r.Files
		if r.Conversations == 0 {
			break // 并发恢复导致本批无可删项，结束避免空转
		}
	}
	return total, nil
}

// purgeBatch 彻底删除一批会话，事务提交后再删除磁盘文件（文件删除失败只记日志，不回滚数据库）。
func (s *Service) purgeBatch(ctx context.Context, convIDs []string) (store.PurgeResult, error) {
	r, err := s.store.PurgeConversations(ctx, convIDs)
	if err != nil {
		return r, err
	}
	removed := 0
	for _, key := range r.FileKeys {
		if s.removeUploadedFile(key) {
			removed++
		}
	}
	if len(r.FileKeys) > 0 {
		s.bizLog.Info("purge_files_removed", zap.Int("records", len(r.FileKeys)), zap.Int("removed", removed))
	}
	return r, nil
}

// removeUploadedFile 只删除上传目录内的文件：拒绝绝对路径、路径穿越与目录逃逸。
func (s *Service) removeUploadedFile(storeKey string) bool {
	if s.uploadsDir == "" || storeKey == "" || strings.Contains(storeKey, "..") || strings.ContainsAny(storeKey, "\\\x00") || filepath.IsAbs(storeKey) {
		return false
	}
	base, err := filepath.Abs(s.uploadsDir)
	if err != nil {
		return false
	}
	full := filepath.Clean(filepath.Join(base, filepath.FromSlash(storeKey)))
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		s.secLog.Warn("purge_file_path_rejected", zap.String("key", storeKey))
		return false
	}
	if err := os.Remove(full); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.bizLog.Warn("purge_file_remove_failed", zap.String("key", storeKey), zap.Error(err))
		}
		return false
	}
	return true
}

// RecallMessage 客服撤回自己 2 分钟内的消息，并实时通知会话内访客与所有客服。
func (s *Service) RecallMessage(ctx context.Context, messageID, agentID, actor string) (string, error) {
	convID, err := s.store.RecallMessage(ctx, messageID, agentID, actor, time.Now())
	if err != nil {
		return "", err
	}
	if s.hub != nil {
		s.hub.FanoutToConv(ctx, &ws.Envelope{Type: "recall", ID: messageID, ConvID: convID, From: "agent:" + agentID, TS: ws.NowMS()})
	}
	return convID, nil
}

// SetVisitorBlocked 拉黑 / 解除拉黑，实时更新在线访客连接状态并通知客服刷新列表。
func (s *Service) SetVisitorBlocked(ctx context.Context, visitorID string, blocked bool, reason, actor string, blockIP bool) error {
	ipHash := ""
	if blocked && blockIP {
		h, err := s.store.VisitorIPHash(ctx, visitorID)
		if err != nil {
			return err
		}
		ipHash = h
	}
	released, err := s.store.SetVisitorBlocked(ctx, visitorID, blocked, reason, actor, blockIP, ipHash)
	if err != nil {
		return err
	}
	if s.hub != nil {
		s.hub.SetVisitorBlocked(visitorID, blocked)
		for _, id := range released {
			s.hub.SetVisitorBlocked(id, false)
		}
	}
	if len(released) > 0 {
		s.bizLog.Info("visitor_inherited_block_released", zap.String("source_vid", visitorID), zap.Int("count", len(released)))
	}
	s.NotifyProfileUpdated(visitorID)
	return nil
}

// NotifyProfileUpdated 客户资料（备注/标签/置顶/未读/拉黑）变更后通知所有在线客服刷新。
func (s *Service) NotifyProfileUpdated(visitorID string) {
	if s.hub == nil {
		return
	}
	s.hub.BroadcastToAllAgents(&ws.Envelope{Type: "profile_updated", ID: uuid.NewString(), TS: ws.NowMS(),
		Extra: map[string]any{"visitor_id": visitorID}})
}

// ============ 后台维护任务 ============

// StartMaintenance 启动后台维护：每小时彻底删除回收站中超过 7 天的数据；每天北京时间 04:30 按设置自动清理。
// 多实例部署时借 Redis 限流键保证同一窗口只执行一次。
func (s *Service) StartMaintenance(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.bizLog.Error("maintenance_panic", zap.Any("err", r), zap.Stack("stack"))
			}
		}()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Minute): // 启动后稍等，避开迁移与连接预热高峰
		}
		s.runPurgeExpired(ctx)
		purgeTicker := time.NewTicker(time.Hour)
		defer purgeTicker.Stop()
		cleanTimer := time.NewTimer(time.Until(nextBeijingClock(time.Now(), autoCleanHour, autoCleanMinute)))
		defer cleanTimer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-purgeTicker.C:
				s.runPurgeExpired(ctx)
			case <-cleanTimer.C:
				s.runAutoClean(ctx)
				s.runHallCleanup(ctx)
				cleanTimer.Reset(time.Until(nextBeijingClock(time.Now(), autoCleanHour, autoCleanMinute)))
			}
		}
	}()
}

// nextBeijingClock 下一个北京时间 hh:mm（严格晚于 now）。
func nextBeijingClock(now time.Time, hour, minute int) time.Time {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	n := now.In(loc)
	next := time.Date(n.Year(), n.Month(), n.Day(), hour, minute, 0, 0, loc)
	if !next.After(n) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func (s *Service) acquireJobLock(ctx context.Context, job string, window time.Duration) bool {
	ok, err := s.limiter.AllowAction(ctx, "job_"+job, 1, window, "global")
	if err != nil {
		s.bizLog.Warn("job_lock_unavailable", zap.String("job", job), zap.Error(err))
		return false
	}
	if !ok {
		s.bizLog.Info("job_skipped_recently_run", zap.String("job", job), zap.Duration("window", window))
	}
	return ok
}

func (s *Service) runPurgeExpired(ctx context.Context) {
	if !s.acquireJobLock(ctx, "purge_expired", 50*time.Minute) {
		return
	}
	runID := uuid.NewString()
	start := time.Now()
	s.bizLog.Info("job_purge_expired_start", zap.String("run_id", runID))
	jctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	var total store.PurgeResult
	cutoff := time.Now().Add(-store.RecycleRetention)
	var jobErr error
	for i := 0; i < maxDeleteAllLoops; i++ {
		ids, err := s.store.DeletedConversationIDs(jctx, nil, cutoff, purgeBatchSize)
		if err != nil {
			jobErr = err
			break
		}
		if len(ids) == 0 {
			break
		}
		r, err := s.purgeBatch(jctx, ids)
		if err != nil {
			jobErr = err
			break
		}
		total.Conversations += r.Conversations
		total.Messages += r.Messages
		total.Files += r.Files
		if r.Conversations == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond) // 批间让出 IO，避免压满数据库
	}
	fields := []zap.Field{zap.String("run_id", runID), zap.Int("conversations", total.Conversations),
		zap.Int("messages", total.Messages), zap.Int("files", total.Files), zap.Int64("duration_ms", time.Since(start).Milliseconds())}
	if jobErr != nil {
		s.bizLog.Error("job_purge_expired_failed", append(fields, zap.Error(jobErr))...)
		return
	}
	s.bizLog.Info("job_purge_expired_done", fields...)
	if total.Conversations > 0 {
		s.store.AuditLog(context.Background(), "system", "recycle_purge", "expired",
			fmt.Sprintf("{\"conversations\":%d,\"messages\":%d,\"files\":%d}", total.Conversations, total.Messages, total.Files), "")
	}
}

// runHallCleanup [119] 每日清理 30 天前的大厅消息与大厅附件（数据库记录 + 磁盘文件），分批短事务、批间让出 IO。
func (s *Service) runHallCleanup(ctx context.Context) {
	if !s.acquireJobLock(ctx, "hall_cleanup", 20*time.Hour) {
		return
	}
	runID := uuid.NewString()
	start := time.Now()
	s.bizLog.Info("job_hall_cleanup_start", zap.String("run_id", runID))
	jctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	res, jobErr := s.purgeHallBefore(jctx, s.store, store.HallDefaultRoom, time.Now().Add(-store.HallRetention))
	messages, files, removed := res.Messages, res.FileRecords, res.FilesRemoved
	fields := []zap.Field{zap.String("run_id", runID), zap.Int("messages", messages), zap.Int("file_records", files),
		zap.Int("files_removed", removed), zap.Int64("duration_ms", time.Since(start).Milliseconds())}
	if jobErr != nil {
		s.bizLog.Error("job_hall_cleanup_failed", append(fields, zap.Error(jobErr))...)
	} else {
		s.bizLog.Info("job_hall_cleanup_done", fields...)
	}
	// 失败前已删除的部分同样留审计（数据已不可恢复，必须可追溯）
	if messages > 0 || files > 0 {
		s.store.AuditLog(context.Background(), "system", "hall_cleanup", "retention_days=30",
			fmt.Sprintf(`{"messages":%d,"file_records":%d,"files_removed":%d}`, messages, files, removed), "")
	}
}

// AutoCleanDays 读取自动清理天数（0 = 关闭；合法范围 7-3650）。
func (s *Service) AutoCleanDays(ctx context.Context) int {
	days, err := strconv.Atoi(strings.TrimSpace(s.SettingStr(ctx, "auto_clean_days", "0")))
	if err != nil || days < 7 || days > 3650 {
		return 0
	}
	return days
}

func (s *Service) runAutoClean(ctx context.Context) {
	days := s.AutoCleanDays(ctx)
	if days == 0 || !s.acquireJobLock(ctx, "auto_clean", 20*time.Hour) {
		return
	}
	runID := uuid.NewString()
	start := time.Now()
	s.bizLog.Info("job_auto_clean_start", zap.String("run_id", runID), zap.Int("days", days))
	jctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	olderThan := time.Now().AddDate(0, 0, -days)
	moved := 0
	var jobErr error
	for i := 0; i < maxDeleteAllLoops; i++ {
		n, err := s.store.AutoCleanBatch(jctx, olderThan, "system:auto_clean", time.Now(), 500)
		if err != nil {
			jobErr = err
			break
		}
		moved += n
		if n == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	fields := []zap.Field{zap.String("run_id", runID), zap.Int("days", days), zap.Int("conversations", moved),
		zap.Int64("duration_ms", time.Since(start).Milliseconds())}
	if jobErr != nil {
		s.bizLog.Error("job_auto_clean_failed", append(fields, zap.Error(jobErr))...)
		return
	}
	s.bizLog.Info("job_auto_clean_done", fields...)
	if moved > 0 {
		s.store.AuditLog(context.Background(), "system", "auto_clean", fmt.Sprintf("older_than_days=%d", days),
			fmt.Sprintf("{\"conversations\":%d}", moved), "")
		if s.hub != nil {
			s.hub.BroadcastToAllAgents(&ws.Envelope{Type: "conversations_deleted", ID: uuid.NewString(), TS: ws.NowMS(),
				Extra: map[string]any{"auto_clean": true}})
		}
	}
}
