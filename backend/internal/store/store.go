package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Store 封装所有数据库操作（爷爷铁律：100% 参数化 SQL，杜绝注入）。
// 注意：所有 SQL 都用 ? 占位符 + 显式 Exec/Query；DSN 已关 interpolateParams。

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

// ============ 数据结构（仅服务端用，外部不需要导出） ============

type Visitor struct {
	ID         string
	SiteID     string
	IPCipher   string // 加密的 IP
	UA         string
	Country    string
	City       string
	Referer    string
	LastPage   string
	FirstSeen  time.Time
	LastSeen   time.Time
	Identifier string // 客户自填的姓名/邮箱（可空）
}

type Conversation struct {
	ID         string
	SiteID     string
	VisitorID  string
	AgentID    sql.NullInt64
	Status     string // open / closed
	UnreadV    int
	UnreadA    int
	StartedAt  time.Time
	UpdatedAt  time.Time
	ClosedAt   sql.NullTime
}

type Message struct {
	ID         string
	ConvID     string
	Sender     string // visitor | agent | sys
	SenderRef  string // visitorID 或 agentID
	Content    string
	MediaURL   sql.NullString
	MediaKind  sql.NullString
	MediaName  sql.NullString
	MediaSize  sql.NullInt64
	CreatedAt  time.Time
	DeliveredWS bool // true=已通过 WSS 实时投递；false=未投递（离线落库等待）
}

type Agent struct {
	ID         int64
	Username   string
	PassHash   string
	Role       string // admin | agent
	Nickname   string
	Active     bool
	CreatedAt  time.Time
	LastLogin  sql.NullTime
}

// ============ Visitor ============

func (s *Store) UpsertVisitor(ctx context.Context, v *Visitor) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO visitors(id, site_id, ip_cipher, ua, country, city, referer, last_page, first_seen, last_seen, identifier)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  ip_cipher=VALUES(ip_cipher),
  ua=VALUES(ua),
  country=COALESCE(NULLIF(VALUES(country), ''), country),
  city=COALESCE(NULLIF(VALUES(city), ''), city),
  referer=VALUES(referer),
  last_page=VALUES(last_page),
  last_seen=VALUES(last_seen),
  identifier=COALESCE(NULLIF(VALUES(identifier), ''), identifier)`,
		v.ID, v.SiteID, v.IPCipher, v.UA, v.Country, v.City, v.Referer, v.LastPage,
		v.FirstSeen, v.LastSeen, v.Identifier)
	return err
}

// ============ Conversation ============

func (s *Store) OpenOrGetConversation(ctx context.Context, siteID, visitorID string) (*Conversation, error) {
	c := &Conversation{}
	err := s.db.QueryRowContext(ctx, `
SELECT id, site_id, visitor_id, agent_id, status, unread_visitor, unread_agent, started_at, updated_at, closed_at
FROM conversations
WHERE visitor_id=? AND site_id=? AND status='open'
ORDER BY started_at DESC LIMIT 1`, visitorID, siteID).Scan(
		&c.ID, &c.SiteID, &c.VisitorID, &c.AgentID, &c.Status, &c.UnreadV, &c.UnreadA,
		&c.StartedAt, &c.UpdatedAt, &c.ClosedAt)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// 新建
	now := time.Now()
	c = &Conversation{
		ID:        uuid.NewString(),
		SiteID:    siteID,
		VisitorID: visitorID,
		Status:    "open",
		StartedAt: now,
		UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO conversations(id, site_id, visitor_id, status, unread_visitor, unread_agent, started_at, updated_at)
VALUES(?, ?, ?, 'open', 0, 0, ?, ?)`,
		c.ID, c.SiteID, c.VisitorID, c.StartedAt, c.UpdatedAt)
	return c, err
}

func (s *Store) AssignAgent(ctx context.Context, convID string, agentID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET agent_id=?, updated_at=? WHERE id=? AND status='open'`,
		agentID, time.Now(), convID)
	return err
}

func (s *Store) CloseConversation(ctx context.Context, convID string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET status='closed', closed_at=?, updated_at=? WHERE id=?`,
		now, now, convID)
	return err
}

// ListOpenConversations 给客服后台用：当前所有进行中的会话（含访客信息）。
func (s *Store) ListOpenConversations(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.visitor_id, c.agent_id, c.unread_agent, c.started_at, c.updated_at,
       v.identifier, v.country, v.city, v.last_page, v.referer
FROM conversations c
JOIN visitors v ON v.id = c.visitor_id
WHERE c.status='open'
ORDER BY c.updated_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0, limit)
	for rows.Next() {
		var (
			id, vid                       string
			aid                           sql.NullInt64
			unread                        int
			started, updated              time.Time
			ident, country, city, page, ref sql.NullString
		)
		if err := rows.Scan(&id, &vid, &aid, &unread, &started, &updated,
			&ident, &country, &city, &page, &ref); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id":         id,
			"visitor_id": vid,
			"agent_id":   nullInt(aid),
			"unread":     unread,
			"started_at": started,
			"updated_at": updated,
			"identifier": nullStr(ident),
			"country":    nullStr(country),
			"city":       nullStr(city),
			"last_page":  nullStr(page),
			"referer":    nullStr(ref),
		})
	}
	return out, rows.Err()
}

// ============ Message ============

func (s *Store) InsertMessage(ctx context.Context, m *Message) error {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO messages(id, conv_id, sender, sender_ref, content, media_url, media_kind, media_name, media_size, delivered_ws, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ConvID, m.Sender, m.SenderRef, m.Content,
		m.MediaURL, m.MediaKind, m.MediaName, m.MediaSize, m.DeliveredWS, m.CreatedAt)
	if err != nil {
		return err
	}
	// 更新会话 updated_at + 未读
	col := "unread_agent"
	if m.Sender == "agent" {
		col = "unread_visitor"
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE conversations SET updated_at=?, `+col+`=`+col+`+1 WHERE id=?`,
		m.CreatedAt, m.ConvID)
	return err
}

func (s *Store) MarkRead(ctx context.Context, convID, by string) error {
	col := "unread_visitor"
	if by == "agent" {
		col = "unread_agent"
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET `+col+`=0, updated_at=? WHERE id=?`,
		time.Now(), convID)
	return err
}

func (s *Store) ListMessages(ctx context.Context, convID string, beforeID string, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if beforeID == "" {
		rows, err = s.db.QueryContext(ctx, `
SELECT id, conv_id, sender, sender_ref, content, media_url, media_kind, media_name, media_size, delivered_ws, created_at
FROM messages WHERE conv_id=? ORDER BY created_at DESC LIMIT ?`, convID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
SELECT id, conv_id, sender, sender_ref, content, media_url, media_kind, media_name, media_size, delivered_ws, created_at
FROM messages WHERE conv_id=? AND created_at < (SELECT created_at FROM messages WHERE id=?)
ORDER BY created_at DESC LIMIT ?`, convID, beforeID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Message, 0, limit)
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ConvID, &m.Sender, &m.SenderRef, &m.Content,
			&m.MediaURL, &m.MediaKind, &m.MediaName, &m.MediaSize, &m.DeliveredWS, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ============ Agent ============

func (s *Store) GetAgentByUsername(ctx context.Context, username string) (*Agent, error) {
	a := &Agent{}
	err := s.db.QueryRowContext(ctx, `
SELECT id, username, pass_hash, role, nickname, active, created_at, last_login
FROM agents WHERE username=? LIMIT 1`, username).Scan(
		&a.ID, &a.Username, &a.PassHash, &a.Role, &a.Nickname, &a.Active, &a.CreatedAt, &a.LastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (s *Store) CreateAgent(ctx context.Context, a *Agent) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO agents(username, pass_hash, role, nickname, active, created_at)
VALUES(?, ?, ?, ?, 1, ?)`,
		a.Username, a.PassHash, a.Role, a.Nickname, time.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, pass_hash, role, nickname, active, created_at, last_login FROM agents ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.Username, &a.PassHash, &a.Role, &a.Nickname, &a.Active, &a.CreatedAt, &a.LastLogin); err != nil {
			return nil, err
		}
		a.PassHash = "" // 不外泄
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) UpdateAgentLastLogin(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET last_login=? WHERE id=?`, time.Now(), id)
	return err
}

func (s *Store) SetAgentActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET active=? WHERE id=?`, active, id)
	return err
}

func (s *Store) ResetAgentPassword(ctx context.Context, id int64, passHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET pass_hash=? WHERE id=?`, passHash, id)
	return err
}

// ============ File ============

type FileRecord struct {
	ID        string
	ConvID    string
	UploadBy  string // visitor | agent
	UploaderRef string
	Filename  string
	StoreKey  string
	Size      int64
	MIME      string
	CreatedAt time.Time
}

func (s *Store) InsertFile(ctx context.Context, f *FileRecord) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO files(id, conv_id, upload_by, uploader_ref, filename, store_key, size, mime, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.ConvID, f.UploadBy, f.UploaderRef, f.Filename, f.StoreKey, f.Size, f.MIME, f.CreatedAt)
	return err
}

// ============ Audit ============

func (s *Store) AuditLog(ctx context.Context, actor, action, target, detail, ip string) {
	_, _ = s.db.ExecContext(ctx, `
INSERT INTO audit_logs(actor, action, target, detail, ip, created_at)
VALUES(?, ?, ?, ?, ?, ?)`, actor, action, target, detail, ip, time.Now())
}

// ============ helpers ============

func nullStr(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}
func nullInt(v sql.NullInt64) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}
