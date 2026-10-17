package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func hallRow(id, sender, ref, content string, at time.Time, deletedBy string) []driver.Value {
	var deletedAt driver.Value
	if deletedBy != "" {
		deletedAt = at
	}
	return []driver.Value{id, HallDefaultRoom, sender, ref, "v_0123456789abcdef0123", "访客 0001", content, nil, nil, nil, nil, at, deletedAt, deletedBy}
}

// 历史查询：参数化、按 (room, created_at) 倒序取最近 N 条再翻转为正序；访客能看到自己的静默消息。
func TestListHallMessagesSQLAndOrder(t *testing.T) {
	now := time.Now()
	s, f := newFakeStore(t, func(q string, args []driver.Value) fakeResult {
		if strings.Contains(q, "SELECT created_at FROM hall_messages") {
			return fakeResult{cols: cols(1), rows: [][]driver.Value{{now}}}
		}
		return fakeResult{cols: cols(14), rows: [][]driver.Value{
			hallRow("m3", "visitor", "vid", "c3", now.Add(-1*time.Second), ""),
			hallRow("m2", "visitor", "vid", "c2", now.Add(-2*time.Second), HallSilencedBy),
			hallRow("m1", "agent", "1", "c1", now.Add(-3*time.Second), ""),
		}}
	})
	list, err := s.ListHallMessages(context.Background(), HallDefaultRoom, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 999, HallViewer{Sender: "visitor", SenderRef: "vid"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != "m1" || list[2].ID != "m3" {
		t.Fatalf("应翻转为正序：%+v", list)
	}
	calls := f.snapshot()
	for _, c := range calls {
		assertPlaceholders(t, c)
	}
	q := calls[len(calls)-1]
	if !strings.Contains(q.query, "deleted_at IS NULL OR (deleted_by=? AND sender=? AND sender_ref=?)") || !strings.Contains(q.query, "ORDER BY created_at DESC, id DESC LIMIT ?") {
		t.Fatalf("SQL 结构错误：%s", q.query)
	}
	if q.args[len(q.args)-1] != int64(50) {
		t.Fatalf("limit 必须被钳制到 50：%v", q.args)
	}
	for _, a := range q.args {
		if s, ok := a.(string); ok && strings.Contains(q.query, s) && len(s) > 3 {
			t.Fatalf("参数值不得拼进 SQL：%s", s)
		}
	}
	// 游标不存在：返回空列表而不是全部
	s2, _ := newFakeStore(t, func(q string, _ []driver.Value) fakeResult { return fakeResult{cols: cols(1)} })
	empty, err := s2.ListHallMessages(context.Background(), HallDefaultRoom, "x", 50, HallViewer{})
	if err != nil || len(empty) != 0 {
		t.Fatalf("游标不存在应返回空：%v %v", empty, err)
	}
}

func TestInsertHallMessageIdempotentAndConflict(t *testing.T) {
	now := time.Now()
	existing := hallRow("m1", "visitor", "vid", "hello", now, "")
	s, _ := newFakeStore(t, func(q string, _ []driver.Value) fakeResult {
		if strings.HasPrefix(strings.TrimSpace(q), "INSERT INTO hall_messages") {
			return fakeResult{err: &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}}
		}
		return fakeResult{cols: cols(14), rows: [][]driver.Value{existing}}
	})
	m := &HallMessage{ID: "m1", Room: HallDefaultRoom, Sender: "visitor", SenderRef: "vid", Content: "hello", CreatedAt: now}
	inserted, err := s.InsertHallMessage(context.Background(), m)
	if err != nil || inserted {
		t.Fatalf("同发送者同内容重试应幂等：%v %v", inserted, err)
	}
	m.Content = "被篡改"
	if _, err := s.InsertHallMessage(context.Background(), m); !errors.Is(err, ErrHallMessageConflict) {
		t.Fatalf("同 ID 不同内容应冲突：%v", err)
	}
	m.Content, m.SenderRef = "hello", "other-vid"
	if _, err := s.InsertHallMessage(context.Background(), m); !errors.Is(err, ErrHallMessageConflict) {
		t.Fatalf("同 ID 不同发送者应冲突：%v", err)
	}
}

func TestSoftDeleteAndPurgeHallSQL(t *testing.T) {
	s, f := newFakeStore(t, func(q string, _ []driver.Value) fakeResult {
		switch {
		case strings.HasPrefix(strings.TrimSpace(q), "UPDATE hall_messages"):
			return fakeResult{affected: 0}
		case strings.HasPrefix(strings.TrimSpace(q), "DELETE FROM hall_messages"):
			return fakeResult{affected: 7}
		case strings.Contains(q, "SELECT 1 FROM hall_messages"):
			return fakeResult{cols: cols(1), rows: [][]driver.Value{{int64(1)}}}
		}
		return fakeResult{cols: cols(1)}
	})
	found, changed, err := s.SoftDeleteHallMessage(context.Background(), HallDefaultRoom, "m1", "agent:2", time.Now())
	if err != nil || !found || changed {
		t.Fatalf("已删除的消息再删应 found=true changed=false：%v %v %v", found, changed, err)
	}
	n, err := s.PurgeHallMessagesBefore(context.Background(), time.Now().Add(-HallRetention), 99999)
	if err != nil || n != 7 {
		t.Fatalf("清理返回行数错误：%d %v", n, err)
	}
	for _, c := range f.snapshot() {
		assertPlaceholders(t, c)
		if strings.HasPrefix(strings.TrimSpace(c.query), "DELETE FROM hall_messages") && c.args[1] != int64(1000) {
			t.Fatalf("单批删除上限必须被钳制：%v", c.args)
		}
	}
}

func TestBlockedVisitorIDsChunked(t *testing.T) {
	s, f := newFakeStore(t, func(q string, args []driver.Value) fakeResult {
		return fakeResult{cols: cols(1), rows: [][]driver.Value{{args[0]}}}
	})
	ids := make([]string, 0, 450)
	for i := 0; i < 450; i++ {
		ids = append(ids, fmt.Sprintf("vid-%03d", i))
	}
	got, err := s.BlockedVisitorIDs(context.Background(), append(ids, ids[0], ""))
	if err != nil {
		t.Fatal(err)
	}
	calls := f.snapshot()
	if len(calls) != 3 {
		t.Fatalf("450 个应分 3 批查询：%d", len(calls))
	}
	for _, c := range calls {
		assertPlaceholders(t, c)
	}
	if !got["vid-000"] || !got["vid-200"] || !got["vid-400"] {
		t.Fatalf("结果合并错误：%v", got)
	}
}
