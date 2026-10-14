package store

import (
	"strings"
)

// inPlaceholders 生成 "?,?,?" 占位符与参数切片（值来自已校验的 ID 列表，仍走参数化，杜绝注入）。
func inPlaceholders(ids []string) (string, []any) {
	if len(ids) == 0 {
		return "", nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

// uniqueNonEmpty 去重并去掉空串，保持原顺序。
func uniqueNonEmpty(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// chunkStrings 把列表按 size 分批，控制单条 SQL 的 IN 列表长度与事务大小（保护数据库与内存）。
func chunkStrings(items []string, size int) [][]string {
	if size <= 0 {
		size = 200
	}
	var out [][]string
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[start:end])
	}
	return out
}
