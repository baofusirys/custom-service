package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// QuickReply 快捷回复：AgentID 为空表示团队话术，否则为该客服的个人话术。
type QuickReply struct {
	ID        int64         `json:"id"`
	AgentID   sql.NullInt64 `json:"-"`
	Scope     string        `json:"scope"`
	Title     string        `json:"title"`
	Content   string        `json:"content"`
	SortOrder int           `json:"sort_order"`
	UpdatedAt time.Time     `json:"updated_at"`
}

var ErrQuickReplyNotFound = errors.New("store: quick reply not found")

func scopeOf(agentID sql.NullInt64) string {
	if agentID.Valid {
		return "personal"
	}
	return "team"
}

// ListQuickReplies 团队话术 + 该客服的个人话术（团队在前，按排序值、ID）。
func (s *Store) ListQuickReplies(ctx context.Context, agentID int64) ([]QuickReply, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, agent_id, title, content, sort_order, updated_at FROM quick_replies
WHERE agent_id IS NULL OR agent_id = ?
ORDER BY (agent_id IS NOT NULL), sort_order, id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QuickReply{}
	for rows.Next() {
		var q QuickReply
		if err := rows.Scan(&q.ID, &q.AgentID, &q.Title, &q.Content, &q.SortOrder, &q.UpdatedAt); err != nil {
			return nil, err
		}
		q.Scope = scopeOf(q.AgentID)
		out = append(out, q)
	}
	return out, rows.Err()
}

// CountQuickReplies 统计团队（agentID=nil）或某客服的个人话术数量。
func (s *Store) CountQuickReplies(ctx context.Context, agentID *int64) (int, error) {
	var n int
	var err error
	if agentID == nil {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quick_replies WHERE agent_id IS NULL`).Scan(&n)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quick_replies WHERE agent_id = ?`, *agentID).Scan(&n)
	}
	return n, err
}

func (s *Store) CreateQuickReply(ctx context.Context, agentID *int64, title, content string, sortOrder int, actor string) (int64, error) {
	now := time.Now()
	var aid any
	if agentID != nil {
		aid = *agentID
	}
	r, err := s.db.ExecContext(ctx, `
INSERT INTO quick_replies(agent_id, title, content, sort_order, created_by, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		aid, title, content, sortOrder, actor, now, now)
	if err != nil {
		return 0, mapMySQLError(err)
	}
	return r.LastInsertId()
}

func (s *Store) GetQuickReply(ctx context.Context, id int64) (*QuickReply, error) {
	var q QuickReply
	err := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, title, content, sort_order, updated_at FROM quick_replies WHERE id=?`, id).
		Scan(&q.ID, &q.AgentID, &q.Title, &q.Content, &q.SortOrder, &q.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrQuickReplyNotFound
	}
	if err != nil {
		return nil, err
	}
	q.Scope = scopeOf(q.AgentID)
	return &q, nil
}

func (s *Store) UpdateQuickReply(ctx context.Context, id int64, title, content string, sortOrder int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE quick_replies SET title=?, content=?, sort_order=?, updated_at=? WHERE id=?`,
		title, content, sortOrder, time.Now(), id)
	return mapMySQLError(err)
}

func (s *Store) DeleteQuickReply(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM quick_replies WHERE id=?`, id)
	return err
}
