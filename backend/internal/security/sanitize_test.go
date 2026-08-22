package security

import (
	"strings"
	"testing"
)

func TestRedactRawQuery(t *testing.T) {
	raw := "token=header.payload.signature&site=default&refresh_token=another-secret"
	redacted := RedactRawQuery(raw)
	if strings.Contains(redacted, "header.payload.signature") || strings.Contains(redacted, "another-secret") {
		t.Fatalf("敏感 query 未脱敏: %s", redacted)
	}
	if !strings.Contains(redacted, "site=default") || !strings.Contains(redacted, "%5BREDACTED%5D") {
		t.Fatalf("脱敏后缺少必要上下文: %s", redacted)
	}
}

func TestValidTraceID(t *testing.T) {
	if !ValidTraceID("req-0123456789abcdef") {
		t.Fatal("合法 trace id 被拒")
	}
	for _, value := range []string{"short", "line\nbreak", "../../escape", strings.Repeat("a", 65)} {
		if ValidTraceID(value) {
			t.Fatalf("非法 trace id 被放行: %q", value)
		}
	}
}
