package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
)

// 防 SQL 注入 / XSS（爷爷铁律）：
//
//   - SQL 注入：100% 走预编译参数（database/sql 的 ? 占位符 + DSN 关 interpolateParams）。
//     这里 DetectSQLInjection 只做「上报式」检测：发现可疑 payload 上报安全日志 + 触发拉黑计数，
//     而不依赖检测器来拦截 —— 真正防线是参数化查询。
//
//   - XSS：所有用户文本走 bluemonday StrictPolicy（剥光所有 HTML 标签），
//     展示侧再做转义；图片/文件走独立通道，永不内嵌 HTML。

var sqlInjectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(union\s+select)`),
	regexp.MustCompile(`(?i)(select\s+.*\s+from\s+)`),
	regexp.MustCompile(`(?i)(insert\s+into\s+)`),
	regexp.MustCompile(`(?i)(update\s+\S+\s+set\s+)`),
	regexp.MustCompile(`(?i)(delete\s+from\s+)`),
	regexp.MustCompile(`(?i)(drop\s+(table|database)\s+)`),
	regexp.MustCompile(`(?i)(exec\s*\()`),
	regexp.MustCompile(`(?i)(--|#|/\*|\*/)`),
	regexp.MustCompile(`(?i)('\s+or\s+'?\d|'\s+or\s+1=1)`),
	regexp.MustCompile(`(?i)(load_file|outfile|into\s+outfile)`),
}

// RedactRawQuery 保留请求参数名与非敏感值，同时固定脱敏 token/密码/密钥类字段。
// WebSocket JWT 绝不允许以完整 query 进入长期日志。
func RedactRawQuery(raw string) string {
	if raw == "" {
		return ""
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "[malformed-query]"
	}
	for key := range values {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "password") ||
			strings.Contains(lower, "secret") || strings.Contains(lower, "credential") ||
			strings.Contains(lower, "access") || lower == "key" || strings.HasSuffix(lower, "_key") {
			values.Set(key, "[REDACTED]")
		}
	}
	return values.Encode()
}

// ValidTraceID 只接受短小的可打印标识；非法外部值由中间件替换为服务端 UUID。
func ValidTraceID(value string) bool {
	if len(value) < 8 || len(value) > 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || b == '-' || b == '_' {
			continue
		}
		return false
	}
	return true
}

func DetectSQLInjection(s string) (bool, string) {
	for _, re := range sqlInjectionPatterns {
		if re.MatchString(s) {
			return true, re.String()
		}
	}
	return false, ""
}

var strictHTML = bluemonday.StrictPolicy()

// SanitizeText 把任何用户输入清洗为纯文本，去掉一切 HTML 标签和 JS。
// 同时把零宽字符、控制字符替换为空。
func SanitizeText(s string) string {
	s = strictHTML.Sanitize(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteRune(r)
		case r < 0x20:
			// 跳过其他控制字符
		case r == 0x200B || r == 0x200C || r == 0x200D || r == 0xFEFF:
			// 零宽字符
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func SafeMediaURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/files/") || strings.Contains(u.Path, "..") {
		return ""
	}
	for key := range u.Query() {
		if key != "access" {
			return ""
		}
	}
	return u.EscapedPath() + func() string {
		if u.RawQuery == "" {
			return ""
		}
		return "?" + u.RawQuery
	}()
}

// RedactJSONPayload 在日志写入前脱敏认证秘密。无法解析的报文只保留长度，
// 避免 malformed payload 中夹带 Cookie、JWT 或密码进入原始日志。
func RedactJSONPayload(raw []byte) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []byte(fmt.Sprintf("[malformed-json length=%d]", len(raw)))
	}
	redactJSONValue(v)
	out, err := json.Marshal(v)
	if err != nil {
		return []byte(fmt.Sprintf("[unmarshalable-json length=%d]", len(raw)))
	}
	return out
}

func redactJSONValue(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, value := range x {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "token") || strings.Contains(lk, "password") ||
				strings.Contains(lk, "secret") || strings.Contains(lk, "cookie") ||
				strings.Contains(lk, "credential") || strings.HasSuffix(lk, "_key") || lk == "key" || lk == "authorization" {
				x[k] = "[REDACTED]"
				continue
			}
			if s, ok := value.(string); ok && (lk == "media" || lk == "media_url" || lk == "url" || lk == "href") && strings.Contains(s, "access=") {
				if u, err := url.Parse(s); err == nil {
					u.RawQuery = RedactRawQuery(u.RawQuery)
					x[k] = u.String()
					continue
				}
			}
			redactJSONValue(value)
		}
	case []any:
		for _, item := range x {
			redactJSONValue(item)
		}
	}
}

// ValidatePublicURL 校验用户提供的站点/回调/页面地址，拒绝 SSRF 常见目标。
// 当前服务不会根据这些地址主动发起请求，但先在边界阻断可疑地址，避免后续功能扩展形成 SSRF。
func ValidatePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > 2048 {
		return "", errors.New("url too long")
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return "", errors.New("invalid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("unsupported url scheme")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "metadata.google.internal" || host == "instance-data" {
		return "", errors.New("private host blocked")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateOrReservedIP(ip) {
			return "", errors.New("private ip blocked")
		}
		return u.String(), nil
	}
	// DNS rebinding/内网域名防护：解析结果全部必须是公网地址。
	ctx, cancel := contextWithTimeout(2 * time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return "", errors.New("host cannot be resolved")
	}
	for _, ip := range ips {
		if isPrivateOrReservedIP(ip) {
			return "", errors.New("resolved private ip blocked")
		}
	}
	return u.String(), nil
}

// contextWithTimeout 独立封装，保持安全包的调用点易测且不暴露可变全局配置。
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func isPrivateOrReservedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	// 云厂商 metadata 常见地址及 CGNAT/保留段。
	reserved := []*net.IPNet{
		{IP: net.ParseIP("169.254.169.254"), Mask: net.CIDRMask(32, 32)},
		{IP: net.ParseIP("100.64.0.0"), Mask: net.CIDRMask(10, 32)},
		{IP: net.ParseIP("192.0.0.0"), Mask: net.CIDRMask(24, 32)},
		{IP: net.ParseIP("198.18.0.0"), Mask: net.CIDRMask(15, 32)},
		{IP: net.ParseIP("198.51.100.0"), Mask: net.CIDRMask(24, 32)},
		{IP: net.ParseIP("203.0.113.0"), Mask: net.CIDRMask(24, 32)},
	}
	for _, n := range reserved {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

type UploadType struct {
	Extension string
	MIME      string
	Kind      string
}

// ValidateUpload 同时校验扩展名、服务端嗅探 MIME 和文件 magic bytes。
// HTML/SVG/脚本扩展名不在白名单；历史上已经落盘的危险扩展名由 ServeFile 强制下载。
func ValidateUpload(filename, detectedMIME string, header []byte) (UploadType, error) {
	name := filepath.Base(strings.TrimSpace(filename))
	if name == "." || name == "" || strings.ContainsAny(name, "\x00\r\n") {
		return UploadType{}, errors.New("invalid filename")
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".jpeg" {
		ext = ".jpg"
	}
	t := UploadType{Extension: ext, MIME: strings.ToLower(strings.TrimSpace(detectedMIME))}
	match := func(mime string, magic ...byte) bool {
		if t.MIME != mime || len(header) < len(magic) || !bytes.Equal(header[:len(magic)], magic) {
			return false
		}
		return true
	}
	switch {
	case ext == ".jpg" && match("image/jpeg", 0xff, 0xd8, 0xff):
		t.Kind = "image"
	case ext == ".png" && match("image/png", 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a):
		t.Kind = "image"
	case ext == ".gif" && t.MIME == "image/gif" && len(header) >= 6 && (bytes.Equal(header[:6], []byte("GIF87a")) || bytes.Equal(header[:6], []byte("GIF89a"))):
		t.Kind = "image"
	case ext == ".webp" && t.MIME == "image/webp" && len(header) >= 12 && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP")):
		t.Kind = "image"
	case ext == ".pdf" && match("application/pdf", '%', 'P', 'D', 'F', '-'):
		t.Kind = "file"
	case ext == ".zip" && (t.MIME == "application/zip" || t.MIME == "application/x-zip-compressed") && len(header) >= 4 && bytes.Equal(header[:4], []byte("PK\x03\x04")):
		t.Kind = "file"
	case (ext == ".docx" || ext == ".xlsx") && (t.MIME == "application/zip" || t.MIME == "application/x-zip-compressed") && len(header) >= 4 && bytes.Equal(header[:4], []byte("PK\x03\x04")):
		t.Kind = "file"
	case (ext == ".doc" || ext == ".xls") && (t.MIME == "application/x-ole-storage" || t.MIME == "application/octet-stream") && len(header) >= 8 && bytes.Equal(header[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}):
		t.Kind = "file"
	case ext == ".txt" && t.MIME == "text/plain" && len(header) > 0:
		t.Kind = "file"
	default:
		return UploadType{}, errors.New("extension mime magic mismatch")
	}
	return t, nil
}
