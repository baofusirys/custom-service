package store

import (
	"database/sql"
	"testing"
	"time"
)

func TestSameMessagePayload(t *testing.T) {
	base := &Message{
		ID: "m2-0123456789abcdef0123456789abcdef", ConvID: "conv-a",
		Sender: "agent", SenderRef: "7", Content: "hello",
		MediaURL: sql.NullString{String: "/files/a.png", Valid: true},
	}
	same := *base
	if !sameMessagePayload(base, &same) {
		t.Fatal("相同 ID 与 payload 应当允许幂等重试")
	}
	changed := same
	changed.Content = "tampered"
	if sameMessagePayload(base, &changed) {
		t.Fatal("相同 ID 携带不同 payload 必须被识别为冲突")
	}
	changed = same
	changed.ConvID = "conv-b"
	if sameMessagePayload(base, &changed) {
		t.Fatal("相同 ID 跨会话重放必须被识别为冲突")
	}
}

func TestSetMessageStatusRejectsLegacyDeliveredFalsePositive(t *testing.T) {
	legacy := &Message{ID: "local-123", DeliveredWS: true}
	setMessageStatus(legacy)
	if legacy.Status != "persisted" {
		t.Fatalf("旧 delivered_ws 不可信，实际状态=%s", legacy.Status)
	}
	current := &Message{ID: "m2-0123456789abcdef0123456789abcdef", DeliveredWS: true}
	setMessageStatus(current)
	if current.Status != "delivered" {
		t.Fatalf("新协议 delivery ACK 应映射 delivered，实际状态=%s", current.Status)
	}
	current.Read = true
	setMessageStatus(current)
	if current.Status != "read" {
		t.Fatalf("read 必须是最高状态，实际状态=%s", current.Status)
	}
}

func TestReadCursorNeverMovesBackward(t *testing.T) {
	current := time.Date(2026, 8, 22, 18, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	older := current.Add(-time.Minute)
	newer := current.Add(time.Minute)
	if got := monotonicReadTime(sql.NullTime{Time: current, Valid: true}, older); !got.Equal(current) {
		t.Fatalf("乱序旧回执导致游标回退: got=%s want=%s", got, current)
	}
	if got := monotonicReadTime(sql.NullTime{Time: current, Valid: true}, newer); !got.Equal(newer) {
		t.Fatalf("新回执没有推进游标: got=%s want=%s", got, newer)
	}
}
