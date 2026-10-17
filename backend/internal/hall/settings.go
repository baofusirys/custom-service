package hall

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// 大厅设置项（复用 settings 表与 /api/admin/settings，白名单 + 严格校验）。
const (
	KeyEnabled         = "hall_enabled"
	KeyVideoEnabled    = "hall_video_enabled"
	KeyMaxSpeakers     = "hall_max_speakers"
	KeyMaxCameras      = "hall_max_cameras"
	KeyMaxParticipants = "hall_max_participants"
)

// SettingKeys 全部大厅设置键（管理接口白名单用）。
var SettingKeys = []string{KeyEnabled, KeyVideoEnabled, KeyMaxSpeakers, KeyMaxCameras, KeyMaxParticipants}

// Settings 生效中的大厅设置。
type Settings struct {
	Enabled         bool
	VideoEnabled    bool
	MaxSpeakers     int
	MaxCameras      int
	MaxParticipants int
}

// DefaultSettings 契约第 3 节默认值。
var DefaultSettings = Settings{Enabled: true, VideoEnabled: false, MaxSpeakers: 6, MaxCameras: 2, MaxParticipants: 100}

type intRange struct{ min, max int }

var settingRanges = map[string]intRange{
	KeyMaxSpeakers:     {1, 20},
	KeyMaxCameras:      {0, 6},
	KeyMaxParticipants: {10, 500},
}

var settingLabels = map[string]string{
	KeyEnabled:         "大厅开关",
	KeyVideoEnabled:    "大厅视频开关",
	KeyMaxSpeakers:     "同时开麦访客上限",
	KeyMaxCameras:      "同时开摄像头上限",
	KeyMaxParticipants: "大厅人数上限",
}

// decimalPattern 只接受不带符号、不带前导零的十进制整数（最多 4 位），拒绝 "6.5"、"+6"、"06"、"0x6"。
var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,3})$`)

// IsSettingKey 是否大厅设置键。
func IsSettingKey(key string) bool {
	_, ok := settingLabels[key]
	return ok
}

// ValidateSettingValue 校验并返回规范化后的值；错误信息为中文，可直接返回前端。
func ValidateSettingValue(key, raw string) (string, error) {
	label, ok := settingLabels[key]
	if !ok {
		return "", errors.New("未知的大厅设置项")
	}
	v := strings.TrimSpace(raw)
	switch key {
	case KeyEnabled, KeyVideoEnabled:
		if v != "true" && v != "false" {
			return "", errors.New(label + "只能是 true 或 false")
		}
		return v, nil
	}
	r := settingRanges[key]
	if !decimalPattern.MatchString(v) {
		return "", errors.New(label + "必须是 " + strconv.Itoa(r.min) + "-" + strconv.Itoa(r.max) + " 的整数")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < r.min || n > r.max {
		return "", errors.New(label + "必须是 " + strconv.Itoa(r.min) + "-" + strconv.Itoa(r.max) + " 的整数")
	}
	return strconv.Itoa(n), nil
}

// ParseSettings 从存储值解析；缺失或存量非法值回退默认（不因脏数据把大厅弄坏）。
func ParseSettings(m map[string]string) Settings {
	s := DefaultSettings
	if v, err := ValidateSettingValue(KeyEnabled, m[KeyEnabled]); err == nil {
		s.Enabled = v == "true"
	}
	if v, err := ValidateSettingValue(KeyVideoEnabled, m[KeyVideoEnabled]); err == nil {
		s.VideoEnabled = v == "true"
	}
	if v, err := ValidateSettingValue(KeyMaxSpeakers, m[KeyMaxSpeakers]); err == nil {
		s.MaxSpeakers, _ = strconv.Atoi(v)
	}
	if v, err := ValidateSettingValue(KeyMaxCameras, m[KeyMaxCameras]); err == nil {
		s.MaxCameras, _ = strconv.Atoi(v)
	}
	if v, err := ValidateSettingValue(KeyMaxParticipants, m[KeyMaxParticipants]); err == nil {
		s.MaxParticipants, _ = strconv.Atoi(v)
	}
	return s
}

// BroadcastExtra hall_settings 信封的 extra（契约第 5 节）。
func (s Settings) BroadcastExtra() map[string]any {
	return map[string]any{
		"enabled":       s.Enabled,
		"video_enabled": s.VideoEnabled,
		"max_speakers":  s.MaxSpeakers,
		"max_cameras":   s.MaxCameras,
	}
}

// StateView 客服 state 接口里的 settings（5 项）。
func (s Settings) StateView() map[string]any {
	return map[string]any{
		"enabled":          s.Enabled,
		"video_enabled":    s.VideoEnabled,
		"max_speakers":     s.MaxSpeakers,
		"max_cameras":      s.MaxCameras,
		"max_participants": s.MaxParticipants,
	}
}
