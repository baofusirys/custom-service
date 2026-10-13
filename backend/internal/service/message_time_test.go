package service

import (
	"testing"
	"time"

	"github.com/custom-service/backend/internal/ws"
)

// [110] 落库时间取 Hub 盖的接收时间戳；缺失时回退当前时间。
func TestBuildMsgUsesEnvelopeTimestamp(t *testing.T) {
	ts := int64(1_790_000_000_123)
	m := buildMsg(&ws.Envelope{ID: "id-1", ConvID: "c-1", Content: "hi", TS: ts}, "visitor", "v-1")
	if got := m.CreatedAt.UnixMilli(); got != ts {
		t.Fatalf("CreatedAt=%d want %d", got, ts)
	}
	before := time.Now().Add(-time.Second)
	if m2 := buildMsg(&ws.Envelope{ID: "id-2", ConvID: "c-1", Content: "hi"}, "visitor", "v-1"); m2.CreatedAt.Before(before) {
		t.Fatalf("missing TS should fall back to now, got %v", m2.CreatedAt)
	}
}
