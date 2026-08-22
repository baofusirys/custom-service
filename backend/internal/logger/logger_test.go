package logger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatBeijingTimestampIncludesOffset(t *testing.T) {
	got := formatBeijingTimestamp(time.Date(2026, 8, 22, 9, 30, 1, 123000000, time.UTC))
	want := "2026-08-22T17:30:01.123+08:00"
	if got != want {
		t.Fatalf("北京时间格式错误: got=%s want=%s", got, want)
	}
}

func TestJSONLineHasEnterpriseFieldsAndBeijingTime(t *testing.T) {
	dir := t.TempDir()
	logs, err := Init("info", dir)
	if err != nil {
		t.Fatal(err)
	}
	logs.Business.Info("service started")
	logs.Close()

	raw, err := os.ReadFile(filepath.Join(dir, "business.log"))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(raw))
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("日志不是合法 JSON Lines: %v; raw=%s", err, line)
	}
	for _, key := range []string{"ts", "level", "service", "module", "environment", "message"} {
		if record[key] == nil || record[key] == "" {
			t.Fatalf("日志缺少字段 %s: %s", key, line)
		}
	}
	if record["level"] != "INFO" || record["module"] != "business" {
		t.Fatalf("日志分级或模块错误: %s", line)
	}
	if !strings.HasSuffix(record["ts"].(string), "+08:00") {
		t.Fatalf("日志不是带偏移的北京时间: %s", line)
	}
}
