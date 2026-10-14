package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// conversation_list.go [115] 会话列表（按客户聚合）两阶段查询。
//
// 背景：旧实现用 ROW_NUMBER() OVER (PARTITION BY visitor_id ...) 先对全部会话排序，SELECT 列表里
// 「是否已联系」的相关子查询在 ORDER BY/LIMIT 之前逐行执行，优化器还会选 idx_conv_deleted(deleted_at)
// 做 deleted_at IS NULL 的 ref 查找（几乎所有行都命中，随机回表），之后再逐行查最后一条消息预览（N+1）。
// 生产 1.7 万会话时单次 1.3~2.3 秒、并发 4.6~6 秒。
//
// 新实现（接口字段、口径、排序语义不变）：
//
//	第 1 阶段：只读会话表覆盖索引 idx_conv_list（迁移 014）按访客分组 + 访客/资料表主键查找，
//	          求出本页访客 ID 与顺序（置顶在前按 pinned_at 倒序，其余按「代表会话 updated_at」倒序）。
//	第 2 阶段：只对本页 ≤limit 个访客批量取 代表会话+访客信息+资料+已联系（1 条 SQL）、
//	          最后一条消息预览（1 条 LATERAL SQL）、标签（1 条 SQL），全程无逐行查询。
//
// 两个阶段在同一个只读事务里执行（InnoDB 一致性快照），避免两次查询之间数据变化导致缺行。
//
// 排序口径说明：旧实现按「代表会话（优先 open，其次 updated_at 最新，再 started_at 最新）的 updated_at」排序。
// 代表会话的 updated_at 恰好等于：该访客存在未删除的 open 会话时取 open 会话的 MAX(updated_at)，
// 否则取全部未删除会话的 MAX(updated_at)。因此第 1 阶段用
// COALESCE(MAX(CASE WHEN status='open' THEN updated_at END), MAX(updated_at)) 精确复现旧口径，
// 无需先确定每位访客的代表会话（那正是旧实现慢的根源），也没有改成「全部会话 MAX(updated_at)」。
// 排序键完全相同时旧实现顺序不确定，新实现追加 visitor_id DESC / id DESC 作为稳定的决胜键，翻页不重不漏。
//
// 安全：SQL 结构全部是代码内常量拼接，tag、ID 列表、limit/offset 一律 ? 参数化（DSN 关闭 interpolateParams）。

const (
	listDefaultLimit = 50
	listMaxLimit     = 200
	// previewMaxRunes 列表最后一条消息预览的最大字数（超出截断加「…」）。
	previewMaxRunes = 50
)

// queryer 由 *sql.DB 与 *sql.Tx 实现，便于同一批查询在只读快照事务中执行。
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// normalizeListPage 与 handler 同口径兜底分页参数。
func normalizeListPage(limit, offset int) (int, int) {
	if limit <= 0 || limit > listMaxLimit {
		limit = listDefaultLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// visitorGroupSQL 按访客分组的未删除会话（只用会话表覆盖索引 idx_conv_list）。
// IGNORE INDEX (idx_conv_deleted)：deleted_at IS NULL 几乎命中全表，走该索引是低选择性 ref + 随机回表。
// withLastActive=true 时带出「代表会话 updated_at」排序键（见文件头说明）。
// onlyContacted=true 时只保留存在 visitor_engaged=1 的未删除会话的访客（与旧实现 IN 子查询 / 计数 WHERE 同口径）。
func visitorGroupSQL(onlyContacted, withLastActive bool) string {
	var b strings.Builder
	b.WriteString("\n  SELECT c.visitor_id")
	if withLastActive {
		b.WriteString(",\n         COALESCE(MAX(CASE WHEN c.status = 'open' THEN c.updated_at END), MAX(c.updated_at)) AS last_active")
	}
	b.WriteString("\n  FROM conversations c IGNORE INDEX (idx_conv_deleted)\n  WHERE c.deleted_at IS NULL\n  GROUP BY c.visitor_id")
	if onlyContacted {
		b.WriteString("\n  HAVING MAX(c.visitor_engaged) = 1")
	}
	return b.String()
}

const tagExistsSQL = "\n  AND EXISTS (SELECT 1 FROM visitor_tags vt WHERE vt.visitor_id = a.visitor_id AND vt.tag = ?)"

// visitorPageSQL 第 1 阶段：本页访客 ID 与顺序。排除已拉黑客户；JOIN visitors 与旧实现一致（无访客行的会话不展示）。
func visitorPageSQL(onlyContacted, withTag bool) string {
	var b strings.Builder
	b.WriteString("\nSELECT a.visitor_id\nFROM (")
	b.WriteString(visitorGroupSQL(onlyContacted, true))
	b.WriteString("\n) a\nJOIN visitors v ON v.id = a.visitor_id\nLEFT JOIN visitor_profiles p ON p.visitor_id = a.visitor_id\nWHERE p.blocked_at IS NULL")
	if withTag {
		b.WriteString(tagExistsSQL)
	}
	b.WriteString("\nORDER BY (p.pinned_at IS NOT NULL) DESC, p.pinned_at DESC, a.last_active DESC, a.visitor_id DESC\nLIMIT ? OFFSET ?")
	return b.String()
}

// visitorCountSQL 与列表同口径的客户总数：排除回收站会话与已拉黑客户，可按标签筛选。
// 与旧实现 COUNT(DISTINCT c.visitor_id) 等价（资料表按 visitor_id 唯一，标签条件按访客判断），且不 JOIN visitors（旧口径如此）。
func visitorCountSQL(onlyContacted, withTag bool) string {
	var b strings.Builder
	b.WriteString("\nSELECT COUNT(*)\nFROM (")
	b.WriteString(visitorGroupSQL(onlyContacted, false))
	b.WriteString("\n) a\nLEFT JOIN visitor_profiles p ON p.visitor_id = a.visitor_id\nWHERE p.blocked_at IS NULL")
	if withTag {
		b.WriteString(tagExistsSQL)
	}
	return b.String()
}

// representativeSQL 第 2 阶段：本页访客的代表会话（与旧 ROW_NUMBER 规则一致：优先 open，其次 updated_at 最新，
// 再 started_at 最新；完全相同时按 id 决胜）+ 是否已联系 + 访客信息 + 资料，一条 SQL 批量取回。
// ph 为 inPlaceholders 生成的 "?,?,?"。
func representativeSQL(ph string) string {
	return `
SELECT t.id, t.visitor_id, t.agent_id, t.unread_agent, t.status, t.started_at, t.updated_at, (t.engaged = 1) AS contacted,
       v.identifier, v.country, v.city, v.last_page, v.referer, v.ip_cipher,
       COALESCE(p.note, ''), p.pinned_at IS NOT NULL, COALESCE(p.marked_unread, 0)
FROM (
  SELECT c.id, c.visitor_id, c.agent_id, c.unread_agent, c.status, c.started_at, c.updated_at,
         MAX(c.visitor_engaged) OVER (PARTITION BY c.visitor_id) AS engaged,
         ROW_NUMBER() OVER (PARTITION BY c.visitor_id ORDER BY (c.status = 'open') DESC, c.updated_at DESC, c.started_at DESC, c.id DESC) AS rn
  FROM conversations c IGNORE INDEX (idx_conv_deleted)
  WHERE c.deleted_at IS NULL AND c.visitor_id IN (` + ph + `)
) t
JOIN visitors v ON v.id = t.visitor_id
LEFT JOIN visitor_profiles p ON p.visitor_id = t.visitor_id
WHERE t.rn = 1`
}

// lastMessageSQL 第 2 阶段：本页代表会话各自最后一条消息（LATERAL 每会话走 idx_conv_time 倒序取 1 条，一次往返）。
// 过滤条件、排序与旧 getLastMessagePreview 逐字一致：跳过 page_navigation（sys + sender_ref LIKE 'page:%'）。
// LEFT(content, 51) 只取预览所需字数（MySQL 按字符截取，与 Go 按 rune 截断等价），避免长消息全文回传。
// ph 为 inPlaceholders 生成的 "?,?,?"。
func lastMessageSQL(ph string) string {
	return `
SELECT c.id, lm.sender, lm.content, lm.media_kind, lm.created_at, lm.recalled
FROM conversations c,
LATERAL (
  SELECT m.sender, LEFT(m.content, 51) AS content, m.media_kind, m.created_at, m.recalled_at IS NOT NULL AS recalled
  FROM messages m
  WHERE m.conv_id = c.id AND NOT (m.sender = 'sys' AND m.sender_ref LIKE 'page:%')
  ORDER BY m.created_at DESC
  LIMIT 1
) lm
WHERE c.id IN (` + ph + `)`
}

// formatMessagePreview 列表最后一条消息预览文案：撤回显示「[已撤回]」，无文字的图片「[图片]」、其他附件「[文件]」，
// 超过 50 字截断加「…」。与 0.8.0 getLastMessagePreview 规则完全一致。
func formatMessagePreview(content string, mediaKind sql.NullString, recalled bool) string {
	preview := content
	if recalled {
		preview = "[已撤回]"
	} else if preview == "" && mediaKind.Valid {
		switch mediaKind.String {
		case "image":
			preview = "[图片]"
		default:
			preview = "[文件]"
		}
	}
	rs := []rune(preview)
	if len(rs) > previewMaxRunes {
		preview = string(rs[:previewMaxRunes]) + "…"
	}
	return preview
}

// listVisitorAggregated [088][111][113][115] 会话列表统一实现：按「客户(访客)」聚合，一人一条，跨 open/closed。
//
//	「全部」  = 所有来过的访客(含只浏览没说话 / 已离线 / 已关闭)。
//	「已联系」= 访客主动发过文字/图片 或 打过语音电话(visitor_engaged=1)的客户（onlyContacted=true）。
//
// 每行带 contacted(该客户是否主动操作过)；排除回收站会话与已拉黑客户；置顶在前；可按标签精确筛选。
// 实现见文件头「两阶段」说明。
func (s *Store) listVisitorAggregated(ctx context.Context, onlyContacted bool, tag string, limit, offset int) ([]map[string]any, error) {
	limit, offset = normalizeListPage(limit, offset)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	vids, err := pageVisitorIDs(ctx, tx, onlyContacted, tag, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(vids))
	if len(vids) == 0 {
		return out, tx.Commit()
	}
	reps, err := representativeRows(ctx, tx, vids)
	if err != nil {
		return nil, err
	}
	convIDs := make([]string, 0, len(reps))
	for _, vid := range vids {
		if r, ok := reps[vid]; ok {
			convIDs = append(convIDs, r.convID)
		}
	}
	previews, err := lastMessagePreviews(ctx, tx, convIDs)
	if err != nil {
		return nil, err
	}
	tags, err := tagsForVisitors(ctx, tx, vids)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return assembleVisitorRows(vids, reps, previews, tags), nil
}

// countVisibleVisitors [113][115] 与列表同口径计数：排除回收站、已拉黑客户，可按标签筛选。
func (s *Store) countVisibleVisitors(ctx context.Context, onlyContacted bool, tag string) (int, error) {
	args := []any{}
	if tag != "" {
		args = append(args, tag)
	}
	var n int
	err := s.db.QueryRowContext(ctx, visitorCountSQL(onlyContacted, tag != ""), args...).Scan(&n)
	return n, err
}

// pageVisitorIDs 第 1 阶段：本页访客 ID（已排好序）。
func pageVisitorIDs(ctx context.Context, q queryer, onlyContacted bool, tag string, limit, offset int) ([]string, error) {
	args := make([]any, 0, 3)
	if tag != "" {
		args = append(args, tag)
	}
	args = append(args, limit, offset)
	rows, err := q.QueryContext(ctx, visitorPageSQL(onlyContacted, tag != ""), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vids := make([]string, 0, limit)
	for rows.Next() {
		var vid string
		if err := rows.Scan(&vid); err != nil {
			return nil, err
		}
		vids = append(vids, vid)
	}
	return vids, rows.Err()
}

// visitorListRow 第 2 阶段代表会话 + 访客 + 资料的扫描结果。
type visitorListRow struct {
	convID                                    string
	visitorID                                 string
	agentID                                   sql.NullInt64
	unread                                    int
	status                                    string
	startedAt, updatedAt                      time.Time
	contacted                                 bool
	ident, country, city, page, ref, ipCipher sql.NullString
	note                                      string
	pinned, markedUnread                      bool
}

// representativeRows 第 2 阶段：按访客 ID 批量取代表会话、访客信息、资料与已联系标记。
func representativeRows(ctx context.Context, q queryer, vids []string) (map[string]*visitorListRow, error) {
	out := make(map[string]*visitorListRow, len(vids))
	if len(vids) == 0 {
		return out, nil
	}
	ph, args := inPlaceholders(vids)
	rows, err := q.QueryContext(ctx, representativeSQL(ph), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r := &visitorListRow{}
		if err := rows.Scan(&r.convID, &r.visitorID, &r.agentID, &r.unread, &r.status, &r.startedAt, &r.updatedAt, &r.contacted,
			&r.ident, &r.country, &r.city, &r.page, &r.ref, &r.ipCipher, &r.note, &r.pinned, &r.markedUnread); err != nil {
			return nil, err
		}
		out[r.visitorID] = r
	}
	return out, rows.Err()
}

// lastMessagePreviews 第 2 阶段：按会话 ID 批量取最后一条消息预览（无消息的会话不出现在结果中）。
func lastMessagePreviews(ctx context.Context, q queryer, convIDs []string) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(convIDs))
	ids := uniqueNonEmpty(convIDs)
	if len(ids) == 0 {
		return out, nil
	}
	ph, args := inPlaceholders(ids)
	rows, err := q.QueryContext(ctx, lastMessageSQL(ph), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			convID, sender, content string
			mediaKind               sql.NullString
			createdAt               time.Time
			recalled                bool
		)
		if err := rows.Scan(&convID, &sender, &content, &mediaKind, &createdAt, &recalled); err != nil {
			return nil, err
		}
		out[convID] = map[string]any{
			"sender":     sender,
			"content":    formatMessagePreview(content, mediaKind, recalled),
			"created_at": createdAt,
		}
	}
	return out, rows.Err()
}

// assembleVisitorRows 按第 1 阶段顺序拼装接口行（字段与 0.8.0 完全一致）。快照内代表会话必然存在，缺失时跳过兜底。
func assembleVisitorRows(vids []string, reps map[string]*visitorListRow, previews map[string]map[string]any, tags map[string][]string) []map[string]any {
	out := make([]map[string]any, 0, len(vids))
	for _, vid := range vids {
		r, ok := reps[vid]
		if !ok {
			continue
		}
		unread := r.unread
		// [113] 手动标为未读：至少显示 1 条未读
		if r.markedUnread && unread == 0 {
			unread = 1
		}
		row := map[string]any{
			"id":              r.convID,
			"visitor_id":      r.visitorID,
			"agent_id":        nullInt(r.agentID),
			"unread":          unread,
			"status":          r.status, // open/closed，前端可标记「已结束」
			"started_at":      r.startedAt,
			"updated_at":      r.updatedAt,
			"identifier":      nullStr(r.ident),
			"country":         nullStr(r.country),
			"city":            nullStr(r.city),
			"last_page":       nullStr(r.page),
			"referer":         nullStr(r.ref),
			"ip_cipher":       nullStr(r.ipCipher), // handler 层会解密成 ip 明文并删掉这个字段
			"contacted":       r.contacted,         // [088] 该客户是否主动操作过(发消息/图片/语音电话)
			"has_visitor_msg": r.contacted,         // 兼容前端旧字段名
			"note":            r.note,              // [113] 客户备注
			"tags":            []string{},          // [113] 标签
			"pinned":          r.pinned,            // [113] 置顶
			"marked_unread":   r.markedUnread,      // [113] 手动标为未读
		}
		if t, ok := tags[vid]; ok {
			row["tags"] = t
		}
		if lm, ok := previews[r.convID]; ok {
			row["last_message"] = lm
		}
		out = append(out, row)
	}
	return out
}
