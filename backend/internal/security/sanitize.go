package security

import (
	"net/url"
	"regexp"
	"strings"

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
			lower == "key" || strings.HasSuffix(lower, "_key") {
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
