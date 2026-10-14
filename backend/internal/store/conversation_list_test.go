package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	tBase = time.Date(2026, 10, 14, 20, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
)

// listFixture 第 1 阶段返回 v3,v1,v2（已排好序）；第 2 阶段代表会话乱序返回，验证按第 1 阶段顺序拼装。
func listFixture(t *testing.T, previewErr error) (*Store, *fakeDB) {
	return newFakeStore(t, func(q string, args []driver.Value) fakeResult {
		switch {
		case strings.Contains(q, "SELECT a.visitor_id"):
			return fakeResult{cols: cols(1), rows: [][]driver.Value{{"v3"}, {"v1"}, {"v2"}}}
		case strings.Contains(q, "ROW_NUMBER() OVER"):
			return fakeResult{cols: cols(17), rows: [][]driver.Value{
				{"c1", "v1", nil, int64(0), "open", tBase, tBase.Add(time.Minute), int64(1),
					"甲", "中国", "北京", "https://a/p", "", "cipher1", "老客户", int64(1), int64(1)},
				{"c2", "v2", int64(4), int64(3), "closed", tBase, tBase, int64(0),
					nil, nil, nil, nil, nil, nil, "", int64(0), int64(0)},
				{"c3", "v3", int64(8), int64(2), "open", tBase, tBase.Add(2 * time.Minute), int64(1),
					"丙", "", "", "", "", "cipher3", "", int64(0), int64(0)},
			}}
		case strings.Contains(q, "LATERAL"):
			if previewErr != nil {
				return fakeResult{err: previewErr}
			}
			return fakeResult{cols: cols(6), rows: [][]driver.Value{
				{"c1", "agent", "原文不可外泄", nil, tBase, int64(1)},
				{"c3", "visitor", strings.Repeat("长", 51), nil, tBase, int64(0)},
			}}
		case strings.Contains(q, "FROM visitor_tags"):
			return fakeResult{cols: cols(2), rows: [][]driver.Value{{"v2", "VIP"}, {"v2", "新客"}}}
		}
		t.Fatalf("意外的 SQL: %s", q)
		return fakeResult{}
	})
}

func TestListVisitorAggregatedBatchesEveryLookup(t *testing.T) {
	s, f := listFixture(t, nil)
	rows, err := s.ListAllVisitorConversations(context.Background(), "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.snapshot()
	// 3 位访客：第 1 阶段 1 条 + 代表会话/访客/资料 1 条 + 预览 1 条 + 标签 1 条，与页大小无关（无 N+1）
	if len(calls) != 4 {
		t.Fatalf("应恰好 4 条批量 SQL，实际 %d", len(calls))
	}
	for _, c := range calls {
		assertPlaceholders(t, c)
	}
	if f.readOnlyBegins != 1 || f.commits != 1 {
		t.Fatalf("两阶段必须在同一只读事务中：readOnly=%d commits=%d", f.readOnlyBegins, f.commits)
	}
	if got := calls[0].args; len(got) != 2 || got[0] != int64(50) || got[1] != int64(0) {
		t.Fatalf("第 1 阶段 limit/offset 参数错误: %v", got)
	}
	if got := calls[1].args; len(got) != 3 || got[0] != "v3" || got[1] != "v1" || got[2] != "v2" {
		t.Fatalf("第 2 阶段应只查本页访客: %v", got)
	}
	if len(rows) != 3 || rows[0]["visitor_id"] != "v3" || rows[1]["visitor_id"] != "v1" || rows[2]["visitor_id"] != "v2" {
		t.Fatalf("必须保持第 1 阶段顺序: %v", rows)
	}
	v1 := rows[1]
	if v1["unread"] != 1 || v1["marked_unread"] != true || v1["pinned"] != true || v1["note"] != "老客户" {
		t.Fatalf("标为未读至少 1 条 / 资料字段错误: %v", v1)
	}
	if v1["agent_id"] != int64(0) || v1["contacted"] != true || v1["has_visitor_msg"] != true || v1["ip_cipher"] != "cipher1" {
		t.Fatalf("v1 字段错误: %v", v1)
	}
	if lm, _ := v1["last_message"].(map[string]any); lm == nil || lm["content"] != "[已撤回]" || lm["sender"] != "agent" {
		t.Fatalf("撤回消息预览必须为 [已撤回]: %v", v1["last_message"])
	}
	v3 := rows[0]
	if lm, _ := v3["last_message"].(map[string]any); lm == nil || lm["content"] != strings.Repeat("长", 50)+"…" {
		t.Fatalf("超过 50 字必须截断加省略号: %v", v3["last_message"])
	}
	if tags, _ := v3["tags"].([]string); tags == nil || len(tags) != 0 {
		t.Fatalf("无标签时必须是空数组: %#v", v3["tags"])
	}
	v2 := rows[2]
	if _, ok := v2["last_message"]; ok {
		t.Fatalf("无消息的会话不应带 last_message: %v", v2)
	}
	if tags, _ := v2["tags"].([]string); len(tags) != 2 || tags[0] != "VIP" {
		t.Fatalf("标签批量填充错误: %v", v2["tags"])
	}
	if v2["agent_id"] != int64(4) || v2["unread"] != 3 || v2["status"] != "closed" || v2["contacted"] != false || v2["identifier"] != "" {
		t.Fatalf("v2 字段错误: %v", v2)
	}
	// 字段集合与 0.8.0 一致
	want := []string{"id", "visitor_id", "agent_id", "unread", "status", "started_at", "updated_at", "identifier", "country", "city",
		"last_page", "referer", "ip_cipher", "contacted", "has_visitor_msg", "note", "tags", "pinned", "marked_unread", "last_message"}
	if len(v1) != len(want) {
		t.Fatalf("字段数量变化: got %d want %d", len(v1), len(want))
	}
	for _, k := range want {
		if _, ok := v1[k]; !ok {
			t.Fatalf("缺少字段 %s", k)
		}
	}
}

func TestListContactedWithTagUsesParameterizedFilters(t *testing.T) {
	s, f := newFakeStore(t, func(q string, args []driver.Value) fakeResult {
		return fakeResult{cols: cols(1)}
	})
	rows, err := s.ListContactedConversations(context.Background(), "VIP", 500, -3)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rows)
	if string(b) != "[]" {
		t.Fatalf("空页必须返回 [] 而不是 null: %s", b)
	}
	calls := f.snapshot()
	if len(calls) != 1 {
		t.Fatalf("空页不应再发第 2 阶段查询: %d", len(calls))
	}
	q := calls[0]
	assertPlaceholders(t, q)
	if !strings.Contains(q.query, "HAVING MAX(c.visitor_engaged) = 1") || !strings.Contains(q.query, "vt.tag = ?") {
		t.Fatalf("已联系 / 标签筛选条件缺失: %s", q.query)
	}
	if len(q.args) != 3 || q.args[0] != "VIP" || q.args[1] != int64(listDefaultLimit) || q.args[2] != int64(0) {
		t.Fatalf("越界分页必须兜底且标签参数化: %v", q.args)
	}
	if strings.Contains(q.query, "VIP") {
		t.Fatal("标签值不得拼接进 SQL")
	}
}

func TestListPhaseTwoErrorRollsBack(t *testing.T) {
	s, f := listFixture(t, errors.New("boom"))
	if _, err := s.ListAllVisitorConversations(context.Background(), "", 50, 0); err == nil {
		t.Fatal("预览查询失败必须返回错误")
	}
	if f.commits != 0 || f.rollbacks != 1 {
		t.Fatalf("失败时必须回滚: commits=%d rollbacks=%d", f.commits, f.rollbacks)
	}
}

func TestCountVisibleVisitorsSQL(t *testing.T) {
	s, f := newFakeStore(t, func(q string, args []driver.Value) fakeResult {
		return fakeResult{cols: cols(1), rows: [][]driver.Value{{int64(42)}}}
	})
	n, err := s.CountContactedVisitors(context.Background(), "老客户")
	if err != nil || n != 42 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if _, err := s.CountAllVisitors(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	calls := f.snapshot()
	for _, c := range calls {
		assertPlaceholders(t, c)
		if !strings.Contains(c.query, "IGNORE INDEX (idx_conv_deleted)") || !strings.Contains(c.query, "p.blocked_at IS NULL") || !strings.Contains(c.query, "c.deleted_at IS NULL") {
			t.Fatalf("计数必须排除回收站与拉黑并避开 idx_conv_deleted: %s", c.query)
		}
	}
	if !strings.Contains(calls[0].query, "HAVING MAX(c.visitor_engaged) = 1") || len(calls[0].args) != 1 {
		t.Fatalf("已联系计数口径错误: %s %v", calls[0].query, calls[0].args)
	}
	if strings.Contains(calls[1].query, "HAVING") || strings.Contains(calls[1].query, "visitor_tags") || len(calls[1].args) != 0 {
		t.Fatalf("全部计数不应带已联系/标签条件: %s", calls[1].query)
	}
}

func TestListSQLKeepsLegacySemantics(t *testing.T) {
	page := visitorPageSQL(false, false)
	for _, s := range []string{
		"IGNORE INDEX (idx_conv_deleted)",
		"COALESCE(MAX(CASE WHEN c.status = 'open' THEN c.updated_at END), MAX(c.updated_at)) AS last_active",
		"JOIN visitors v ON v.id = a.visitor_id",
		"WHERE p.blocked_at IS NULL",
		"ORDER BY (p.pinned_at IS NOT NULL) DESC, p.pinned_at DESC, a.last_active DESC",
	} {
		if !strings.Contains(page, s) {
			t.Fatalf("第 1 阶段 SQL 缺少 %q", s)
		}
	}
	rep := representativeSQL("?")
	if !strings.Contains(rep, "ORDER BY (c.status = 'open') DESC, c.updated_at DESC, c.started_at DESC") ||
		!strings.Contains(rep, "c.deleted_at IS NULL") || !strings.Contains(rep, "MAX(c.visitor_engaged) OVER (PARTITION BY c.visitor_id)") {
		t.Fatalf("代表会话规则与旧 ROW_NUMBER 不一致: %s", rep)
	}
	lm := lastMessageSQL("?")
	for _, s := range []string{"NOT (m.sender = 'sys' AND m.sender_ref LIKE 'page:%')", "ORDER BY m.created_at DESC", "LIMIT 1", "LATERAL"} {
		if !strings.Contains(lm, s) {
			t.Fatalf("预览 SQL 缺少 %q", s)
		}
	}
}

func TestFormatMessagePreview(t *testing.T) {
	img := sql.NullString{String: "image", Valid: true}
	file := sql.NullString{String: "file", Valid: true}
	cases := []struct {
		name, content string
		kind          sql.NullString
		recalled      bool
		want          string
	}{
		{"文本", "你好", sql.NullString{}, false, "你好"},
		{"图片", "", img, false, "[图片]"},
		{"其他附件", "", file, false, "[文件]"},
		{"带文字的图片显示文字", "看图", img, false, "看图"},
		{"空消息", "", sql.NullString{}, false, ""},
		{"撤回优先", "原文", img, true, "[已撤回]"},
		{"恰好 50 字不截断", strings.Repeat("a", 50), sql.NullString{}, false, strings.Repeat("a", 50)},
		{"51 字截断", strings.Repeat("字", 51), sql.NullString{}, false, strings.Repeat("字", 50) + "…"},
		{"多字节按 rune 截断", strings.Repeat("😀", 60), sql.NullString{}, false, strings.Repeat("😀", 50) + "…"},
	}
	for _, c := range cases {
		if got := formatMessagePreview(c.content, c.kind, c.recalled); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// SQL 侧 LEFT(content, 51) 只取前 51 字：对任意长度文本，截断前后生成的预览必须完全一致。
func TestPreviewSQLTruncationEquivalent(t *testing.T) {
	for _, n := range []int{0, 1, 49, 50, 51, 52, 200} {
		full := strings.Repeat("中a😀", n)
		rs := []rune(full)
		cut := full
		if len(rs) > 51 {
			cut = string(rs[:51])
		}
		if formatMessagePreview(full, sql.NullString{}, false) != formatMessagePreview(cut, sql.NullString{}, false) {
			t.Fatalf("n=%d LEFT(content,51) 与全文预览不一致", n)
		}
	}
}
