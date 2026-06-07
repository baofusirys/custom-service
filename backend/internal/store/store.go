package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// Store 封装所有数据库操作（爷爷铁律：100% 参数化 SQL，杜绝注入）。
// 注意：所有 SQL 都用 ? 占位符 + 显式 Exec/Query；DSN 已关 interpolateParams。

// ============ [052] 业务语义错误（handler 层用 errors.Is 判断分支返不同 HTTP code） ============
// 这些 sentinel error 配合 mapMySQLError 把 driver 层裸 *mysql.MySQLError 归类成业务概念，
// 让 handler 不再写 if strings.Contains(err.Error(), "Duplicate") 这种脆弱判断
var (
	// 唯一键冲突（agents.username / 未来其他 UNIQUE KEY 都用这个，handler 按字段名给文案）
	ErrDuplicateUsername = errors.New("store: duplicate username")
	// 字段长度超过 schema 定义（理论上 handler 入参校验应已拦截，这是兜底）
	ErrFieldTooLong = errors.New("store: field value too long")
)

// mapMySQLError 把 driver 层原始 *mysql.MySQLError 归类成业务 sentinel error。
// 未识别的 MySQL 错误 / 非 MySQL 错误（context cancel / driver bad conn 等）原样返回，
// 由 handler 层根据 errors.Is 兜底成 500。
func mapMySQLError(err error) error {
	if err == nil {
		return nil
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1062: // Duplicate entry for UNIQUE KEY
			return ErrDuplicateUsername
		case 1406: // Data too long for column
			return ErrFieldTooLong
		}
	}
	return err
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

// ============ 数据结构（仅服务端用，外部不需要导出） ============

// 注意：所有结构体字段都带 JSON tag —— Go 默认导出大写字段名，
// 但前端用小写 snake_case 读，必须显式指定，否则消息内容、发送者等都读不到。

type Visitor struct {
	ID         string    `json:"id"`
	SiteID     string    `json:"site_id"`
	IPCipher   string    `json:"-"` // 永不外泄
	IPHash     string    `json:"-"` // [055] HMAC-SHA256(IP) 不可逆但可索引，供 RelatedVisitorsByIPHash 查
	UA         string    `json:"ua"`
	Country    string    `json:"country"`
	City       string    `json:"city"`
	Referer    string    `json:"referer"`
	LastPage   string    `json:"last_page"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	Identifier string    `json:"identifier"`
}

type Conversation struct {
	ID        string        `json:"id"`
	SiteID    string        `json:"site_id"`
	VisitorID string        `json:"visitor_id"`
	AgentID   sql.NullInt64 `json:"agent_id"`
	Status    string        `json:"status"`
	UnreadV   int           `json:"unread_visitor"`
	UnreadA   int           `json:"unread_agent"`
	StartedAt time.Time     `json:"started_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	ClosedAt  sql.NullTime  `json:"closed_at"`
}

type Message struct {
	ID          string         `json:"id"`
	ConvID      string         `json:"conv_id"`
	Sender      string         `json:"sender"` // visitor | agent | sys
	SenderRef   string         `json:"sender_ref"`
	Content     string         `json:"content"`
	MediaURL    sql.NullString `json:"media_url,omitempty"`
	MediaKind   sql.NullString `json:"media_kind,omitempty"`
	MediaName   sql.NullString `json:"media_name,omitempty"`
	MediaSize   sql.NullInt64  `json:"media_size,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	DeliveredWS bool           `json:"delivered_ws"`
	// Read 由 ListMessages 计算填充：自己（sender）的消息是否已被对端读过
	// （比较 created_at vs 对端的 last_read_*_at）
	Read bool `json:"read"`
}

type Agent struct {
	ID        int64        `json:"id"`
	Username  string       `json:"username"`
	PassHash  string       `json:"-"` // 永不外泄
	Role      string       `json:"role"`
	Nickname  string       `json:"nickname"`
	Active    bool         `json:"active"`
	CreatedAt time.Time    `json:"created_at"`
	LastLogin sql.NullTime `json:"last_login"`
}

// ============ Visitor ============

func (s *Store) UpsertVisitor(ctx context.Context, v *Visitor) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	// [055] 同时写 ip_cipher（看明文用）和 ip_hash（建索引查关联访客用）
	_, err := s.db.ExecContext(ctx, `
INSERT INTO visitors(id, site_id, ip_cipher, ip_hash, ua, country, city, referer, last_page, first_seen, last_seen, identifier)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  ip_cipher=VALUES(ip_cipher),
  ip_hash=COALESCE(NULLIF(VALUES(ip_hash), ''), ip_hash),
  ua=VALUES(ua),
  country=COALESCE(NULLIF(VALUES(country), ''), country),
  city=COALESCE(NULLIF(VALUES(city), ''), city),
  referer=VALUES(referer),
  last_page=VALUES(last_page),
  last_seen=VALUES(last_seen),
  identifier=COALESCE(NULLIF(VALUES(identifier), ''), identifier)`,
		v.ID, v.SiteID, v.IPCipher, v.IPHash, v.UA, v.Country, v.City, v.Referer, v.LastPage,
		v.FirstSeen, v.LastSeen, v.Identifier)
	return err
}

// [055] 关联访客：查 30 天内同 IP（ip_hash 相等）出现的其他 vid，按 last_seen 倒序。
// 业务用法：客服端访客详情页「关联访客 (N)」面板，给客服参考"疑似同一人"。
// 不强行合并 vid（vid 仍是浏览器维度），仅 UI 层提示。
// 排除自己的 vid；ip_hash 为空（旧数据 / 本地开发空 IP）不返回避免误关联。
func (s *Store) RelatedVisitorsByIPHash(ctx context.Context, ipHash, excludeVID string, days, limit int) ([]Visitor, error) {
	if ipHash == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	if days <= 0 {
		days = 30
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, site_id, ip_cipher, ip_hash, ua, country, city, referer, last_page,
       first_seen, last_seen, identifier
FROM visitors
WHERE ip_hash = ? AND id != ?
  AND last_seen > DATE_SUB(NOW(), INTERVAL ? DAY)
ORDER BY last_seen DESC
LIMIT ?`, ipHash, excludeVID, days, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Visitor
	for rows.Next() {
		var v Visitor
		if err := rows.Scan(&v.ID, &v.SiteID, &v.IPCipher, &v.IPHash, &v.UA, &v.Country, &v.City,
			&v.Referer, &v.LastPage, &v.FirstSeen, &v.LastSeen, &v.Identifier); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// GetVisitor 按 ID 读取访客。主要用于拿数据库里真实的 first_seen（UpsertVisitor
// 的 ON DUPLICATE KEY UPDATE 不更新 first_seen，所以调用方传的 v.FirstSeen 是 now，
// 不可靠；要判断"真新 vs 回访"必须重新查 DB）。
func (s *Store) GetVisitor(ctx context.Context, id string) (*Visitor, error) {
	if id == "" {
		return nil, sql.ErrNoRows
	}
	v := &Visitor{ID: id}
	err := s.db.QueryRowContext(ctx, `
SELECT site_id, ip_cipher, ua, country, city, referer, last_page, first_seen, last_seen, identifier
FROM visitors WHERE id=?`, id).Scan(
		&v.SiteID, &v.IPCipher, &v.UA, &v.Country, &v.City, &v.Referer, &v.LastPage,
		&v.FirstSeen, &v.LastSeen, &v.Identifier)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// ============ Conversation ============

// EnsureConversation 拿到（或新建）一个 open 会话，并返回是否新建。
// 调用方可以用 isNew 判断要不要触发"访客进入"通知和问候消息。
func (s *Store) EnsureConversation(ctx context.Context, siteID, visitorID string) (*Conversation, bool, error) {
	c, err := s.OpenOrGetConversation(ctx, siteID, visitorID)
	if err != nil {
		return nil, false, err
	}
	// 新建的会话特征：started_at == updated_at（同一时刻插入）且差距毫秒级
	isNew := !c.StartedAt.IsZero() && c.StartedAt.Equal(c.UpdatedAt) &&
		time.Since(c.StartedAt) < 2*time.Second
	return c, isNew, nil
}

// EnsureFreshConversation 在 EnsureConversation 基础上加「活跃期」语义：
//   - 没有 open 会话 → 新建 → isNewSession=true
//   - 有 open 会话且 updated_at 距今 ≤ freshMinutes → 复用 → isNewSession=false
//   - 有 open 会话但 updated_at 距今 > freshMinutes → 关闭旧的 + 新建新的 →
//     isNewSession=true（让访客重新触发"问候 + 进入通知"，旧消息保留）
//
// freshMinutes <= 0 时等价于 EnsureConversation（永不超时重开）。
func (s *Store) EnsureFreshConversation(ctx context.Context, siteID, visitorID string, freshMinutes int) (*Conversation, bool, error) {
	existing, err := s.findOpenConversation(ctx, siteID, visitorID)
	if err != nil {
		return nil, false, err
	}
	if existing == nil {
		// 全新访客：没有旧会话可继承，agent_id 留空
		c, err := s.createConversation(ctx, siteID, visitorID, sql.NullInt64{})
		if err != nil {
			return nil, false, err
		}
		return c, true, nil
	}
	if freshMinutes > 0 && time.Since(existing.UpdatedAt) > time.Duration(freshMinutes)*time.Minute {
		// [083] Bug① 修复：超时本应「关旧开新」，但旧会话若还有客服未读(UnreadA>0)，
		//   关掉它会把未读埋进 closed 会话——工作台列表只查 status='open'，客服收到
		//   通知却在列表找不到人(爷爷反馈的「收到通知看不到人」)。
		//   所以只要有未读，一律复用旧会话(不关、不另起)，让客服先把未读处理完；
		//   isNew=false 不重复触发问候/进入提醒。无未读才走原来的关旧开新。
		if existing.UnreadA > 0 {
			return existing, false, nil
		}
		// 旧会话超时且无未读：关闭它（消息不删，可在客服「历史记录」页查到）+ 开新会话
		if err := s.CloseConversation(ctx, existing.ID); err != nil {
			return nil, false, err
		}
		// [085] 期望①：重建会话继承旧会话的客服(agent_id)，保住「已接手」不丢，
		//   避免新会话 agent_id=NULL 把"已接待的客户"挤出列表/降级为未接手。
		c, err := s.createConversation(ctx, siteID, visitorID, existing.AgentID)
		if err != nil {
			return nil, false, err
		}
		return c, true, nil
	}
	return existing, false, nil
}

// findOpenConversation 仅查询，不新建。没找到返回 (nil, nil)。
func (s *Store) findOpenConversation(ctx context.Context, siteID, visitorID string) (*Conversation, error) {
	c := &Conversation{}
	err := s.db.QueryRowContext(ctx, `
SELECT id, site_id, visitor_id, agent_id, status, unread_visitor, unread_agent, started_at, updated_at, closed_at
FROM conversations
WHERE visitor_id=? AND site_id=? AND status='open'
ORDER BY started_at DESC LIMIT 1`, visitorID, siteID).Scan(
		&c.ID, &c.SiteID, &c.VisitorID, &c.AgentID, &c.Status, &c.UnreadV, &c.UnreadA,
		&c.StartedAt, &c.UpdatedAt, &c.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// createConversation 仅新建一条 open 会话。
// [085] agentID 用于「超时重建继承旧客服」：全新访客传 sql.NullInt64{}（空），
//
//	超时重建传 existing.AgentID，保住「已接手」不丢。
func (s *Store) createConversation(ctx context.Context, siteID, visitorID string, agentID sql.NullInt64) (*Conversation, error) {
	now := time.Now()
	c := &Conversation{
		ID:        uuid.NewString(),
		SiteID:    siteID,
		VisitorID: visitorID,
		AgentID:   agentID,
		Status:    "open",
		StartedAt: now,
		UpdatedAt: now,
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO conversations(id, site_id, visitor_id, agent_id, status, unread_visitor, unread_agent, started_at, updated_at)
VALUES(?, ?, ?, ?, 'open', 0, 0, ?, ?)`,
		c.ID, c.SiteID, c.VisitorID, agentID, c.StartedAt, c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

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
	// [073] 接管会话不再刷 updated_at：客服「点开/接管」会话不是「新消息活动」，
	//   不应顶起会话在列表的时间和排序（updated_at 仅代表「最后一条消息时间」）。
	//   原先 SET updated_at=now() 会让客服「点开看一眼」把会话时间改成点击时间（爷爷反馈的 bug）。
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET agent_id=? WHERE id=? AND status='open'`,
		agentID, convID)
	return err
}

func (s *Store) CloseConversation(ctx context.Context, convID string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET status='closed', closed_at=?, updated_at=? WHERE id=?`,
		now, now, convID)
	return err
}

// ListOpenConversations 给客服后台用：当前所有进行中的会话（含访客信息 + 最后一条消息预览）。
// listVisitorAggregated [088] 会话列表统一实现：按「客户(访客)」聚合，每个访客取最新一条会话，
// 跨 open/closed（人走了 / 会话关了也算）。onlyContacted=true 时只保留「主动操作过」(visitor_engaged)的客户。
//
//	「全部」  = 所有来过的访客(含只浏览没说话 / 已离线 / 已关闭)，一人一条。
//	「已联系」= 访客主动发过文字/图片 或 打过语音电话(visitor_engaged=1)的客户，一人一条。
//
// 每行带 contacted 字段(该客户是否主动操作过)，供「全部」tab 标记。
// filter 是代码内常量拼接(非用户输入)，limit/offset 全参数化 ? 占位，无注入风险。
func (s *Store) listVisitorAggregated(ctx context.Context, onlyContacted bool, limit, offset int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	filter := ""
	if onlyContacted {
		filter = "WHERE c.visitor_id IN (SELECT DISTINCT visitor_id FROM conversations WHERE visitor_engaged = 1)"
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT t.id, t.visitor_id, t.agent_id, t.unread_agent, t.status, t.started_at, t.updated_at,
       v.identifier, v.country, v.city, v.last_page, v.referer, v.ip_cipher,
       EXISTS(SELECT 1 FROM conversations c3 WHERE c3.visitor_id = t.visitor_id AND c3.visitor_engaged = 1) AS contacted
FROM (
  SELECT c.id, c.visitor_id, c.agent_id, c.unread_agent, c.status, c.started_at, c.updated_at,
         ROW_NUMBER() OVER (PARTITION BY c.visitor_id ORDER BY c.updated_at DESC) AS rn
  FROM conversations c
  `+filter+`
) t
JOIN visitors v ON v.id = t.visitor_id
WHERE t.rn = 1
ORDER BY t.updated_at DESC
LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0, limit)
	for rows.Next() {
		var (
			id, vid                                   string
			aid                                       sql.NullInt64
			unread                                    int
			status                                    string
			started, updated                          time.Time
			ident, country, city, page, ref, ipCipher sql.NullString
			contacted                                 bool
		)
		if err := rows.Scan(&id, &vid, &aid, &unread, &status, &started, &updated,
			&ident, &country, &city, &page, &ref, &ipCipher, &contacted); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id":              id,
			"visitor_id":      vid,
			"agent_id":        nullInt(aid),
			"unread":          unread,
			"status":          status, // open/closed，前端可标记「已结束」
			"started_at":      started,
			"updated_at":      updated,
			"identifier":      nullStr(ident),
			"country":         nullStr(country),
			"city":            nullStr(city),
			"last_page":       nullStr(page),
			"referer":         nullStr(ref),
			"ip_cipher":       nullStr(ipCipher), // handler 层会解密成 ip 明文并删掉这个字段
			"contacted":       contacted,         // [088] 该客户是否主动操作过(发消息/图片/语音电话)
			"has_visitor_msg": contacted,         // 兼容前端旧字段名
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range out {
		if cid, ok := c["id"].(string); ok {
			if lm, err := s.getLastMessagePreview(ctx, cid); err == nil && lm != nil {
				c["last_message"] = lm
			}
		}
	}
	return out, nil
}

// ListAllVisitorConversations [088]「全部」：所有来过的访客(一人一条最新会话，跨 open/closed)。
func (s *Store) ListAllVisitorConversations(ctx context.Context, limit, offset int) ([]map[string]any, error) {
	return s.listVisitorAggregated(ctx, false, limit, offset)
}

// MarkAgentReplied [085] 标记会话已被客服回复过（客服发消息时调用）。
// 用于「已联系」口径：按访客历史聚合，该访客任一会话被回复过即算「已联系」。
// 幂等：只在 agent_replied=0 时写，避免重复 UPDATE 放大。
func (s *Store) MarkAgentReplied(ctx context.Context, convID string) error {
	if convID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET agent_replied=1 WHERE id=? AND agent_replied=0`, convID)
	return err
}

// ListContactedConversations [088]「已联系」：访客主动操作过(发文字/图片 或 打语音电话)的客户。
//
//	口径 = 访客名下任一会话 visitor_engaged=1（按客户聚合、跨 open/closed），不管客服是否回复过。
//	爷爷定的「已联系」真义：访客手动操作过才算；纯浏览(page_navigation)/系统问候/访客进入一律不算。
func (s *Store) ListContactedConversations(ctx context.Context, limit, offset int) ([]map[string]any, error) {
	return s.listVisitorAggregated(ctx, true, limit, offset)
}

// [088]「待回复」(ListPending/CountPending) 已移除：爷爷决定列表只保留「全部 / 已联系」两个口径。

// CountContactedVisitors [088]「已联系」客户总数（主动操作过的去重访客数）。
func (s *Store) CountContactedVisitors(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT visitor_id) FROM conversations WHERE visitor_engaged=1`).Scan(&n)
	return n, err
}

// CountAllVisitors [088]「全部」客户总数（所有来过的去重访客，跨 open/closed）。
func (s *Store) CountAllVisitors(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT visitor_id) FROM conversations`).Scan(&n)
	return n, err
}

// getLastMessagePreview 返回会话最近一条消息的预览（用于列表展示）。
// 图片/文件类型用占位文案，文本超长截断 50 字。
func (s *Store) getLastMessagePreview(ctx context.Context, convID string) (map[string]any, error) {
	var sender, content string
	var mediaKind sql.NullString
	var createdAt time.Time
	// [072] 侧边「最新消息预览」跳过 page_navigation（访客浏览动作），显示真正的最后一句对话。
	err := s.db.QueryRowContext(ctx, `
SELECT sender, content, media_kind, created_at
FROM messages WHERE conv_id=? AND NOT (sender='sys' AND sender_ref LIKE 'page:%')
ORDER BY created_at DESC LIMIT 1`, convID).Scan(
		&sender, &content, &mediaKind, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	preview := content
	if preview == "" && mediaKind.Valid {
		switch mediaKind.String {
		case "image":
			preview = "[图片]"
		default:
			preview = "[文件]"
		}
	}
	rs := []rune(preview)
	if len(rs) > 50 {
		preview = string(rs[:50]) + "…"
	}
	return map[string]any{
		"sender":     sender,
		"content":    preview,
		"created_at": createdAt,
	}, nil
}

// ============ Message ============

// AgentOwnsConversation [077] 校验会话存在且该客服已接管(或会话未分配，允许接管过程中发送)。
// 防 agent 孤儿消息 + 防越权写入他人会话([069] 遗留 TODO)。走 idx_agent 索引、参数化防注入。
func (s *Store) AgentOwnsConversation(ctx context.Context, convID, agentID string) (bool, error) {
	if convID == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM conversations WHERE id=? AND (agent_id=CAST(? AS UNSIGNED) OR agent_id IS NULL) LIMIT 1`,
		convID, agentID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) InsertMessage(ctx context.Context, m *Message) error {
	// [077] 兜底：conv_id 不能为空（防任何路径写入孤儿消息；同步层 PreprocessAgentMessage 已拦，这里双保险）
	if m.ConvID == "" {
		return errors.New("insert_message: conv_id 不能为空")
	}
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

	// ===== [065] 修复：sys 消息不再错算 unread_agent =====
	// 业务规则：
	//   visitor 发消息  → 客服未读 +1 (unread_agent)
	//   agent 发消息    → 访客未读 +1 (unread_visitor)
	//   sys（系统消息） → 只刷 updated_at（让会话上浮），不累加任何一端的未读
	//                    例：访客访问页面横幅、自动问候、语音转文字结果
	// 设计要点：
	//   1. 用 switch 显式枚举三种 sender，default 兜底（防止未来悄悄混进新类型又走错分支）
	//   2. sys 类消息只刷新 updated_at 让会话在客服列表「按最近活跃」排序时上浮，
	//      但绝不累加 unread_agent —— 因为 sys 不是访客主动发的内容，客服无需「读」它，
	//      当成未读会让 badge 虚高（爷爷之前发现的 bug：badge=2 但实际只有 1 条访客消息）
	//   3. 两条 UPDATE 路径都只走一次 SQL（避免一次 InsertMessage 两次往返 DB）
	//   4. 全程参数化 SQL（占位符 ?），杜绝注入
	switch m.Sender {
	case "visitor":
		// [088] 访客发消息/图片 = 主动操作过 → 置 visitor_engaged（「已联系」口径用）
		_, err = s.db.ExecContext(ctx,
			`UPDATE conversations SET updated_at=?, unread_agent=unread_agent+1, visitor_engaged=1 WHERE id=?`,
			m.CreatedAt, m.ConvID)
	case "agent":
		_, err = s.db.ExecContext(ctx,
			`UPDATE conversations SET updated_at=?, unread_visitor=unread_visitor+1 WHERE id=?`,
			m.CreatedAt, m.ConvID)
	case "sys":
		// 系统消息：仅刷新 updated_at（客服会话列表按 updated_at 倒序，让有最新活动的会话排前面）。
		// [072] 例外：page_navigation（访客浏览动作，sender_ref="page:<url>"）只落库展示、不刷 updated_at，
		//   不让「访客访问了 X 页面」顶起会话列表的时间和排序；voice 来电(voice:*)/问候等其他 sys 照旧上浮。
		if strings.HasPrefix(m.SenderRef, "voice") {
			// [088] voice 通话事件 = 访客主动打来电话(含未接/秒挂) → 置 visitor_engaged（「已联系」口径用）
			_, err = s.db.ExecContext(ctx,
				`UPDATE conversations SET updated_at=?, visitor_engaged=1 WHERE id=?`,
				m.CreatedAt, m.ConvID)
		} else if !strings.HasPrefix(m.SenderRef, "page:") {
			_, err = s.db.ExecContext(ctx,
				`UPDATE conversations SET updated_at=? WHERE id=?`,
				m.CreatedAt, m.ConvID)
		}
	default:
		// 防御性：未来万一引入新 sender 类型却忘了改这里，兜底只刷 updated_at，不污染 unread
		_, err = s.db.ExecContext(ctx,
			`UPDATE conversations SET updated_at=? WHERE id=?`,
			m.CreatedAt, m.ConvID)
	}
	return err
}

func (s *Store) MarkRead(ctx context.Context, convID, by string) error {
	col := "unread_visitor"
	if by == "agent" {
		col = "unread_agent"
	}
	// [073] 标记已读不再刷 updated_at：已读是「客服查看」动作、不是新消息，
	//   不应顶起会话列表的时间/排序。只清未读计数。
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET `+col+`=0 WHERE id=?`,
		convID)
	return err
}

// UpdateLastRead 把指定 role 的 last_read_*_at 推到 at；同时把对应 unread 清零。
// role: "agent" 或 "visitor"。
// 这是「已读」语义的服务端落地：所有 created_at <= at 且 sender 为对端的消息视为已读。
func (s *Store) UpdateLastRead(ctx context.Context, convID, role string, at time.Time) error {
	var col, unreadCol string
	switch role {
	case "agent":
		col, unreadCol = "last_read_agent_at", "unread_agent"
	case "visitor":
		col, unreadCol = "last_read_visitor_at", "unread_visitor"
	default:
		return errors.New("invalid role for UpdateLastRead")
	}
	// [073] 已读不再刷 updated_at（同 MarkRead / AssignAgent）：避免客服点开会话标记已读
	//   把会话时间顶成点击时间。只推 last_read_*_at + 清对应未读。
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET `+col+`=?, `+unreadCol+`=0 WHERE id=?`,
		at, convID)
	return err
}

// GetLastReadTimes 返回会话的两个已读时间戳，用于 ListMessages 计算 read 字段。
func (s *Store) GetLastReadTimes(ctx context.Context, convID string) (lastAgent, lastVisitor sql.NullTime, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT last_read_agent_at, last_read_visitor_at FROM conversations WHERE id=?`,
		convID).Scan(&lastAgent, &lastVisitor)
	return
}

func (s *Store) ListMessages(ctx context.Context, convID string, beforeID string, afterID string, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case afterID != "":
		// [070] 增量同步：拉比 afterID 更新的消息，用于进会话后台静默刷新（前端先显本地缓存，再补这段增量）。
		// 注意 messages.created_at 是 DATETIME 秒级精度，同一秒可能有多条 → 用 >= 而非 >，
		// 避免漏掉与 afterID 同一秒的后续消息；带回的 afterID 自身那几条由前端按 message.id 去重丢弃。
		// ASC 从旧到新，便于前端往列表尾部顺序追加。走 idx_conv_time(conv_id, created_at) 索引，O(log N)。
		rows, err = s.db.QueryContext(ctx, `
SELECT id, conv_id, sender, sender_ref, content, media_url, media_kind, media_name, media_size, delivered_ws, created_at
FROM messages WHERE conv_id=? AND created_at >= (SELECT created_at FROM messages WHERE id=?)
ORDER BY created_at ASC LIMIT ?`, convID, afterID, limit)
	case beforeID != "":
		rows, err = s.db.QueryContext(ctx, `
SELECT id, conv_id, sender, sender_ref, content, media_url, media_kind, media_name, media_size, delivered_ws, created_at
FROM messages WHERE conv_id=? AND created_at < (SELECT created_at FROM messages WHERE id=?)
ORDER BY created_at DESC LIMIT ?`, convID, beforeID, limit)
	default:
		rows, err = s.db.QueryContext(ctx, `
SELECT id, conv_id, sender, sender_ref, content, media_url, media_kind, media_name, media_size, delivered_ws, created_at
FROM messages WHERE conv_id=? ORDER BY created_at DESC LIMIT ?`, convID, limit)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 拉对端的 last_read_*_at 并为每条消息计算 read 状态
	lastAgent, lastVisitor, err := s.GetLastReadTimes(ctx, convID)
	if err == nil {
		for i := range out {
			switch out[i].Sender {
			case "visitor":
				// 访客发的消息，被「客服」读过 → read=true
				if lastAgent.Valid && !out[i].CreatedAt.After(lastAgent.Time) {
					out[i].Read = true
				}
			case "agent":
				if lastVisitor.Valid && !out[i].CreatedAt.After(lastVisitor.Time) {
					out[i].Read = true
				}
			}
		}
	}
	return out, nil
}

// ListMessagesByVisitor [090] 按「客户(访客)」查其所有会话段的消息，合并成一条完整时间流。
//
//	配合 [088] 列表按客户聚合：点开客户要看到他「所有历史会话」(含已结束的旧会话段)的真实对话/通话，
//	而不是只看最新一段会话(常常只有系统消息)。
//	read 状态：每条消息按它「所属那段会话」的 last_read_*_at 算(JOIN conversations 带出)，跨会话段各算各的。
//	分页同 ListMessages：after 增量 / before 翻页 / default 最新；游标是 message id，按 created_at 全局排序。
//	走 idx(conversations.visitor_id) + messages.conv_id 索引。
func (s *Store) ListMessagesByVisitor(ctx context.Context, visitorID, beforeID, afterID string, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	base := `
SELECT m.id, m.conv_id, m.sender, m.sender_ref, m.content, m.media_url, m.media_kind, m.media_name, m.media_size, m.delivered_ws, m.created_at,
       c.last_read_agent_at, c.last_read_visitor_at
FROM messages m
JOIN conversations c ON c.id = m.conv_id
WHERE c.visitor_id = ?`
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case afterID != "":
		rows, err = s.db.QueryContext(ctx, base+`
  AND m.created_at >= (SELECT created_at FROM messages WHERE id=?)
ORDER BY m.created_at ASC LIMIT ?`, visitorID, afterID, limit)
	case beforeID != "":
		rows, err = s.db.QueryContext(ctx, base+`
  AND m.created_at < (SELECT created_at FROM messages WHERE id=?)
ORDER BY m.created_at DESC LIMIT ?`, visitorID, beforeID, limit)
	default:
		rows, err = s.db.QueryContext(ctx, base+`
ORDER BY m.created_at DESC LIMIT ?`, visitorID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Message, 0, limit)
	for rows.Next() {
		var m Message
		var lastAgent, lastVisitor sql.NullTime
		if err := rows.Scan(&m.ID, &m.ConvID, &m.Sender, &m.SenderRef, &m.Content,
			&m.MediaURL, &m.MediaKind, &m.MediaName, &m.MediaSize, &m.DeliveredWS, &m.CreatedAt,
			&lastAgent, &lastVisitor); err != nil {
			return nil, err
		}
		// 每条消息按其「所属会话」的 last_read 算 read（跨会话段各算各的）
		switch m.Sender {
		case "visitor":
			if lastAgent.Valid && !m.CreatedAt.After(lastAgent.Time) {
				m.Read = true
			}
		case "agent":
			if lastVisitor.Valid && !m.CreatedAt.After(lastVisitor.Time) {
				m.Read = true
			}
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

// GetAgentByID [064] 按 ID 查 agent。/agent/login/refresh 用，确认 agent 仍存在且 active 再签新 token。
// 不返回 pass_hash（refresh 流程不需要）。
func (s *Store) GetAgentByID(ctx context.Context, id int64) (*Agent, error) {
	a := &Agent{}
	err := s.db.QueryRowContext(ctx, `
SELECT id, username, role, nickname, active, created_at, last_login
FROM agents WHERE id=? LIMIT 1`, id).Scan(
		&a.ID, &a.Username, &a.Role, &a.Nickname, &a.Active, &a.CreatedAt, &a.LastLogin)
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
		// [052] 把 driver 层错误归类成业务 sentinel error，handler 可 errors.Is 分支返不同 code
		return 0, mapMySQLError(err)
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
	ID          string    `json:"id"`
	ConvID      string    `json:"conv_id"`
	UploadBy    string    `json:"upload_by"`
	UploaderRef string    `json:"uploader_ref"`
	Filename    string    `json:"filename"`
	StoreKey    string    `json:"store_key"`
	Size        int64     `json:"size"`
	MIME        string    `json:"mime"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Store) InsertFile(ctx context.Context, f *FileRecord) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO files(id, conv_id, upload_by, uploader_ref, filename, store_key, size, mime, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.ConvID, f.UploadBy, f.UploaderRef, f.Filename, f.StoreKey, f.Size, f.MIME, f.CreatedAt)
	return err
}

// ============ Settings (key-value) ============

// GetSetting 单条读取；missing 时返回 def。
func (s *Store) GetSetting(ctx context.Context, key, def string) string {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key_name=?`, key).Scan(&v)
	if err != nil || !v.Valid {
		return def
	}
	return v.String
}

// GetSettingsMap 批量读取。允许 keys 为空表示拉全表。
func (s *Store) GetSettingsMap(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	var (
		rows *sql.Rows
		err  error
	)
	if len(keys) == 0 {
		rows, err = s.db.QueryContext(ctx, `SELECT key_name, value FROM settings`)
	} else {
		// IN 子句安全拼接（keys 来自代码硬编码，非用户输入）
		placeholders := ""
		args := make([]any, len(keys))
		for i, k := range keys {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args[i] = k
		}
		rows, err = s.db.QueryContext(ctx,
			`SELECT key_name, value FROM settings WHERE key_name IN (`+placeholders+`)`, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v sql.NullString
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if v.Valid {
			out[k] = v.String
		} else {
			out[k] = ""
		}
	}
	return out, rows.Err()
}

// SetSetting upsert 单条。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings(key_name, value, updated_at) VALUES(?, ?, ?)
ON DUPLICATE KEY UPDATE value=VALUES(value), updated_at=VALUES(updated_at)`,
		key, value, time.Now())
	return err
}

// SetSettings 批量 upsert。
func (s *Store) SetSettings(ctx context.Context, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	now := time.Now()
	for k, v := range kv {
		_, err := tx.ExecContext(ctx, `
INSERT INTO settings(key_name, value, updated_at) VALUES(?, ?, ?)
ON DUPLICATE KEY UPDATE value=VALUES(value), updated_at=VALUES(updated_at)`,
			k, v, now)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
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
