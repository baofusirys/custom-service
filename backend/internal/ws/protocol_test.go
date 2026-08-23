package ws

import (
	"encoding/json"
	"testing"
)

func TestValidMessageID(t *testing.T) {
	valid := []string{
		"m2-0123456789abcdef0123456789abcdef",
		"550e8400-e29b-41d4-a716-446655440000",
		"local-1724312345678",
	}
	for _, id := range valid {
		if !ValidMessageID(id) {
			t.Fatalf("合法消息 ID 被拒绝: %s", id)
		}
	}
	invalid := []string{"", "../other-message", "contains space", "0123456789012345678901234567890123457"}
	for _, id := range invalid {
		if ValidMessageID(id) {
			t.Fatalf("非法消息 ID 被放行: %q", id)
		}
	}
}

func TestEnvelopeClientIDRoundTrip(t *testing.T) {
	want := Envelope{Type: "ack", ID: "m2-0123456789abcdef0123456789abcdef", ClientID: "local-1724312345678"}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ClientID != want.ClientID {
		t.Fatalf("client_id JSON 往返丢失: %s", got.ClientID)
	}
}
