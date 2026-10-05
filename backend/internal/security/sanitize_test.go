package security

import (
	"strings"
	"testing"
)

func TestSanitizeTextAndRedactJSONPayload(t *testing.T) {
	if got := SanitizeText(`<img src=x onerror=alert(1)>客服`); got != "客服" {
		t.Fatalf("XSS 标签未清除: %q", got)
	}
	redacted := string(RedactJSONPayload([]byte(`{"token":"abc","nested":{"password":"p"},"content":"ok"}`)))
	if strings.Contains(redacted, "abc") || strings.Contains(redacted, `"p"`) || !strings.Contains(redacted, "REDACTED") {
		t.Fatalf("JSON 脱敏失败: %s", redacted)
	}
}

func TestValidateUpload(t *testing.T) {
	valid, err := ValidateUpload("photo.jpg", "image/jpeg", []byte{0xff, 0xd8, 0xff, 0x00})
	if err != nil || valid.Kind != "image" {
		t.Fatalf("合法图片被拒: %+v %v", valid, err)
	}
	for _, tc := range []struct {
		name   string
		mime   string
		header []byte
	}{
		{"evil.html", "text/html", []byte("<script>alert(1)</script>")},
		{"photo.jpg", "image/jpeg", []byte("not-a-jpeg")},
		{"evil.svg", "image/svg+xml", []byte("<svg onload=alert(1)>")},
	} {
		if _, err := ValidateUpload(tc.name, tc.mime, tc.header); err == nil {
			t.Fatalf("危险或伪装文件被放行: %s", tc.name)
		}
	}
}

func TestValidatePublicURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/admin", "http://169.254.169.254/latest", "javascript:alert(1)", "http://localhost/"} {
		if got, err := ValidatePublicURL(raw); err == nil || got != "" {
			t.Fatalf("SSRF/危险 URL 被放行: %q -> %q %v", raw, got, err)
		}
	}
}

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
