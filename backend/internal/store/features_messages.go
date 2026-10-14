package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// RecallWindow 客服可撤回自己消息的时限。
const RecallWindow = 2 * time.Minute

var (
	ErrRecallNotFound  = errors.New("store: message not found")
	ErrRecallForbidden = errors.New("store: not your message")
	ErrRecallExpired   = errors.New("store: recall window expired")
	ErrRecallAlready   = errors.New("store: message already recalled")
)

type SearchHit struct {
	MessageID  string    `json:"message_id"`
	ConvID     string    `json:"conv_id"`
	VisitorID  string    `json:"visitor_id"`
	Identifier string    `json:"identifier"`
	Sender     string    `json:"sender"`
	Content    string    `json:"-"`
	Snippet    string    `json:"snippet"`
	CreatedAt  time.Time `json:"created_at"`
}

type ExportRow struct {
	VisitorID  string
	Identifier string
	Sender     string
	Content    string
	MediaKind  string
	MediaName  string
	Recalled   bool
	CreatedAt  time.Time
}

// escapeLike 转义 LIKE 通配符（配合 ESCAPE '!'），让用户输入的 % _ 按字面匹配。
func escapeLike(q string) string {
	r := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return r.Replace(q)
}

// SearchMessages 全局搜索消息内容。agentID 非空时只搜该客服可访问的会话（未分配或已分配给他）；管理员传空。
// 排除已删除会话、已撤回消息、浏览页面记录；按时间倒序。
func (s *Store) SearchMessages(ctx context.Context, q, agentID string, limit int) ([]SearchHit, error) {
	if limit <= 0 || limit > 50 {
		limit = 30
	}
	pattern := "%" + escapeLike(q) + "%"
	query := `
SELECT m.id, m.conv_id, c.visitor_id, COALESCE(v.identifier, ''), m.sender, m.content, m.created_at
FROM messages m
JOIN conversations c ON c.id = m.conv_id
LEFT JOIN visitors v ON v.id = c.visitor_id
WHERE c.deleted_at IS NULL AND m.recalled_at IS NULL
  AND m.content LIKE ? ESCAPE '!'
  AND NOT (m.sender = 'sys' AND m.sender_ref LIKE 'page:%')`
	args := []any{pattern}
	if agentID != "" {
		query += ` AND (c.agent_id = CAST(? AS UNSIGNED) OR c.agent_id IS NULL)`
		args = append(args, agentID)
	}
	query += ` ORDER BY m.created_at DESC, m.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchHit{}
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.MessageID, &h.ConvID, &h.VisitorID, &h.Identifier, &h.Sender, &h.Content, &h.CreatedAt); err != nil {
			return nil, err
		}
		h.Snippet = Snippet(h.Content, q, 24)
		out = append(out, h)
	}
	return out, rows.Err()
}

// Snippet 以关键词为中心截取前后各 radius 个字符（按字符而非字节，中文安全）。
func Snippet(content, q string, radius int) string {
	runes := []rune(content)
	if len(runes) == 0 {
		return ""
	}
	idx := strings.Index(strings.ToLower(content), strings.ToLower(q))
	center := 0
	if idx >= 0 {
		center = len([]rune(content[:idx]))
	}
	start := center - radius
	if start < 0 {
		start = 0
	}
	end := center + len([]rune(q)) + radius
	if end > len(runes) {
		end = len(runes)
	}
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

// RecallMessage 客服撤回自己发送、未超时、未撤回的消息；返回所属会话 ID。行锁防并发重复撤回。
func (s *Store) RecallMessage(ctx context.Context, messageID, agentID, actor string, now time.Time) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var convID, sender, senderRef string
	var createdAt time.Time
	var recalledAt, deletedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
SELECT m.conv_id, m.sender, m.sender_ref, m.created_at, m.recalled_at, c.deleted_at
FROM messages m JOIN conversations c ON c.id = m.conv_id
WHERE m.id = ? FOR UPDATE`, messageID).Scan(&convID, &sender, &senderRef, &createdAt, &recalledAt, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrRecallNotFound
	}
	if err != nil {
		return "", err
	}
	if deletedAt.Valid {
		return "", ErrRecallNotFound
	}
	if sender != "agent" || senderRef != agentID {
		return "", ErrRecallForbidden
	}
	if recalledAt.Valid {
		return "", ErrRecallAlready
	}
	if now.Sub(createdAt) > RecallWindow {
		return "", ErrRecallExpired
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET recalled_at=?, recalled_by=? WHERE id=?`, now, actor, messageID); err != nil {
		return "", err
	}
	return convID, tx.Commit()
}

// RecentRecalledIDs 访客近期被撤回的消息 ID（访客端离线期间的撤回据此改写本地缓存）。
func (s *Store) RecentRecalledIDs(ctx context.Context, visitorID string, since time.Time, limit int) ([]string, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id FROM messages m JOIN conversations c ON c.id = m.conv_id
WHERE c.visitor_id = ? AND c.deleted_at IS NULL AND m.recalled_at IS NOT NULL AND m.recalled_at > ?
ORDER BY m.recalled_at DESC LIMIT ?`, visitorID, since, limit)
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

// ExportMessages 导出一批客户的聊天记录（未删除会话、不含浏览页面记录），按客户、时间排序，最多 limit 条。
func (s *Store) ExportMessages(ctx context.Context, visitorIDs []string, limit int) ([]ExportRow, error) {
	ids := uniqueNonEmpty(visitorIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 50000 {
		limit = 50000
	}
	ph, args := inPlaceholders(ids)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT c.visitor_id, COALESCE(v.identifier, ''), m.sender, m.content, COALESCE(m.media_kind, ''), COALESCE(m.media_name, ''),
       m.recalled_at IS NOT NULL, m.created_at
FROM messages m
JOIN conversations c ON c.id = m.conv_id
LEFT JOIN visitors v ON v.id = c.visitor_id
WHERE c.visitor_id IN (`+ph+`) AND c.deleted_at IS NULL
  AND NOT (m.sender = 'sys' AND m.sender_ref LIKE 'page:%')
ORDER BY c.visitor_id, m.created_at, m.id
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExportRow{}
	for rows.Next() {
		var r ExportRow
		if err := rows.Scan(&r.VisitorID, &r.Identifier, &r.Sender, &r.Content, &r.MediaKind, &r.MediaName, &r.Recalled, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
