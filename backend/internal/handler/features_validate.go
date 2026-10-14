package handler

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// [113] 新功能入参校验（纯函数，便于单元测试）。客户端已做同样校验，这里是服务端权威校验。

var (
	errInvalidIDs    = errors.New("invalid ids")
	errTooManyIDs    = errors.New("too many ids")
	errInvalidTags   = errors.New("invalid tags")
	errNoteTooLong   = errors.New("note too long")
	errInvalidQuick  = errors.New("invalid quick reply")
	errInvalidQuery  = errors.New("invalid search query")
	errInvalidClean  = errors.New("invalid auto clean days")
	visitorIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	numericIDPattern = regexp.MustCompile(`^[0-9]{1,18}$`)
)

const (
	maxTags         = 10
	maxTagRunes     = 16
	maxNoteRunes    = 500
	maxQuickTitle   = 32
	maxQuickContent = 1000
	minSearchRunes  = 2
	maxSearchRunes  = 64
	maxReasonRunes  = 100
)

// ValidVisitorID 访客 ID：UUID 等字母数字与中划线，最长 64。
func ValidVisitorID(id string) bool { return visitorIDPattern.MatchString(id) }

// ValidConversationID [115] 会话 ID：服务端生成的 UUID，格式规则与访客 ID 相同（字母数字与中划线，最长 64）。
func ValidConversationID(id string) bool { return visitorIDPattern.MatchString(id) }

// logSafeParam [115] 写安全日志用：路径参数为不可信输入，只保留前 64 字节，防超长参数放大日志。
func logSafeParam(s string) string {
	if len(s) <= 64 {
		return s
	}
	return s[:64] + "…(truncated)"
}

// ParseIDList 去重、去空白、逐个校验格式；数量 1..max。
func ParseIDList(ids []string, max int) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !ValidVisitorID(id) {
			return nil, errInvalidIDs
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, errInvalidIDs
	}
	if len(out) > max {
		return nil, errTooManyIDs
	}
	return out, nil
}

// stripControl 去掉控制字符（保留换行与制表符时 keepNewline=true）。
func stripControl(s string, keepNewline bool) string {
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError {
			return -1
		}
		if keepNewline && (r == '\n' || r == '\t') {
			return r
		}
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == '\u200b' || r == '\ufeff' {
			return -1
		}
		return r
	}, s)
}

// NormalizeTags 标签：去首尾空白、去重，每个 1-16 字，不含尖括号与控制字符，最多 10 个。
func NormalizeTags(tags []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := []string{}
	for _, t := range tags {
		t = strings.TrimSpace(stripControl(t, false))
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > maxTagRunes || strings.ContainsAny(t, "<>") {
			return nil, errInvalidTags
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) > maxTags {
		return nil, errInvalidTags
	}
	return out, nil
}

// NormalizeTagFilter 列表筛选用的单个标签（空表示不筛选）。
func NormalizeTagFilter(tag string) (string, bool) {
	tag = strings.TrimSpace(stripControl(tag, false))
	if tag == "" {
		return "", true
	}
	if utf8.RuneCountInString(tag) > maxTagRunes || strings.ContainsAny(tag, "<>") {
		return "", false
	}
	return tag, true
}

// NormalizeNote 备注：去首尾空白与控制字符（保留换行），≤500 字。
func NormalizeNote(note string) (string, error) {
	note = strings.TrimSpace(stripControl(note, true))
	if utf8.RuneCountInString(note) > maxNoteRunes {
		return "", errNoteTooLong
	}
	return note, nil
}

// NormalizeReason 拉黑原因：≤100 字，超长截断。
func NormalizeReason(reason string) string {
	reason = strings.TrimSpace(stripControl(reason, false))
	if utf8.RuneCountInString(reason) > maxReasonRunes {
		reason = string([]rune(reason)[:maxReasonRunes])
	}
	return reason
}

// NormalizeQuickReply 快捷回复：标题 ≤32 字（可空），内容 1-1000 字。
func NormalizeQuickReply(title, content string) (string, string, error) {
	title = strings.TrimSpace(stripControl(title, false))
	content = strings.TrimSpace(stripControl(content, true))
	if utf8.RuneCountInString(title) > maxQuickTitle || content == "" || utf8.RuneCountInString(content) > maxQuickContent {
		return "", "", errInvalidQuick
	}
	return title, content, nil
}

// NormalizeSearchQuery 搜索词：2-64 字。
func NormalizeSearchQuery(q string) (string, error) {
	q = strings.TrimSpace(stripControl(q, false))
	n := utf8.RuneCountInString(q)
	if n < minSearchRunes || n > maxSearchRunes {
		return "", errInvalidQuery
	}
	return q, nil
}

// ParseAutoCleanDays 自动清理天数："0" 关闭，或 7-3650。
func ParseAutoCleanDays(v string) (string, error) {
	v = strings.TrimSpace(v)
	n, err := strconv.Atoi(v)
	if err != nil || (n != 0 && (n < 7 || n > 3650)) {
		return "", errInvalidClean
	}
	return strconv.Itoa(n), nil
}

// ParseNumericID 解析数字 ID（快捷回复等）。
func ParseNumericID(s string) (int64, bool) {
	if !numericIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}

// pageParams 解析 offset/limit（limit 1-200，默认 50）。
func pageParams(offsetStr, limitStr string) (int, int) {
	offset, _ := strconv.Atoi(offsetStr)
	limit, _ := strconv.Atoi(limitStr)
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return offset, limit
}
