package service

import (
	"context"
	"time"

	"github.com/custom-service/backend/internal/store"
)

// [119] 多人大厅：每日维护任务中的「30 天前大厅消息与大厅附件」清理。
// 调度入口是 features.go 的 runHallCleanup（北京时间 04:30，Redis 任务锁保证多副本只跑一次）；
// 这里只放与调度无关的清理本体，依赖收敛到 hallCleanupStore 接口，便于单元测试。

// hallCleanupStore 大厅清理需要的最小存储能力（*store.Store 实现）。
type hallCleanupStore interface {
	PurgeHallMessagesBefore(ctx context.Context, before time.Time, limit int) (int, error)
	HallFilesBefore(ctx context.Context, room string, before time.Time, limit int) ([]store.HallFileRef, error)
	DeleteFileRecords(ctx context.Context, ids []string) (int, error)
}

const (
	hallPurgeMsgBatch  = 1000 // 单批删除消息条数（短事务，避免长时间锁表）
	hallPurgeFileBatch = 500  // 单批处理附件条数
)

// hallCleanupPause 批间让出数据库与磁盘 IO 的间隔（测试可调小）。
var hallCleanupPause = 50 * time.Millisecond

// hallCleanupResult 一次清理的统计（写入任务日志与审计）。
type hallCleanupResult struct {
	Messages     int // 物理删除的大厅消息条数
	FileRecords  int // 删除的大厅附件数据库记录数
	FilesRemoved int // 实际删除的磁盘文件数（文件已不存在或路径被拒时不计）
}

// purgeHallBefore 分批删除 room 中早于 cutoff 的大厅消息与大厅附件：
//   - 先删消息，再删附件记录，记录删除成功后才删磁盘文件（磁盘删除失败只记日志，不回滚数据库）；
//   - 每批之间让出 IO，并响应 ctx 取消；批次数有上限，防止异常数据导致无限循环；
//   - 磁盘删除复用 removeUploadedFile 的路径校验（拒绝绝对路径、穿越与目录逃逸）。
func (s *Service) purgeHallBefore(ctx context.Context, st hallCleanupStore, room string, cutoff time.Time) (hallCleanupResult, error) {
	var res hallCleanupResult
	for i := 0; i < maxDeleteAllLoops; i++ {
		n, err := st.PurgeHallMessagesBefore(ctx, cutoff, hallPurgeMsgBatch)
		if err != nil {
			return res, err
		}
		res.Messages += n
		if n == 0 {
			break
		}
		if err := pauseCtx(ctx, hallCleanupPause); err != nil {
			return res, err
		}
	}
	for i := 0; i < maxDeleteAllLoops; i++ {
		refs, err := st.HallFilesBefore(ctx, room, cutoff, hallPurgeFileBatch)
		if err != nil {
			return res, err
		}
		if len(refs) == 0 {
			break
		}
		ids := make([]string, 0, len(refs))
		for _, r := range refs {
			ids = append(ids, r.ID)
		}
		n, err := st.DeleteFileRecords(ctx, ids)
		if err != nil {
			return res, err
		}
		res.FileRecords += n
		for _, r := range refs {
			if s.removeUploadedFile(r.StoreKey) {
				res.FilesRemoved++
			}
		}
		if n == 0 {
			break // 并发删除导致本批无可删项，结束避免空转
		}
		if err := pauseCtx(ctx, hallCleanupPause); err != nil {
			return res, err
		}
	}
	return res, nil
}

// pauseCtx 等待 d 或 ctx 结束（任务被取消 / 进程退出时立即返回）。
func pauseCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
