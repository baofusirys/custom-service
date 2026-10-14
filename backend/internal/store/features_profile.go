package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// VisitorProfile 客户资料：备注、标签、置顶、手动未读、拉黑状态。
type VisitorProfile struct {
	VisitorID    string     `json:"visitor_id"`
	Note         string     `json:"note"`
	Tags         []string   `json:"tags"`
	Pinned       bool       `json:"pinned"`
	MarkedUnread bool       `json:"marked_unread"`
	Blocked      bool       `json:"blocked"`
	BlockReason  string     `json:"block_reason"`
	BlockedAt    *time.Time `json:"blocked_at"`
	IPBlocked    bool       `json:"ip_blocked"`
}

// BlockedVisitor 黑名单条目（ip_cipher 由 handler 解密后再返回，store 不碰明文）。
type BlockedVisitor struct {
	VisitorID  string    `json:"visitor_id"`
	Identifier string    `json:"identifier"`
	IPCipher   string    `json:"-"`
	Reason     string    `json:"reason"`
	BlockedBy  string    `json:"blocked_by"`
	BlockedAt  time.Time `json:"blocked_at"`
	IPBlocked  bool      `json:"ip_blocked"`
}

type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// VisitorExists 访客是否存在。
func (s *Store) VisitorExists(ctx context.Context, visitorID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM visitors WHERE id=? LIMIT 1`, visitorID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AgentCanManageVisitor 客服能否管理该客户资料（备注/标签/置顶/拉黑）：客户名下有分配给他或未分配的会话（含回收站中的）。
func (s *Store) AgentCanManageVisitor(ctx context.Context, visitorID, agentID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM conversations WHERE visitor_id=? AND (agent_id=CAST(? AS UNSIGNED) OR agent_id IS NULL) LIMIT 1`,
		visitorID, agentID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ConversationDeleted 会话是否已被移入回收站（不存在也视为已删除）。
func (s *Store) ConversationDeleted(ctx context.Context, convID string) (bool, error) {
	var deleted sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT deleted_at FROM conversations WHERE id=?`, convID).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return deleted.Valid, nil
}

// VisitorHasActiveHistory 访客是否还有未删除的会话。
func (s *Store) VisitorHasActiveHistory(ctx context.Context, visitorID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM conversations WHERE visitor_id=? AND deleted_at IS NULL LIMIT 1`, visitorID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ensureProfile 确保资料行存在（INSERT IGNORE 语义，幂等）。
func ensureProfile(ctx context.Context, ex execer, visitorID, actor string, now time.Time) error {
	_, err := ex.ExecContext(ctx, `
INSERT INTO visitor_profiles(visitor_id, updated_by, updated_at) VALUES(?, ?, ?)
ON DUPLICATE KEY UPDATE visitor_id=visitor_id`, visitorID, actor, now)
	return err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// GetVisitorProfile 读取客户资料（不存在时返回空资料）。
func (s *Store) GetVisitorProfile(ctx context.Context, visitorID string) (*VisitorProfile, error) {
	p := &VisitorProfile{VisitorID: visitorID, Tags: []string{}}
	var pinned, blocked sql.NullTime
	var marked bool
	err := s.db.QueryRowContext(ctx, `
SELECT note, pinned_at, marked_unread, blocked_at, block_reason FROM visitor_profiles WHERE visitor_id=?`,
		visitorID).Scan(&p.Note, &pinned, &marked, &blocked, &p.BlockReason)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	p.Pinned = pinned.Valid
	p.MarkedUnread = marked
	if blocked.Valid {
		p.Blocked = true
		t := blocked.Time
		p.BlockedAt = &t
	}
	tags, err := s.TagsForVisitors(ctx, []string{visitorID})
	if err != nil {
		return nil, err
	}
	if t, ok := tags[visitorID]; ok {
		p.Tags = t
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM blocked_ips WHERE visitor_id=? LIMIT 1`, visitorID).Scan(&n)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	p.IPBlocked = err == nil
	return p, nil
}

// SaveVisitorProfile 全量覆盖备注与标签（同一事务内完成，避免读到半更新状态）。
func (s *Store) SaveVisitorProfile(ctx context.Context, visitorID, note string, tags []string, actor string) error {
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO visitor_profiles(visitor_id, note, updated_by, updated_at) VALUES(?, ?, ?, ?)
ON DUPLICATE KEY UPDATE note=VALUES(note), updated_by=VALUES(updated_by), updated_at=VALUES(updated_at)`,
		visitorID, note, actor, now); err != nil {
		return mapMySQLError(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM visitor_tags WHERE visitor_id=?`, visitorID); err != nil {
		return err
	}
	for i, tag := range tags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO visitor_tags(visitor_id, tag, created_at) VALUES(?, ?, ?)`,
			visitorID, tag, now.Add(time.Duration(i)*time.Millisecond)); err != nil {
			return mapMySQLError(err)
		}
	}
	return tx.Commit()
}

// TagsForVisitors 批量读取标签（按添加顺序）。
func (s *Store) TagsForVisitors(ctx context.Context, visitorIDs []string) (map[string][]string, error) {
	return tagsForVisitors(ctx, s.db, visitorIDs)
}

// tagsForVisitors 批量读取标签（一条 SQL）；q 可以是 *sql.DB 或只读快照事务 *sql.Tx。
func tagsForVisitors(ctx context.Context, q queryer, visitorIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	ids := uniqueNonEmpty(visitorIDs)
	if len(ids) == 0 {
		return out, nil
	}
	ph, args := inPlaceholders(ids)
	rows, err := q.QueryContext(ctx,
		`SELECT visitor_id, tag FROM visitor_tags WHERE visitor_id IN (`+ph+`) ORDER BY created_at, tag`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var vid, tag string
		if err := rows.Scan(&vid, &tag); err != nil {
			return nil, err
		}
		out[vid] = append(out[vid], tag)
	}
	return out, rows.Err()
}

// ListTags 已用标签及使用的客户数（筛选项用）。只统计列表中可见的客户：有未删除会话且未被拉黑。
func (s *Store) ListTags(ctx context.Context, limit int) ([]TagCount, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`
SELECT t.tag, COUNT(*) AS n FROM visitor_tags t
LEFT JOIN visitor_profiles p ON p.visitor_id = t.visitor_id
WHERE p.blocked_at IS NULL
  AND EXISTS (SELECT 1 FROM conversations c WHERE c.visitor_id = t.visitor_id AND c.deleted_at IS NULL)
GROUP BY t.tag ORDER BY n DESC, t.tag ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TagCount{}
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetVisitorPinned 置顶 / 取消置顶。
func (s *Store) SetVisitorPinned(ctx context.Context, visitorID string, pinned bool, actor string) error {
	now := time.Now()
	var at any
	if pinned {
		at = now
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO visitor_profiles(visitor_id, pinned_at, updated_by, updated_at) VALUES(?, ?, ?, ?)
ON DUPLICATE KEY UPDATE pinned_at=VALUES(pinned_at), updated_by=VALUES(updated_by), updated_at=VALUES(updated_at)`,
		visitorID, at, actor, now)
	return err
}

// SetVisitorMarkedUnread 标为未读 / 取消。
func (s *Store) SetVisitorMarkedUnread(ctx context.Context, visitorID string, unread bool, actor string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO visitor_profiles(visitor_id, marked_unread, updated_by, updated_at) VALUES(?, ?, ?, ?)
ON DUPLICATE KEY UPDATE marked_unread=VALUES(marked_unread), updated_by=VALUES(updated_by), updated_at=VALUES(updated_at)`,
		visitorID, unread, actor, now)
	return err
}

// ClearMarkedUnreadByConversation 客服打开会话标记已读时，清除该客户的手动未读。返回是否有变化。
func (s *Store) ClearMarkedUnreadByConversation(ctx context.Context, convID string) (string, bool, error) {
	var vid string
	if err := s.db.QueryRowContext(ctx, `SELECT visitor_id FROM conversations WHERE id=?`, convID).Scan(&vid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE visitor_profiles SET marked_unread=0 WHERE visitor_id=? AND marked_unread=1`, vid)
	if err != nil {
		return vid, false, err
	}
	n, _ := res.RowsAffected()
	return vid, n > 0, nil
}

// SetVisitorBlocked 拉黑 / 解除拉黑。blockIP 且 ipHash 非空时同时拉黑 IP；
// 解除时一并删除该访客产生的 IP 拉黑，并解除因该 IP 自动继承拉黑（blocked_by='system'）的其他访客。
// 返回被一并解除拉黑的其他访客 ID（供实时连接状态同步）。
func (s *Store) SetVisitorBlocked(ctx context.Context, visitorID string, blocked bool, reason, actor string, blockIP bool, ipHash string) ([]string, error) {
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var released []string
	if blocked {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO visitor_profiles(visitor_id, blocked_at, blocked_by, block_reason, updated_by, updated_at) VALUES(?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE blocked_at=VALUES(blocked_at), blocked_by=VALUES(blocked_by), block_reason=VALUES(block_reason),
  updated_by=VALUES(updated_by), updated_at=VALUES(updated_at)`,
			visitorID, now, actor, reason, actor, now); err != nil {
			return nil, mapMySQLError(err)
		}
		if blockIP && ipHash != "" {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO blocked_ips(ip_hash, visitor_id, blocked_by, created_at) VALUES(?, ?, ?, ?)
ON DUPLICATE KEY UPDATE visitor_id=VALUES(visitor_id), blocked_by=VALUES(blocked_by), created_at=VALUES(created_at)`,
				ipHash, visitorID, actor, now); err != nil {
				return nil, err
			}
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
UPDATE visitor_profiles SET blocked_at=NULL, blocked_by='', block_reason='', updated_by=?, updated_at=? WHERE visitor_id=?`,
			actor, now, visitorID); err != nil {
			return nil, err
		}
		// 找出因本访客拉黑的 IP 而自动继承拉黑的其他访客，一并解除（仍被其他 IP 规则命中的不受影响：继续由 IsVisitorBlocked 判定）
		rows, err := tx.QueryContext(ctx, `
SELECT p.visitor_id FROM visitor_profiles p
JOIN visitors v ON v.id = p.visitor_id
JOIN blocked_ips b ON b.ip_hash = v.ip_hash AND b.visitor_id = ?
WHERE p.blocked_by = 'system' AND p.blocked_at IS NOT NULL AND p.visitor_id <> ?
FOR UPDATE`, visitorID, visitorID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			released = append(released, id)
		}
		rows.Close()
		for _, chunk := range chunkStrings(released, 200) {
			ph, args := inPlaceholders(chunk)
			args = append([]any{actor, now}, args...)
			if _, err := tx.ExecContext(ctx, `
UPDATE visitor_profiles SET blocked_at=NULL, blocked_by='', block_reason='', updated_by=?, updated_at=?
WHERE blocked_by='system' AND visitor_id IN (`+ph+`)`, args...); err != nil {
				return nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM blocked_ips WHERE visitor_id=?`, visitorID); err != nil {
			return nil, err
		}
	}
	return released, tx.Commit()
}

// IsVisitorBlocked 访客是否被拉黑（本人被拉黑，或打开客服窗口时因 IP 黑名单自动继承拉黑）。
// IP 黑名单只通过「继承」生效：继承会写入资料表，黑名单页可见、可单独解除，避免同 IP 的正常访客被无提示地隐藏。
func (s *Store) IsVisitorBlocked(ctx context.Context, visitorID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT 1 FROM visitor_profiles WHERE visitor_id = ? AND blocked_at IS NOT NULL LIMIT 1`, visitorID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// InheritIPBlock 新访客来自已拉黑 IP 时自动继承拉黑（让 IP 拉黑对清空本地存储换身份的访客同样有效）。
func (s *Store) InheritIPBlock(ctx context.Context, visitorID, ipHash string) (bool, error) {
	if ipHash == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM blocked_ips WHERE ip_hash=? LIMIT 1`, ipHash).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO visitor_profiles(visitor_id, blocked_at, blocked_by, block_reason, updated_by, updated_at)
VALUES(?, ?, 'system', '同 IP 已被拉黑', 'system', ?)
ON DUPLICATE KEY UPDATE blocked_at=IF(blocked_at IS NULL, VALUES(blocked_at), blocked_at),
  blocked_by=IF(blocked_at IS NULL, VALUES(blocked_by), blocked_by),
  block_reason=IF(blocked_at IS NULL, VALUES(block_reason), block_reason)`, visitorID, now, now)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

// VisitorIPHash 读取访客当前 IP 哈希。
func (s *Store) VisitorIPHash(ctx context.Context, visitorID string) (string, error) {
	var h sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT ip_hash FROM visitors WHERE id=?`, visitorID).Scan(&h)
	if err != nil {
		return "", err
	}
	return h.String, nil
}

// ListBlockedVisitors 黑名单（按拉黑时间倒序）。
func (s *Store) ListBlockedVisitors(ctx context.Context, limit, offset int) ([]BlockedVisitor, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM visitor_profiles WHERE blocked_at IS NOT NULL`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT p.visitor_id, COALESCE(v.identifier, ''), COALESCE(v.ip_cipher, ''), p.block_reason, p.blocked_by, p.blocked_at,
       EXISTS(SELECT 1 FROM blocked_ips b WHERE b.visitor_id = p.visitor_id) AS ip_blocked
FROM visitor_profiles p
LEFT JOIN visitors v ON v.id = p.visitor_id
WHERE p.blocked_at IS NOT NULL
ORDER BY p.blocked_at DESC
LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []BlockedVisitor{}
	for rows.Next() {
		var b BlockedVisitor
		if err := rows.Scan(&b.VisitorID, &b.Identifier, &b.IPCipher, &b.Reason, &b.BlockedBy, &b.BlockedAt, &b.IPBlocked); err != nil {
			return nil, 0, err
		}
		out = append(out, b)
	}
	return out, total, rows.Err()
}
