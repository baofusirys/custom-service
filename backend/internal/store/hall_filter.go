package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// [122] 大厅敏感词屏蔽的拦截记录与告警邮件发件箱（全部参数化 SQL）。

// 告警邮件状态。
const (
	HallMailPending  = "pending"
	HallMailSent     = "sent"
	HallMailFailed   = "failed"
	HallMailDisabled = "disabled"
	HallMailSilenced = "silenced"
)

// HallFilterEvent 一条拦截记录。OriginalEnc 为密文，只由大厅服务解密。
type HallFilterEvent struct {
	ID           int64
	MessageID    string
	Room         string
	Sender       string
	SenderRef    string
	Identity     string
	Name         string
	Categories   string
	OriginalEnc  string
	Masked       string
	MailStatus   string
	MailAttempts int
	MailError    string
	CreatedAt    time.Time
}

const hallFilterColumns = `id, message_id, room, sender, sender_ref, identity, name, categories, original_enc, masked, mail_status, mail_attempts, COALESCE(mail_error, ''), created_at`

func scanHallFilterEvent(sc interface{ Scan(...any) error }) (*HallFilterEvent, error) {
	e := &HallFilterEvent{}
	if err := sc.Scan(&e.ID, &e.MessageID, &e.Room, &e.Sender, &e.SenderRef, &e.Identity, &e.Name, &e.Categories,
		&e.OriginalEnc, &e.Masked, &e.MailStatus, &e.MailAttempts, &e.MailError, &e.CreatedAt); err != nil {
		return nil, err
	}
	return e, nil
}

// InsertHallFilterEvent 写入拦截记录；同一消息重复写入（重试）时忽略。
func (s *Store) InsertHallFilterEvent(ctx context.Context, e *HallFilterEvent) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO hall_filter_events(message_id, room, sender, sender_ref, identity, name, categories, original_enc, masked, mail_status, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.MessageID, e.Room, e.Sender, e.SenderRef, e.Identity, e.Name, e.Categories, e.OriginalEnc, e.Masked, e.MailStatus, e.CreatedAt)
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return nil
	}
	return err
}

// CountPendingHallFilterMail 当前可发送的待发记录数（走 idx_hall_filter_mail）。
func (s *Store) CountPendingHallFilterMail(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM hall_filter_events WHERE mail_status = 'pending' AND (mail_next_at IS NULL OR mail_next_at <= ?)`, now).Scan(&n)
	return n, err
}

// ListPendingHallFilterMail 取一批可发送的待发记录（旧到新）。
func (s *Store) ListPendingHallFilterMail(ctx context.Context, now time.Time, limit int) ([]HallFilterEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+hallFilterColumns+` FROM hall_filter_events
WHERE mail_status = 'pending' AND (mail_next_at IS NULL OR mail_next_at <= ?) ORDER BY id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HallFilterEvent
	for rows.Next() {
		e, err := scanHallFilterEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func idArgs(ids []int64) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	return strings.Join(ph, ","), args
}

// MarkHallFilterMailSent 标记已发送（只改仍为 pending 的记录）。
func (s *Store) MarkHallFilterMailSent(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	ph, args := idArgs(ids)
	_, err := s.db.ExecContext(ctx, `UPDATE hall_filter_events SET mail_status = 'sent', mail_attempts = mail_attempts + 1, mail_sent_at = ?, mail_error = NULL
WHERE mail_status = 'pending' AND id IN (`+ph+`)`, append([]any{at}, args...)...)
	return err
}

// MarkHallFilterMailRetry 发送失败：次数 +1，达到 maxAttempts 记为 failed，否则在 nextAt 重试。
// MySQL 单表 UPDATE 按 SET 顺序求值，mail_status 判断使用的是已加 1 的 mail_attempts。
func (s *Store) MarkHallFilterMailRetry(ctx context.Context, ids []int64, maxAttempts int, nextAt time.Time, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	if len([]rune(reason)) > 200 {
		reason = string([]rune(reason)[:200])
	}
	ph, args := idArgs(ids)
	all := append([]any{maxAttempts, nextAt, reason}, args...)
	_, err := s.db.ExecContext(ctx, `UPDATE hall_filter_events SET mail_attempts = mail_attempts + 1,
mail_status = IF(mail_attempts >= ?, 'failed', 'pending'), mail_next_at = ?, mail_error = ?
WHERE mail_status = 'pending' AND id IN (`+ph+`)`, all...)
	return err
}

// ListHallFilterEvents 管理员查看拦截记录（新到旧，beforeID>0 时向前翻页）。
func (s *Store) ListHallFilterEvents(ctx context.Context, beforeID int64, limit int) ([]HallFilterEvent, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	q := `SELECT ` + hallFilterColumns + ` FROM hall_filter_events`
	args := []any{}
	if beforeID > 0 {
		q += ` WHERE id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HallFilterEvent
	for rows.Next() {
		e, err := scanHallFilterEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// PurgeHallFilterEventsBefore 每日清理：删除早于 before 的拦截记录（分批）。
func (s *Store) PurgeHallFilterEventsBefore(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM hall_filter_events WHERE created_at < ? ORDER BY created_at LIMIT ?`, before, limit)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
