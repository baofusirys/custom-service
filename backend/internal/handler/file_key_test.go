package handler

import "testing"

// [110] /files/*key 的通配参数自带前导斜杠：合法键必须放行，穿越/绝对路径/特殊字符必须拒绝。
func TestNormalizeFileKey(t *testing.T) {
	allowed := map[string]string{
		"/2026/10/14/a9491d0c-105d-4542-a020-1ee26dd739ec.jpg": "2026/10/14/a9491d0c-105d-4542-a020-1ee26dd739ec.jpg",
		"2026/10/14/x.png": "2026/10/14/x.png",
		"/2026/./10/x.pdf": "2026/10/x.pdf",
		"/2026/10//x.txt":  "2026/10/x.txt",
	}
	for raw, want := range allowed {
		got, ok := normalizeFileKey(raw)
		if !ok || got != want {
			t.Fatalf("normalizeFileKey(%q) = %q,%v; want %q,true", raw, got, ok, want)
		}
	}
	rejected := []string{
		"", "/", "//etc/passwd", "/../etc/passwd", "/2026/../../etc/passwd", "/2026/10/..",
		"/a\\b.jpg", "/a\x00.jpg", "/./", "..", "/..",
	}
	for _, raw := range rejected {
		if got, ok := normalizeFileKey(raw); ok {
			t.Fatalf("normalizeFileKey(%q) = %q,true; want rejected", raw, got)
		}
	}
}
