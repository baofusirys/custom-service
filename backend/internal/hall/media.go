package hall

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/livekit"
	"github.com/custom-service/backend/internal/security"
	"github.com/custom-service/backend/internal/store"
)

// JoinResult 加入大厅返回（契约 4.1 / 4.2）。
type JoinResult struct {
	URL          string       `json:"url"`
	Token        string       `json:"token"`
	Room         string       `json:"room"`
	Identity     string       `json:"identity"`
	Name         string       `json:"name"`
	VideoEnabled bool         `json:"video_enabled"`
	ChatEnabled  bool         `json:"chat_enabled"`
	VoiceEnabled bool         `json:"voice_enabled"`
	MaxSpeakers  int          `json:"max_speakers"`
	MaxCameras   int          `json:"max_cameras"`
	ICEServers   []ICEServer  `json:"ice_servers"`
	History      []MessageDTO `json:"history"`
}

// MediaResult 开麦/摄像头返回。
type MediaResult struct {
	On   bool `json:"on"`
	Used int  `json:"used"`
	Max  int  `json:"max"`
}

type participantMeta struct {
	Role string `json:"role"`
	Code string `json:"code"`
}

// agentDisplayName 客服显示名：客服·<昵称或用户名>（清洗 + 限长 64 字符）。
func agentDisplayName(a *store.Agent) string {
	nick := strings.TrimSpace(security.SanitizeText(a.Nickname))
	if nick == "" {
		nick = security.SanitizeText(a.Username)
	}
	name := "客服·" + nick
	if utf8.RuneCountInString(name) > 64 {
		name = string([]rune(name)[:64])
	}
	return name
}

// Join 签发大厅令牌并返回最近 50 条消息。
func (s *Service) Join(ctx context.Context, a Actor) (*JoinResult, error) {
	if e := s.allow(ctx, "join", 10, minute, actorDimension(a)); e != nil {
		return nil, e
	}
	st := s.Settings(ctx)
	if !st.Enabled {
		return nil, errHallClosed
	}
	identity, code := s.identityOf(a)
	if remain, err := s.state.banRemaining(ctx, identity); err != nil {
		return nil, s.internal("hall_join_ban_check_failed", err)
	} else if remain > 0 {
		s.sec.Info("hall_join_rejected_banned", zap.String("identity", identity))
		return nil, bannedError(remain)
	}
	var (
		name    string
		meta    participantMeta
		sources []string
		rec     participantRecord
	)
	if a.Visitor {
		online, err := s.state.isOnline(ctx, identity)
		if err != nil {
			return nil, s.internal("hall_join_online_check_failed", err)
		}
		if !online {
			n, err := s.state.onlineCount(ctx)
			if err != nil {
				return nil, s.internal("hall_join_count_failed", err)
			}
			if n >= st.MaxParticipants {
				s.biz.Info("hall_join_rejected_full", zap.String("identity", identity), zap.Int("online", n), zap.Int("max", st.MaxParticipants))
				return nil, errHallFull
			}
		}
		name = "访客 " + code
		meta = participantMeta{Role: "visitor", Code: code}
		rec = participantRecord{Role: "visitor", VID: a.VisitorID, Name: name}
	} else {
		agent, err := s.store.GetAgentByID(ctx, a.AgentID)
		if err != nil {
			return nil, s.internal("hall_join_agent_lookup_failed", err)
		}
		if agent == nil || !agent.Active {
			return nil, errTargetForbidden
		}
		name = agentDisplayName(agent)
		meta = participantMeta{Role: "agent", Code: ""}
		rec = participantRecord{Role: "agent", AgentID: strconv.FormatInt(a.AgentID, 10), Name: name}
		// 重新加入即恢复被管理员静音的麦克风权限
		if err := s.state.setAgentMuted(ctx, identity, false); err != nil {
			return nil, s.internal("hall_join_unmute_failed", err)
		}
		if st.VoiceEnabled {
			sources = []string{livekit.GrantSourceMicrophone}
		}
		if st.VideoEnabled {
			if held, err := s.state.holdsSlot(ctx, slotCamera, identity); err == nil && held {
				sources = append(sources, livekit.GrantSourceCamera)
			}
		}
	}
	metaJSON, _ := json.Marshal(meta)
	token, _, err := s.media.IssueToken(livekit.ParticipantToken{
		Room: s.room, Identity: identity, Name: name, Metadata: string(metaJSON), TTL: tokenTTL, PublishSources: sources,
	})
	if err != nil {
		return nil, s.internal("hall_token_issue_failed", err)
	}
	if err := s.state.putParticipant(ctx, identity, rec); err != nil {
		return nil, s.internal("hall_participant_record_failed", err)
	}
	history, err := s.history(ctx, a, "", maxHistory)
	if err != nil {
		return nil, s.internal("hall_history_failed", err)
	}
	s.biz.Info("hall_join", zap.String("event", "hall_join"), zap.String("identity", identity), zap.String("role", meta.Role),
		zap.String("vid", a.VisitorID), zap.Int64("aid", a.AgentID), zap.Int("history", len(history)))
	return &JoinResult{
		URL: s.publicURL, Token: token, Room: s.room, Identity: identity, Name: name,
		VideoEnabled: st.VideoEnabled, ChatEnabled: st.ChatEnabled, VoiceEnabled: st.VoiceEnabled, MaxSpeakers: st.MaxSpeakers, MaxCameras: st.MaxCameras,
		ICEServers: s.ice(identity), History: history,
	}, nil
}

// desiredSources 按当前名额与设置计算应有的发布来源（LiveKit 枚举名）。
// 访客：持有开麦名额才有麦克风；任何人：视频开启且持有摄像头名额才有摄像头；客服：默认有麦克风（被管理员静音除外）。
func (s *Service) desiredSources(ctx context.Context, identity string, st Settings) ([]string, error) {
	var sources []string
	if !st.VoiceEnabled {
		// [123] 语音关闭：任何人都没有麦克风权限
	} else if isAgentIdentity(identity) {
		muted, err := s.state.agentMuted(ctx, identity)
		if err != nil {
			return nil, err
		}
		if !muted {
			sources = append(sources, livekit.SourceMicrophone)
		}
	} else {
		held, err := s.state.holdsSlot(ctx, slotSpeaker, identity)
		if err != nil {
			return nil, err
		}
		if held {
			sources = append(sources, livekit.SourceMicrophone)
		}
	}
	if st.VideoEnabled {
		held, err := s.state.holdsSlot(ctx, slotCamera, identity)
		if err != nil {
			return nil, err
		}
		if held {
			sources = append(sources, livekit.SourceCamera)
		}
	}
	return sources, nil
}

// syncPermission 把 LiveKit 中该身份的发布权限同步为「应有来源」（收回的来源，LiveKit 会立即移除对应轨道）。
func (s *Service) syncPermission(ctx context.Context, identity string, st Settings) (*livekit.ParticipantInfo, error) {
	sources, err := s.desiredSources(ctx, identity, st)
	if err != nil {
		return nil, err
	}
	info, err := s.media.UpdatePermission(ctx, s.room, identity, livekit.PermissionFor(sources))
	if err != nil {
		return nil, err
	}
	// 兜底：极旧客户端不支持服务端撤销发布时，把仍残留的不允许来源轨道静音
	allowed := map[string]bool{}
	for _, src := range sources {
		allowed[src] = true
	}
	for _, t := range info.Tracks {
		if (t.Source == livekit.SourceMicrophone || t.Source == livekit.SourceCamera) && !allowed[t.Source] && !t.Muted && t.Sid != "" {
			if err := s.media.MutePublishedTrack(ctx, s.room, identity, t.Sid, true); err != nil && !errors.Is(err, livekit.ErrNotFound) {
				s.biz.Warn("hall_mute_residual_track_failed", zap.String("identity", identity), zap.String("source", t.Source), zap.Error(err))
			}
		}
	}
	return info, nil
}

// SetMic 访客开/关麦（客服不占名额，没有这个接口）。
func (s *Service) SetMic(ctx context.Context, a Actor, on bool) (*MediaResult, error) {
	if !a.Visitor {
		return nil, errTargetForbidden
	}
	return s.setMedia(ctx, a, slotSpeaker, on)
}

// SetCamera 访客/客服开关摄像头（都占名额）。
func (s *Service) SetCamera(ctx context.Context, a Actor, on bool) (*MediaResult, error) {
	return s.setMedia(ctx, a, slotCamera, on)
}

func (s *Service) setMedia(ctx context.Context, a Actor, kind slotKind, on bool) (*MediaResult, error) {
	if e := s.allow(ctx, "media", 20, minute, actorDimension(a)); e != nil {
		return nil, e
	}
	st := s.Settings(ctx)
	if !st.Enabled {
		return nil, errHallClosed
	}
	identity, _ := s.identityOf(a)
	max := st.MaxSpeakers
	full := errSpeakersFull
	source := livekit.SourceMicrophone
	if kind == slotCamera {
		max, full, source = st.MaxCameras, errCamerasFull, livekit.SourceCamera
		if on && !st.VideoEnabled {
			return nil, errVideoDisabled
		}
	} else if on && !st.VoiceEnabled {
		return nil, errVoiceDisabled
	}
	if on {
		if remain, err := s.state.banRemaining(ctx, identity); err != nil {
			return nil, s.internal("hall_media_ban_check_failed", err)
		} else if remain > 0 {
			return nil, errMediaForbidden
		}
		if a.Visitor {
			blocked, err := s.store.IsVisitorBlocked(ctx, a.VisitorID)
			if err != nil {
				return nil, s.internal("hall_media_block_check_failed", err)
			}
			if blocked {
				s.biz.Info("hall_media_rejected_blocked", zap.String("identity", identity), zap.String("source", source))
				return nil, errMediaForbidden
			}
		}
	}
	unlock, locked, err := s.state.lock(ctx, identity)
	if err != nil {
		return nil, s.internal("hall_media_lock_failed", err)
	}
	if !locked {
		return nil, errTooFrequent
	}
	defer unlock()

	if !on {
		released, err := s.state.releaseSlot(ctx, kind, identity)
		if err != nil {
			return nil, s.internal("hall_media_release_failed", err)
		}
		if _, err := s.syncPermission(ctx, identity, st); err != nil && !errors.Is(err, livekit.ErrNotFound) {
			// 名额已释放；收回权限失败由 30 秒对账兜底（无名额却在发布会被收回）
			s.biz.Warn("hall_media_revoke_deferred", zap.String("identity", identity), zap.String("source", source), zap.Error(err))
		}
		used, _ := s.state.slotCount(ctx, kind)
		s.biz.Info("hall_media_off", zap.String("event", "hall_media_off"), zap.String("identity", identity),
			zap.String("source", source), zap.Bool("released", released), zap.Int("used", used))
		return &MediaResult{On: false, Used: used, Max: max}, nil
	}

	ok, newly, used, err := s.state.acquireSlot(ctx, kind, identity, max)
	if err != nil {
		return nil, s.internal("hall_media_acquire_failed", err)
	}
	if !ok {
		s.biz.Info("hall_media_full", zap.String("identity", identity), zap.String("source", source), zap.Int("used", used), zap.Int("max", max))
		return nil, withData(full, map[string]any{"on": false, "used": used, "max": max})
	}
	if _, err := s.syncPermission(ctx, identity, st); err != nil {
		if newly {
			if _, rerr := s.state.releaseSlot(ctx, kind, identity); rerr != nil {
				s.biz.Error("hall_media_rollback_failed", zap.String("identity", identity), zap.Error(rerr))
			}
		}
		s.biz.Warn("hall_media_grant_failed", zap.String("identity", identity), zap.String("source", source), zap.Error(err))
		return nil, mediaError(err)
	}
	s.biz.Info("hall_media_on", zap.String("event", "hall_media_on"), zap.String("identity", identity),
		zap.String("source", source), zap.Bool("newly", newly), zap.Int("used", used), zap.Int("max", max))
	return &MediaResult{On: true, Used: used, Max: max}, nil
}

// internal 记录内部错误（含堆栈）并返回统一 500，不把细节暴露给前端。
func (s *Service) internal(event string, err error) *Error {
	s.biz.Error(event, zap.Error(err), zap.Stack("stack"))
	return errInternal
}
