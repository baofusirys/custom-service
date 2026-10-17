package hall

import (
	"context"
	"encoding/json"
	"strconv"

	"go.uber.org/zap"
)

// [123] 所有客服账号都能切换的大厅开关（契约第 10 节）：总开关、文字聊天、语音、视频。
// 管理员的上限类设置仍走 /api/admin/settings；这里只允许这四个布尔开关，部分更新，修改后与管理员保存设置同样触发广播与强制动作。

const keySwitchLast = "hall_switch_last" // 最后修改人与时间（JSON：{"by":显示名,"at":+08:00 时间}）

// SwitchesView 开关状态。
type SwitchesView struct {
	Enabled      bool   `json:"enabled"`
	ChatEnabled  bool   `json:"chat_enabled"`
	VoiceEnabled bool   `json:"voice_enabled"`
	VideoEnabled bool   `json:"video_enabled"`
	UpdatedBy    string `json:"updated_by"`
	UpdatedAt    string `json:"updated_at"`
}

// SwitchesInput 部分更新（至少一项）。
type SwitchesInput struct {
	Enabled      *bool `json:"enabled"`
	ChatEnabled  *bool `json:"chat_enabled"`
	VoiceEnabled *bool `json:"voice_enabled"`
	VideoEnabled *bool `json:"video_enabled"`
}

type switchLast struct {
	By string `json:"by"`
	At string `json:"at"`
}

func (s *Service) switchesView(ctx context.Context, st Settings) SwitchesView {
	v := SwitchesView{Enabled: st.Enabled, ChatEnabled: st.ChatEnabled, VoiceEnabled: st.VoiceEnabled, VideoEnabled: st.VideoEnabled}
	if m, err := s.store.GetSettingsMap(ctx, []string{keySwitchLast}); err == nil {
		var last switchLast
		if raw := m[keySwitchLast]; raw != "" && json.Unmarshal([]byte(raw), &last) == nil {
			v.UpdatedBy, v.UpdatedAt = last.By, last.At
		}
	}
	return v
}

func requireAgent(op Actor) *Error {
	if op.Visitor || op.AgentID <= 0 {
		return errTargetForbidden
	}
	return nil
}

// Switches 读取开关（任何已登录客服）。
func (s *Service) Switches(ctx context.Context, op Actor) (SwitchesView, error) {
	if e := requireAgent(op); e != nil {
		return SwitchesView{}, e
	}
	return s.switchesView(ctx, s.loadSettings(ctx)), nil
}

// UpdateSwitches 修改开关（任何启用中的客服；只改传入的项）。
func (s *Service) UpdateSwitches(ctx context.Context, op Actor, in *SwitchesInput) (SwitchesView, error) {
	if e := requireAgent(op); e != nil {
		return SwitchesView{}, e
	}
	if in == nil || (in.Enabled == nil && in.ChatEnabled == nil && in.VoiceEnabled == nil && in.VideoEnabled == nil) {
		return SwitchesView{}, withMsg(errBadParam, "参数错误：至少修改一个开关")
	}
	if e := s.allow(ctx, "switches", 30, minute, actorDimension(op)); e != nil {
		return SwitchesView{}, e
	}
	agent, err := s.store.GetAgentByID(ctx, op.AgentID)
	if err != nil {
		return SwitchesView{}, s.internal("hall_switch_agent_lookup_failed", err)
	}
	if agent == nil || !agent.Active {
		return SwitchesView{}, errTargetForbidden
	}
	if s.filterStore == nil {
		return SwitchesView{}, s.internal("hall_switch_store_missing", errNoFilterStore)
	}
	before := s.LoadSettingsFresh(ctx)
	kv := map[string]string{}
	put := func(key string, v *bool) {
		if v != nil {
			kv[key] = strconv.FormatBool(*v)
		}
	}
	put(KeyEnabled, in.Enabled)
	put(KeyChatEnabled, in.ChatEnabled)
	put(KeyVoiceEnabled, in.VoiceEnabled)
	put(KeyVideoEnabled, in.VideoEnabled)
	who := agentDisplayName(agent)
	last, _ := json.Marshal(switchLast{By: who, At: formatTime(s.now())})
	kv[keySwitchLast] = string(last)
	if err := s.filterStore.SetSettings(ctx, kv); err != nil {
		return SwitchesView{}, s.internal("hall_switch_save_failed", err)
	}
	after := s.OnSettingsChanged(ctx, before)
	s.audit.Info("hall_switches_updated", zap.String("event", "hall_switches"), zap.Int64("operator_aid", op.AgentID),
		zap.String("operator", agent.Username), zap.Bool("admin", agent.Role == "admin"),
		zap.Bool("enabled_before", before.Enabled), zap.Bool("enabled", after.Enabled),
		zap.Bool("chat_before", before.ChatEnabled), zap.Bool("chat", after.ChatEnabled),
		zap.Bool("voice_before", before.VoiceEnabled), zap.Bool("voice", after.VoiceEnabled),
		zap.Bool("video_before", before.VideoEnabled), zap.Bool("video", after.VideoEnabled))
	v := s.switchesView(ctx, after)
	if v.UpdatedBy == "" {
		v.UpdatedBy, v.UpdatedAt = who, formatTime(s.now())
	}
	return v, nil
}
