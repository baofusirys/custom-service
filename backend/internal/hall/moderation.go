package hall

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/livekit"
	"github.com/custom-service/backend/internal/ws"
)

// 管理动作（契约 4.2 moderate）。
const (
	ActionMute      = "mute"
	ActionMuteAll   = "mute_all"
	ActionCameraOff = "camera_off"
	ActionKick      = "kick"
)

// ModerateResult 管理动作结果。
type ModerateResult struct {
	Action   string `json:"action"`
	Identity string `json:"identity"`
	Affected int    `json:"affected"`
	Minutes  int    `json:"-"`
}

// Moderate 执行客服管理动作。调用方（handler）负责写审计（含操作者、IP、结果）。
// 返回 (*ModerateResult, *Error)：kick 在 LiveKit 不可用时同时返回结果（禁入已生效）与 503 错误。
func (s *Service) Moderate(ctx context.Context, op Actor, action, identity string, minutes int) (*ModerateResult, error) {
	if op.Visitor || op.AgentID <= 0 {
		return nil, errTargetForbidden
	}
	if e := s.allow(ctx, "moderate", 60, minute, actorDimension(op)); e != nil {
		return nil, e
	}
	switch action {
	case ActionMute, ActionCameraOff, ActionKick:
		if !ValidIdentity(identity) {
			return nil, withMsg(errBadParam, "identity 不合法")
		}
		if identity == agentIdentity(op.AgentID) {
			return nil, withMsg(errTargetForbidden, "不能对自己执行该操作")
		}
		if isAgentIdentity(identity) && !op.Admin {
			return nil, withMsg(errTargetForbidden, "只有管理员可以管理其他客服")
		}
	case ActionMuteAll:
		identity = ""
	default:
		return nil, withMsg(errBadParam, "action 不合法")
	}
	if action == ActionKick {
		if minutes == 0 {
			minutes = defaultKickMinute
		}
		if minutes < 1 || minutes > maxKickMinutes {
			return nil, withMsg(errBadParam, "minutes 必须是 1-1440")
		}
	} else {
		minutes = 0
	}
	st := s.Settings(ctx)
	res := &ModerateResult{Action: action, Identity: identity, Minutes: minutes}
	var err error
	switch action {
	case ActionMute:
		err = s.moderateMute(ctx, identity, st, res)
	case ActionCameraOff:
		err = s.moderateCameraOff(ctx, identity, st, res)
	case ActionKick:
		err = s.moderateKick(ctx, identity, time.Duration(minutes)*time.Minute, res)
	case ActionMuteAll:
		err = s.moderateMuteAll(ctx, st, res)
	}
	s.biz.Info("hall_moderate", zap.String("event", "hall_moderate"), zap.String("action", action), zap.String("target", identity),
		zap.Int64("operator_aid", op.AgentID), zap.Int("minutes", minutes), zap.Int("affected", res.Affected), zap.Bool("ok", err == nil))
	if err != nil {
		return res, err
	}
	return res, nil
}

func (s *Service) moderateMute(ctx context.Context, identity string, st Settings, res *ModerateResult) error {
	if isAgentIdentity(identity) {
		if err := s.state.setAgentMuted(ctx, identity, true); err != nil {
			return s.internal("hall_moderate_state_failed", err)
		}
	} else if _, err := s.state.releaseSlot(ctx, slotSpeaker, identity); err != nil {
		return s.internal("hall_moderate_state_failed", err)
	}
	if _, err := s.syncPermission(ctx, identity, st); err != nil {
		if errors.Is(err, livekit.ErrNotFound) {
			return nil
		}
		s.biz.Warn("hall_moderate_revoke_deferred", zap.String("identity", identity), zap.Error(err))
		return withMsg(errMediaDown, "已释放名额，语音服务恢复后将自动收回麦克风权限")
	}
	res.Affected = 1
	s.notifyTarget(ctx, identity, map[string]any{"action": "muted"})
	return nil
}

func (s *Service) moderateCameraOff(ctx context.Context, identity string, st Settings, res *ModerateResult) error {
	if _, err := s.state.releaseSlot(ctx, slotCamera, identity); err != nil {
		return s.internal("hall_moderate_state_failed", err)
	}
	if _, err := s.syncPermission(ctx, identity, st); err != nil {
		if errors.Is(err, livekit.ErrNotFound) {
			return nil
		}
		s.biz.Warn("hall_moderate_revoke_deferred", zap.String("identity", identity), zap.Error(err))
		return withMsg(errMediaDown, "已释放名额，语音服务恢复后将自动关闭摄像头")
	}
	res.Affected = 1
	s.notifyTarget(ctx, identity, map[string]any{"action": "camera_off"})
	return nil
}

func (s *Service) moderateKick(ctx context.Context, identity string, d time.Duration, res *ModerateResult) error {
	// 先禁入（即使后续 LiveKit 调用失败，也不能再进来；对账会把仍在房间里的禁入者移出）
	if err := s.state.ban(ctx, identity, d); err != nil {
		return s.internal("hall_moderate_ban_failed", err)
	}
	_, _ = s.state.releaseSlot(ctx, slotSpeaker, identity)
	_, _ = s.state.releaseSlot(ctx, slotCamera, identity)
	_ = s.state.removeOnline(ctx, identity)
	// 先在媒体服务移出、再通知本人：若先通知，客户端收到「已被移出」会主动断开，
	// 随后的 RemoveParticipant 因参与者已不在而得到 unavailable（实测 Mac 集成测试），被误报为语音服务故障。
	err := s.media.RemoveParticipant(ctx, s.room, identity, s.now())
	if err != nil && !errors.Is(err, livekit.ErrNotFound) && s.participantGone(ctx, identity) {
		s.biz.Info("hall_moderate_remove_already_left", zap.String("identity", identity), zap.Error(err))
		err = livekit.ErrNotFound
	}
	s.notifyTarget(ctx, identity, map[string]any{"action": "kicked", "minutes": int(d / time.Minute)})
	if err != nil && !errors.Is(err, livekit.ErrNotFound) {
		s.biz.Warn("hall_moderate_remove_deferred", zap.String("identity", identity), zap.Error(err))
		return withMsg(errMediaDown, "已禁止再次进入，语音服务恢复后将自动移出")
	}
	if err == nil {
		res.Affected = 1
	}
	return nil
}

// participantGone 媒体服务里确认该参与者已不在房间（列表查询成功且不含此 identity）。查询失败时返回 false（不能断定）。
func (s *Service) participantGone(ctx context.Context, identity string) bool {
	parts, err := s.media.ListParticipants(ctx, s.room)
	if err != nil {
		return false
	}
	for _, p := range parts {
		if p.Identity == identity {
			return false
		}
	}
	return true
}

func (s *Service) moderateMuteAll(ctx context.Context, st Settings, res *ModerateResult) error {
	targets := map[string]bool{}
	members, err := s.state.slotMembers(ctx, slotSpeaker)
	if err != nil {
		return s.internal("hall_moderate_state_failed", err)
	}
	for _, m := range members {
		targets[m] = true
	}
	if err := s.state.clearSlots(ctx, slotSpeaker); err != nil {
		return s.internal("hall_moderate_state_failed", err)
	}
	parts, listErr := s.media.ListParticipants(ctx, s.room)
	for _, p := range parts {
		if isVisitorIdentity(p.Identity) && (p.HasTrack(livekit.SourceMicrophone) || p.CanPublishSource(livekit.SourceMicrophone)) {
			targets[p.Identity] = true
		}
	}
	failed := 0
	for _, id := range sortedKeys(targets) {
		if !isVisitorIdentity(id) {
			continue
		}
		if _, err := s.syncPermission(ctx, id, st); err != nil {
			if !errors.Is(err, livekit.ErrNotFound) {
				failed++
			}
			continue
		}
		res.Affected++
	}
	s.hub.Broadcast(ctx, &ws.Envelope{Type: ws.HallTypeModeration, To: ws.HallTargetAllVisitors, TS: ws.NowMS(),
		Extra: map[string]any{"action": "mute_all"}})
	if listErr != nil || failed > 0 {
		s.biz.Warn("hall_mute_all_partial", zap.Int("failed", failed), zap.Error(listErr))
		return withMsg(errMediaDown, "已释放全部开麦名额，语音服务恢复后将自动收回麦克风权限")
	}
	return nil
}

// notifyTarget 给被操作者的全部连接发定向通知（访客按 vid 路由，vid 只在服务端/Redis 内部流转）。
func (s *Service) notifyTarget(ctx context.Context, identity string, extra map[string]any) {
	to := ""
	if aid, ok := agentIDFromIdentity(identity); ok {
		to = ws.HallTargetAgentPrefix + itoa64(aid)
	} else {
		recs, err := s.state.participants(ctx, []string{identity})
		if err != nil || recs[identity].VID == "" {
			return
		}
		to = ws.HallTargetVisitorsPrefix + recs[identity].VID
	}
	s.hub.Broadcast(ctx, &ws.Envelope{Type: ws.HallTypeModeration, To: to, TS: ws.NowMS(), Extra: extra})
}

// ============ 客服视角的大厅状态 ============

// StateParticipant 参与者视图（vid 只在客服接口返回）。
type StateParticipant struct {
	Identity string `json:"identity"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	VID      string `json:"vid,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
	MicOn    bool   `json:"mic_on"`
	CameraOn bool   `json:"camera_on"`
	JoinedAt string `json:"joined_at"`
	Blocked  bool   `json:"blocked"`
}

// StateResult 客服 state 接口返回。
type StateResult struct {
	Settings     map[string]any     `json:"settings"`
	Counts       map[string]int     `json:"counts"`
	Participants []StateParticipant `json:"participants"`
	Degraded     bool               `json:"degraded"`
}

// State 客服查看大厅（LiveKit 为权威；不可用时降级为后端缓存）。
func (s *Service) State(ctx context.Context, op Actor) (*StateResult, error) {
	if e := s.allow(ctx, "state", 60, minute, actorDimension(op)); e != nil {
		return nil, e
	}
	st := s.Settings(ctx)
	spk, err := s.state.slotMembers(ctx, slotSpeaker)
	if err != nil {
		return nil, s.internal("hall_state_slots_failed", err)
	}
	cam, err := s.state.slotMembers(ctx, slotCamera)
	if err != nil {
		return nil, s.internal("hall_state_slots_failed", err)
	}
	spkSet, camSet := toSet(spk), toSet(cam)
	res := &StateResult{Settings: st.StateView(), Participants: []StateParticipant{}}
	var ids []string
	byID := map[string]*livekit.ParticipantInfo{}
	parts, listErr := s.media.ListParticipants(ctx, s.room)
	if listErr != nil {
		res.Degraded = true
		s.biz.Warn("hall_state_degraded", zap.Error(listErr))
		ids, err = s.state.onlineMembers(ctx)
		if err != nil {
			return nil, s.internal("hall_state_online_failed", err)
		}
	} else {
		for i := range parts {
			if parts[i].Identity == "" {
				continue
			}
			ids = append(ids, parts[i].Identity)
			byID[parts[i].Identity] = &parts[i]
		}
	}
	sort.Strings(ids)
	recs, err := s.state.participants(ctx, ids)
	if err != nil {
		return nil, s.internal("hall_state_records_failed", err)
	}
	var vids []string
	for _, id := range ids {
		if r, ok := recs[id]; ok && r.VID != "" {
			vids = append(vids, r.VID)
		}
	}
	blocked := map[string]bool{}
	if len(vids) > 0 {
		if b, err := s.store.BlockedVisitorIDs(ctx, vids); err == nil {
			blocked = b
		} else {
			s.biz.Warn("hall_state_blocked_lookup_failed", zap.Error(err))
		}
	}
	for _, id := range ids {
		rec := recs[id]
		p := StateParticipant{Identity: id, Name: rec.Name, Role: "visitor"}
		if isAgentIdentity(id) {
			p.Role = "agent"
			if aid, ok := agentIDFromIdentity(id); ok {
				p.AgentID = itoa64(aid)
			}
		} else if rec.VID != "" {
			p.VID = rec.VID
			p.Blocked = blocked[rec.VID]
		}
		if info := byID[id]; info != nil {
			if info.Name != "" {
				p.Name = info.Name
			}
			p.MicOn = info.Publishing(livekit.SourceMicrophone)
			p.CameraOn = info.Publishing(livekit.SourceCamera)
			if info.JoinedAt > 0 {
				p.JoinedAt = formatTime(time.Unix(info.JoinedAt, 0))
			}
		} else {
			// 降级：只能按名额推断（客服麦克风不占名额，无法推断，记为 false）
			p.MicOn = spkSet[id]
			p.CameraOn = camSet[id]
		}
		res.Participants = append(res.Participants, p)
	}
	res.Counts = map[string]int{"participants": len(ids), "speakers": len(spk), "cameras": len(cam)}
	return res, nil
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
