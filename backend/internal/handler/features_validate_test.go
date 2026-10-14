package handler

import (
	"strings"
	"testing"
)

func TestParseIDList(t *testing.T) {
	ids, err := ParseIDList([]string{" a-1 ", "a-1", "B2", ""}, 5)
	if err != nil || len(ids) != 2 || ids[0] != "a-1" {
		t.Fatalf("got %v %v", ids, err)
	}
	if _, err := ParseIDList([]string{"x' OR 1=1 --"}, 5); err != errInvalidIDs {
		t.Fatal("注入字符必须拒绝")
	}
	if _, err := ParseIDList([]string{}, 5); err != errInvalidIDs {
		t.Fatal("空列表必须拒绝")
	}
	if _, err := ParseIDList([]string{"a", "b", "c"}, 2); err != errTooManyIDs {
		t.Fatal("超出上限必须拒绝")
	}
	if _, err := ParseIDList([]string{strings.Repeat("a", 65)}, 2); err != errInvalidIDs {
		t.Fatal("超长 ID 必须拒绝")
	}
}

func TestNormalizeTags(t *testing.T) {
	tags, err := NormalizeTags([]string{" VIP ", "VIP", "已付款", "", "待发货\u200b"})
	if err != nil || strings.Join(tags, ",") != "VIP,已付款,待发货" {
		t.Fatalf("got %v %v", tags, err)
	}
	if _, err := NormalizeTags([]string{"<script>"}); err == nil {
		t.Fatal("尖括号必须拒绝")
	}
	if _, err := NormalizeTags([]string{strings.Repeat("长", 17)}); err == nil {
		t.Fatal("超过 16 字必须拒绝")
	}
	many := make([]string, 11)
	for i := range many {
		many[i] = string(rune('a' + i))
	}
	if _, err := NormalizeTags(many); err == nil {
		t.Fatal("超过 10 个必须拒绝")
	}
}

func TestNormalizeNoteQuickSearchClean(t *testing.T) {
	if n, err := NormalizeNote("  第一行\n第二行\x00  "); err != nil || n != "第一行\n第二行" {
		t.Fatalf("note %q %v", n, err)
	}
	if _, err := NormalizeNote(strings.Repeat("字", 501)); err == nil {
		t.Fatal("备注超长必须拒绝")
	}
	if _, _, err := NormalizeQuickReply("标题", "   "); err == nil {
		t.Fatal("空内容必须拒绝")
	}
	if _, _, err := NormalizeQuickReply(strings.Repeat("t", 33), "x"); err == nil {
		t.Fatal("标题超长必须拒绝")
	}
	if ti, co, err := NormalizeQuickReply(" 付款 ", " 支持支付宝\n和微信 "); err != nil || ti != "付款" || co != "支持支付宝\n和微信" {
		t.Fatalf("quick %q %q %v", ti, co, err)
	}
	for _, bad := range []string{"a", " ", strings.Repeat("q", 65)} {
		if _, err := NormalizeSearchQuery(bad); err == nil {
			t.Fatalf("搜索词 %q 应拒绝", bad)
		}
	}
	if q, err := NormalizeSearchQuery(" 20261013-8891 "); err != nil || q != "20261013-8891" {
		t.Fatalf("search %q %v", q, err)
	}
	for in, want := range map[string]string{"0": "0", "7": "7", " 30 ": "30", "3650": "3650"} {
		if got, err := ParseAutoCleanDays(in); err != nil || got != want {
			t.Fatalf("clean %q → %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"6", "3651", "-1", "abc", ""} {
		if _, err := ParseAutoCleanDays(bad); err == nil {
			t.Fatalf("clean %q 应拒绝", bad)
		}
	}
	if NormalizeReason(strings.Repeat("骚", 120)) != strings.Repeat("骚", 100) {
		t.Fatal("原因应截断到 100 字")
	}
	if _, ok := ParseNumericID("12a"); ok {
		t.Fatal("非数字 ID 应拒绝")
	}
	if tag, ok := NormalizeTagFilter(" VIP "); !ok || tag != "VIP" {
		t.Fatal("标签筛选")
	}
}
