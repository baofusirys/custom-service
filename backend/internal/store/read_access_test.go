package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

func TestAgentCanReadConversationRejectsWithoutQuery(t *testing.T) {
	s, f := newFakeStore(t, func(q string, args []driver.Value) fakeResult {
		t.Fatalf("不应发出查询: %s", q)
		return fakeResult{}
	})
	if ok, err := s.AgentCanReadConversation(context.Background(), "", 8, false); ok || err != nil {
		t.Fatalf("空会话 ID 必须拒绝: ok=%v err=%v", ok, err)
	}
	if ok, err := s.AgentCanReadConversation(context.Background(), "conv-1", 0, false); ok || err != nil {
		t.Fatalf("无效客服 ID 必须拒绝: ok=%v err=%v", ok, err)
	}
	if len(f.snapshot()) != 0 {
		t.Fatal("拒绝分支不应访问数据库")
	}
}

func TestAgentCanReadConversationAdmin(t *testing.T) {
	s, f := newFakeStore(t, func(q string, args []driver.Value) fakeResult {
		return fakeResult{cols: cols(1), rows: [][]driver.Value{{int64(1)}}}
	})
	ok, err := s.AgentCanReadConversation(context.Background(), "conv-1", 1, true)
	if !ok || err != nil {
		t.Fatalf("管理员对未删除会话应允许: ok=%v err=%v", ok, err)
	}
	c := f.snapshot()[0]
	assertPlaceholders(t, c)
	if !strings.Contains(c.query, "id=? AND deleted_at IS NULL") || strings.Contains(c.query, "agent_id") || len(c.args) != 1 || c.args[0] != "conv-1" {
		t.Fatalf("管理员只校验会话存在且未删除: %s %v", c.query, c.args)
	}
}

func TestAgentCanReadConversationAgentScope(t *testing.T) {
	rows := [][]driver.Value{{int64(1)}}
	s, f := newFakeStore(t, func(q string, args []driver.Value) fakeResult {
		return fakeResult{cols: cols(1), rows: rows}
	})
	ok, err := s.AgentCanReadConversation(context.Background(), "conv-1", 8, false)
	if !ok || err != nil {
		t.Fatalf("可查看的会话应允许: ok=%v err=%v", ok, err)
	}
	c := f.snapshot()[0]
	assertPlaceholders(t, c)
	for _, want := range []string{
		"c.id = ? AND c.deleted_at IS NULL",
		"c.agent_id = ? OR c.agent_id IS NULL",
		"o.visitor_id = c.visitor_id AND o.deleted_at IS NULL",
		"o.agent_id = ? OR o.agent_id IS NULL",
	} {
		if !strings.Contains(c.query, want) {
			t.Fatalf("权限 SQL 缺少 %q: %s", want, c.query)
		}
	}
	if len(c.args) != 3 || c.args[0] != "conv-1" || c.args[1] != int64(8) || c.args[2] != int64(8) {
		t.Fatalf("参数错误: %v", c.args)
	}
	rows = nil // 无权 / 已删除 / 不存在：查询无结果
	ok, err = s.AgentCanReadConversation(context.Background(), "conv-2", 8, false)
	if ok || err != nil {
		t.Fatalf("查询无结果必须拒绝且不报错: ok=%v err=%v", ok, err)
	}
}

func TestAgentCanReadConversationDBError(t *testing.T) {
	s, _ := newFakeStore(t, func(q string, args []driver.Value) fakeResult {
		return fakeResult{err: errors.New("db down")}
	})
	ok, err := s.AgentCanReadConversation(context.Background(), "conv-1", 8, false)
	if ok || err == nil {
		t.Fatalf("数据库错误必须拒绝并返回错误: ok=%v err=%v", ok, err)
	}
}

// 写操作归属规则保持不变：AgentOwnsConversation 仍只允许 agent_id=本人或未分配，不能因已读放宽而扩大。
func TestAgentOwnsConversationUnchanged(t *testing.T) {
	s, f := newFakeStore(t, func(q string, args []driver.Value) fakeResult {
		return fakeResult{cols: cols(1)}
	})
	ok, err := s.AgentOwnsConversation(context.Background(), "conv-1", "8")
	if ok || err != nil {
		t.Fatalf("无结果必须拒绝: ok=%v err=%v", ok, err)
	}
	c := f.snapshot()[0]
	if strings.Contains(c.query, "EXISTS") || strings.Contains(c.query, "visitor_id") ||
		!strings.Contains(c.query, "(agent_id=CAST(? AS UNSIGNED) OR agent_id IS NULL)") {
		t.Fatalf("写操作归属 SQL 被意外修改: %s", c.query)
	}
}
