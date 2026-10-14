package service

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTruncateUTF8(t *testing.T) {
	long := "page:https://xn--5kroo.com/product.html?id=1&from=" + strings.Repeat("中文", 40)
	got := truncateUTF8(long, 64)
	if len(got) > 64 || !utf8.ValidString(got) || !strings.HasPrefix(got, "page:https://") {
		t.Fatalf("bad truncate: %q (%d bytes)", got, len(got))
	}
	if truncateUTF8("short", 64) != "short" || truncateUTF8("abc", 0) != "" {
		t.Fatal("edge cases")
	}
}

func TestNextBeijingClock(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	before := time.Date(2026, 10, 14, 4, 0, 0, 0, loc)
	if got := nextBeijingClock(before, 4, 30).In(loc); got.Day() != 14 || got.Hour() != 4 || got.Minute() != 30 {
		t.Fatalf("same day expected, got %v", got)
	}
	after := time.Date(2026, 10, 14, 4, 30, 0, 0, loc)
	if got := nextBeijingClock(after, 4, 30).In(loc); got.Day() != 15 {
		t.Fatalf("next day expected, got %v", got)
	}
	utcLate := time.Date(2026, 10, 14, 21, 0, 0, 0, time.UTC) // 北京 10-15 05:00
	if got := nextBeijingClock(utcLate, 4, 30).In(loc); got.Day() != 16 || got.Hour() != 4 {
		t.Fatalf("timezone handling wrong: %v", got)
	}
}
