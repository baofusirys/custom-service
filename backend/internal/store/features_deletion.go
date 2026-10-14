package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RecycleRetention 回收站保留期：到期后由后台任务彻底删除（含上传文件）。
const RecycleRetention = 7 * 24 * time.Hour

type DeleteResult struct {
	Visitors      int `json:"visitors"`
	Conversations int `json:"conversations"`
	Messages      int `json:"messages"`
	Skipped       int `json:"skipped"`
}

type PurgeResult struct {
	Conversations int      `json:"conversations"`
	Messages      int      `json:"messages"`
	Files         int      `json:"files"`
	FileKeys      []string `json:"-"`
}

type RecycleItem struct {
	VisitorID     string    `json:"visitor_id"`
	Identifier    string    `json:"identifier"`
	IPCipher      string    `json:"-"`
	Conversations int       `json:"conversations"`
	Messages      int       `json:"messages"`
	DeletedAt     time.Time `json:"deleted_at"`
	DeletedBy     string    `json:"deleted_by"`
	PurgeAt       time.Time `json:"purge_at"`
}

// SoftDeleteVisitors 把一批客户（≤200）的全部未删除会话移入回收站，并记录访客端历史清空时间。
// 单事务完成：计数、标记删除、关闭会话、写 history_reset_at，任何一步失败整体回滚。
func (s *Store) SoftDeleteVisitors(ctx context.Context, visitorIDs []string, actor string, now time.Time) (DeleteResult, error) {
	var res DeleteResult
	ids := uniqueNonEmpty(visitorIDs)
	if len(ids) == 0 {
		return res, nil
	}
	ph, args := inPlaceholders(ids)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT visitor_id), COUNT(*) FROM conversations WHERE visitor_id IN (`+ph+`) AND deleted_at IS NULL`, args...).
		Scan(&res.Visitors, &res.Conversations); err != nil {
		return res, err
	}
	if res.Conversations == 0 {
		return res, nil
	}
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM messages m JOIN conversations c ON c.id = m.conv_id
WHERE c.visitor_id IN (`+ph+`) AND c.deleted_at IS NULL`, args...).Scan(&res.Messages); err != nil {
		return res, err
	}
	updArgs := append([]any{now, actor, now}, args...)
	if _, err := tx.ExecContext(ctx, `
UPDATE conversations SET deleted_at=?, deleted_by=?, status='closed', closed_at=COALESCE(closed_at, ?)
WHERE visitor_id IN (`+ph+`) AND deleted_at IS NULL`, updArgs...); err != nil {
		return res, err
	}
	for _, vid := range ids {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO visitor_profiles(visitor_id, history_reset_at, updated_by, updated_at) VALUES(?, ?, ?, ?)
ON DUPLICATE KEY UPDATE history_reset_at=VALUES(history_reset_at), updated_by=VALUES(updated_by), updated_at=VALUES(updated_at)`,
			vid, now, actor, now); err != nil {
			return res, err
		}
	}
	return res, tx.Commit()
}

// NextActiveVisitorBatch 取一批仍有未删除会话的客户 ID（删除全部时分批处理，避免长事务锁表）。
func (s *Store) NextActiveVisitorBatch(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT visitor_id FROM conversations WHERE deleted_at IS NULL LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListRecycleBin 回收站（按客户聚合，按删除时间倒序）。purge_at 取该客户最早一段会话的到期时间。
func (s *Store) ListRecycleBin(ctx context.Context, limit, offset int) ([]RecycleItem, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT visitor_id) FROM conversations WHERE deleted_at IS NOT NULL`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT g.visitor_id, COALESCE(v.identifier, ''), COALESCE(v.ip_cipher, ''), g.convs, g.last_deleted, g.first_deleted,
       COALESCE((SELECT c2.deleted_by FROM conversations c2 WHERE c2.visitor_id = g.visitor_id AND c2.deleted_at IS NOT NULL
                 ORDER BY c2.deleted_at DESC LIMIT 1), ''),
       (SELECT COUNT(*) FROM messages m JOIN conversations c3 ON c3.id = m.conv_id
        WHERE c3.visitor_id = g.visitor_id AND c3.deleted_at IS NOT NULL)
FROM (
  SELECT visitor_id, COUNT(*) AS convs, MAX(deleted_at) AS last_deleted, MIN(deleted_at) AS first_deleted
  FROM conversations WHERE deleted_at IS NOT NULL GROUP BY visitor_id
) g
LEFT JOIN visitors v ON v.id = g.visitor_id
ORDER BY g.last_deleted DESC, g.visitor_id
LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []RecycleItem{}
	for rows.Next() {
		var it RecycleItem
		var first time.Time
		if err := rows.Scan(&it.VisitorID, &it.Identifier, &it.IPCipher, &it.Conversations, &it.DeletedAt, &first, &it.DeletedBy, &it.Messages); err != nil {
			return nil, 0, err
		}
		it.PurgeAt = first.Add(RecycleRetention)
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// RestoreVisitors 从回收站恢复一批客户的全部已删除会话。
func (s *Store) RestoreVisitors(ctx context.Context, visitorIDs []string) (visitors, conversations int, err error) {
	ids := uniqueNonEmpty(visitorIDs)
	if len(ids) == 0 {
		return 0, 0, nil
	}
	ph, args := inPlaceholders(ids)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT visitor_id) FROM conversations WHERE visitor_id IN (`+ph+`) AND deleted_at IS NOT NULL`, args...).
		Scan(&visitors); err != nil {
		return 0, 0, err
	}
	r, err := tx.ExecContext(ctx,
		`UPDATE conversations SET deleted_at=NULL, deleted_by='' WHERE visitor_id IN (`+ph+`) AND deleted_at IS NOT NULL`, args...)
	if err != nil {
		return 0, 0, err
	}
	n, _ := r.RowsAffected()
	return visitors, int(n), tx.Commit()
}

// DeletedConversationIDs 回收站中会话 ID：按客户（visitorIDs 非空）或按到期时间（before 非零）筛选，最多 limit 条。
func (s *Store) DeletedConversationIDs(ctx context.Context, visitorIDs []string, before time.Time, limit int) ([]string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case len(visitorIDs) > 0:
		ph, args := inPlaceholders(uniqueNonEmpty(visitorIDs))
		args = append(args, limit)
		rows, err = s.db.QueryContext(ctx,
			`SELECT id FROM conversations WHERE visitor_id IN (`+ph+`) AND deleted_at IS NOT NULL LIMIT ?`, args...)
	case !before.IsZero():
		rows, err = s.db.QueryContext(ctx,
			`SELECT id FROM conversations WHERE deleted_at IS NOT NULL AND deleted_at < ? LIMIT ?`, before, limit)
	default:
		rows, err = s.db.QueryContext(ctx,
			`SELECT id FROM conversations WHERE deleted_at IS NOT NULL LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PurgeConversations 彻底删除一批（≤200）已在回收站中的会话及其消息、文件记录；返回待删除的磁盘文件键。
// 只删除 deleted_at 非空的会话，防止误删正常会话。
func (s *Store) PurgeConversations(ctx context.Context, convIDs []string) (PurgeResult, error) {
	var res PurgeResult
	ids := uniqueNonEmpty(convIDs)
	if len(ids) == 0 {
		return res, nil
	}
	ph, args := inPlaceholders(ids)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	// 重新确认确实在回收站中（并发恢复的会话不得被删除）
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM conversations WHERE id IN (`+ph+`) AND deleted_at IS NOT NULL FOR UPDATE`, args...)
	if err != nil {
		return res, err
	}
	confirmed := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, err
		}
		confirmed = append(confirmed, id)
	}
	rows.Close()
	if len(confirmed) == 0 {
		return res, nil
	}
	ph, args = inPlaceholders(confirmed)
	keyRows, err := tx.QueryContext(ctx, `SELECT store_key FROM files WHERE conv_id IN (`+ph+`)`, args...)
	if err != nil {
		return res, err
	}
	for keyRows.Next() {
		var k string
		if err := keyRows.Scan(&k); err != nil {
			keyRows.Close()
			return res, err
		}
		res.FileKeys = append(res.FileKeys, k)
	}
	keyRows.Close()
	r, err := tx.ExecContext(ctx, `DELETE FROM files WHERE conv_id IN (`+ph+`)`, args...)
	if err != nil {
		return res, err
	}
	n, _ := r.RowsAffected()
	res.Files = int(n)
	r, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE conv_id IN (`+ph+`)`, args...)
	if err != nil {
		return res, err
	}
	n, _ = r.RowsAffected()
	res.Messages = int(n)
	r, err = tx.ExecContext(ctx, `DELETE FROM conversations WHERE id IN (`+ph+`) AND deleted_at IS NOT NULL`, args...)
	if err != nil {
		return res, err
	}
	n, _ = r.RowsAffected()
	res.Conversations = int(n)
	return res, tx.Commit()
}

// AutoCleanBatch 自动清理：把最近活动早于 olderThan 的未删除会话移入回收站（置顶客户除外），每批最多 limit 条。
func (s *Store) AutoCleanBatch(ctx context.Context, olderThan time.Time, actor string, now time.Time, limit int) (int, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id FROM conversations c
LEFT JOIN visitor_profiles p ON p.visitor_id = c.visitor_id
WHERE c.deleted_at IS NULL AND c.updated_at < ? AND p.pinned_at IS NULL
LIMIT ?`, olderThan, limit)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return 0, nil
	}
	ph, args := inPlaceholders(ids)
	updArgs := append([]any{now, actor, now}, args...)
	r, err := s.db.ExecContext(ctx, `
UPDATE conversations SET deleted_at=?, deleted_by=?, status='closed', closed_at=COALESCE(closed_at, ?)
WHERE id IN (`+ph+`) AND deleted_at IS NULL`, updArgs...)
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	return int(n), nil
}

// HistoryResetAt 访客端历史最近一次被清空的时间（零值表示从未清空）。
func (s *Store) HistoryResetAt(ctx context.Context, visitorID string) (time.Time, error) {
	var t sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT history_reset_at FROM visitor_profiles WHERE visitor_id=?`, visitorID).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) || !t.Valid {
		return time.Time{}, nil
	}
	return t.Time, err
}
