package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
)

// [119] 多人大厅消息与附件的数据层（全部参数化 SQL）。

const (
	// HallDefaultRoom 当前唯一的大厅房间名（hall_<site_id>）。
	HallDefaultRoom = "hall_default"
	// HallRetention 大厅消息与大厅附件保留期。
	HallRetention = 30 * 24 * time.Hour
	// HallSilencedBy 被拉黑访客的「静默」消息标记：入库但只对发送者本人可见。
	HallSilencedBy = "silenced"
)

// ErrHallMessageConflict 同一消息 ID 被不同发送者或不同内容复用（多端碰撞或伪造重放），必须拒绝。
var ErrHallMessageConflict = errors.New("store: hall message id conflict")

// HallMessage 大厅消息（sender_ref 只在服务端使用，绝不对外返回）。
type HallMessage struct {
	ID        string
	Room      string
	Sender    string // visitor | agent | system
	SenderRef string // 访客 vid / 客服 id
	Identity  string
	Name      string
	Content   string
	MediaURL  sql.NullString
	MediaKind sql.NullString
	MediaName sql.NullString
	MediaSize sql.NullInt64
	CreatedAt time.Time
	DeletedAt sql.NullTime
	DeletedBy string
}

const hallMessageColumns = `id, room, sender, sender_ref, identity, name, content, media_url, media_kind, media_name, media_size, created_at, deleted_at, deleted_by`

func scanHallMessage(sc interface{ Scan(...any) error }) (*HallMessage, error) {
	m := &HallMessage{}
	err := sc.Scan(&m.ID, &m.Room, &m.Sender, &m.SenderRef, &m.Identity, &m.Name, &m.Content,
		&m.MediaURL, &m.MediaKind, &m.MediaName, &m.MediaSize, &m.CreatedAt, &m.DeletedAt, &m.DeletedBy)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// InsertHallMessage 幂等写入：同一 ID 由同一发送者以相同内容重试时返回 (false, nil)，否则冲突报错。
func (s *Store) InsertHallMessage(ctx context.Context, m *HallMessage) (bool, error) {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO hall_messages(id, room, sender, sender_ref, identity, name, content, media_url, media_kind, media_name, media_size, created_at, deleted_at, deleted_by)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Room, m.Sender, m.SenderRef, m.Identity, m.Name, m.Content,
		m.MediaURL, m.MediaKind, m.MediaName, m.MediaSize, m.CreatedAt, m.DeletedAt, m.DeletedBy)
	if err == nil {
		return true, nil
	}
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1062 {
		return false, mapMySQLError(err)
	}
	existing, getErr := s.GetHallMessage(ctx, m.ID)
	if getErr != nil {
		return false, getErr
	}
	if existing != nil && existing.Room == m.Room && existing.Sender == m.Sender && existing.SenderRef == m.SenderRef &&
		existing.Content == m.Content && nullStr(existing.MediaURL) == nullStr(m.MediaURL) {
		return false, nil
	}
	return false, ErrHallMessageConflict
}

// GetHallMessage 按 ID 读取（不存在返回 nil, nil）。
func (s *Store) GetHallMessage(ctx context.Context, id string) (*HallMessage, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+hallMessageColumns+` FROM hall_messages WHERE id=? LIMIT 1`, id)
	m, err := scanHallMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

// HallViewer 历史消息的查看者：访客能看到自己被静默的消息；客服看不到任何静默消息。
type HallViewer struct {
	Sender    string // visitor | agent
	SenderRef string
}

// ListHallMessages 大厅历史（按时间正序返回最近 limit 条；beforeID 非空时只取严格早于该消息的）。
// 走索引 (room, created_at)；beforeID 不存在于该房间时返回空列表。
func (s *Store) ListHallMessages(ctx context.Context, room, beforeID string, limit int, viewer HallViewer) ([]HallMessage, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	args := []any{room, HallSilencedBy, viewer.Sender, viewer.SenderRef}
	cond := ""
	if beforeID != "" {
		var at time.Time
		err := s.db.QueryRowContext(ctx, `SELECT created_at FROM hall_messages WHERE id=? AND room=? LIMIT 1`, beforeID, room).Scan(&at)
		if errors.Is(err, sql.ErrNoRows) {
			return []HallMessage{}, nil
		}
		if err != nil {
			return nil, err
		}
		cond = ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, at, at, beforeID)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+hallMessageColumns+`
FROM hall_messages
WHERE room=? AND (deleted_at IS NULL OR (deleted_by=? AND sender=? AND sender_ref=?))`+cond+`
ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]HallMessage, 0, limit)
	for rows.Next() {
		m, err := scanHallMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// SoftDeleteHallMessage 软删除：found=消息存在于该房间；changed=本次确实由未删除变为已删除（用于决定是否广播）。
func (s *Store) SoftDeleteHallMessage(ctx context.Context, room, id, by string, at time.Time) (found, changed bool, err error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE hall_messages SET deleted_at=?, deleted_by=? WHERE id=? AND room=? AND deleted_at IS NULL`, at, by, id, room)
	if err != nil {
		return false, false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, true, nil
	}
	var one int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM hall_messages WHERE id=? AND room=? LIMIT 1`, id, room).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, false, nil
}

// PurgeHallMessagesBefore 分批物理删除早于 before 的大厅消息（单批 limit 条，短事务，避免长时间锁表）。
func (s *Store) PurgeHallMessagesBefore(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM hall_messages WHERE created_at < ? ORDER BY created_at LIMIT ?`, before, limit)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// HallFileRef 待清理的大厅附件。
type HallFileRef struct {
	ID       string
	StoreKey string
}

// HallFilesBefore 早于 before 的大厅附件（files.conv_id = 房间名，走 idx_conv）。
func (s *Store) HallFilesBefore(ctx context.Context, room string, before time.Time, limit int) ([]HallFileRef, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, store_key FROM files WHERE conv_id=? AND created_at < ? ORDER BY created_at LIMIT ?`, room, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HallFileRef
	for rows.Next() {
		var f HallFileRef
		if err := rows.Scan(&f.ID, &f.StoreKey); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteFileRecords 删除文件记录（调用方在事务成功后再删磁盘文件）。
func (s *Store) DeleteFileRecords(ctx context.Context, ids []string) (int, error) {
	total := 0
	for _, chunk := range chunkStrings(uniqueNonEmpty(ids), 200) {
		ph, args := inPlaceholders(chunk)
		res, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE id IN (`+ph+`)`, args...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// BlockedVisitorIDs 批量查询哪些访客处于拉黑状态（分批 IN，参数化）。
func (s *Store) BlockedVisitorIDs(ctx context.Context, visitorIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, chunk := range chunkStrings(uniqueNonEmpty(visitorIDs), 200) {
		ph, args := inPlaceholders(chunk)
		rows, err := s.db.QueryContext(ctx,
			`SELECT visitor_id FROM visitor_profiles WHERE blocked_at IS NOT NULL AND visitor_id IN (`+ph+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}
