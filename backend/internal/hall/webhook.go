package hall

import (
	"context"
	"errors"
	"regexp"

	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/livekit"
)

// ErrWebhookUnauthorized 回调签名/摘要/时效校验失败（handler 返回 401 40120）。
var ErrWebhookUnauthorized = errors.New("hall: webhook unauthorized")

// webhookIDPattern 事件 ID 白名单（LiveKit 形如 EV_xxx），用于 Redis 去重键，防止注入超长/异常键。
var webhookIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// HandleWebhook 同步验签（失败立即拒绝），通过后把处理放进有界后台任务并立即返回（LiveKit 期望快速 2xx）。
func (s *Service) HandleWebhook(ctx context.Context, authHeader string, body []byte) error {
	ev, err := s.media.VerifyWebhook(authHeader, body)
	if err != nil {
		s.sec.Warn("hall_webhook_rejected", zap.String("event", "hall_webhook_rejected"), zap.String("reason", webhookReason(err)))
		return ErrWebhookUnauthorized
	}
	if ev.RoomName != "" && ev.RoomName != s.room {
		s.biz.Info("hall_webhook_ignored_room", zap.String("webhook_event", ev.Event), zap.String("room", ev.RoomName))
		return nil
	}
	if ev.ID != "" && !webhookIDPattern.MatchString(ev.ID) {
		s.sec.Warn("hall_webhook_bad_event_id", zap.Int("id_length", len(ev.ID)))
		return nil
	}
	first, err := s.state.markWebhookEvent(ctx, ev.ID)
	if err != nil {
		s.biz.Warn("hall_webhook_dedup_unavailable", zap.Error(err))
	} else if !first {
		s.biz.Info("hall_webhook_duplicate", zap.String("webhook_event", ev.Event), zap.String("event_id", ev.ID))
		return nil
	}
	evCopy := *ev
	if !s.runBackground("webhook", webhookTimeout, func(bctx context.Context) { s.processWebhook(bctx, &evCopy) }) {
		s.biz.Warn("hall_webhook_dropped_busy", zap.String("webhook_event", ev.Event), zap.String("event_id", ev.ID))
	}
	return nil
}

func webhookReason(err error) string {
	switch {
	case errors.Is(err, livekit.ErrWebhookNoAuth):
		return "missing_authorization"
	case errors.Is(err, livekit.ErrWebhookChecksum):
		return "checksum_mismatch"
	case errors.Is(err, livekit.ErrWebhookPayload):
		return "bad_payload"
	case errors.Is(err, livekit.ErrDisabled), errors.Is(err, livekit.ErrKeysMissing):
		return "not_configured"
	default:
		return "invalid_token"
	}
}

func (s *Service) processWebhook(ctx context.Context, ev *livekit.WebhookEvent) {
	identity := ""
	if ev.Participant != nil {
		identity = ev.Participant.Identity
	}
	fields := []zap.Field{zap.String("event", "hall_webhook"), zap.String("webhook_event", ev.Event),
		zap.String("event_id", ev.ID), zap.String("identity", identity)}
	switch ev.Event {
	case "participant_joined":
		if !ValidIdentity(identity) {
			s.sec.Warn("hall_unknown_participant", append(fields, zap.String("note", "identity 不是本系统签发格式"))...)
			return
		}
		_ = s.state.addOnline(ctx, identity)
		st := s.Settings(ctx)
		remain, _ := s.state.banRemaining(ctx, identity)
		if !st.Enabled || remain > 0 {
			err := s.media.RemoveParticipant(ctx, s.room, identity, s.now())
			s.biz.Info("hall_participant_removed_on_join", append(fields, zap.Bool("hall_enabled", st.Enabled), zap.Bool("banned", remain > 0), zap.Error(err))...)
			return
		}
		s.biz.Info("hall_participant_joined", fields...)
	case "participant_left", "participant_connection_aborted":
		_ = s.state.removeOnline(ctx, identity)
		spk, _ := s.state.releaseSlot(ctx, slotSpeaker, identity)
		cam, _ := s.state.releaseSlot(ctx, slotCamera, identity)
		s.biz.Info("hall_participant_left", append(fields, zap.Bool("released_mic", spk), zap.Bool("released_camera", cam))...)
	case "track_published":
		if ev.Track == nil || !ValidIdentity(identity) {
			return
		}
		st := s.Settings(ctx)
		revoke := false
		switch ev.Track.Source {
		case livekit.SourceCamera:
			held, err := s.state.holdsSlot(ctx, slotCamera, identity)
			revoke = !st.VideoEnabled || (err == nil && !held)
		case livekit.SourceMicrophone:
			if !st.VoiceEnabled {
				revoke = true
				_, _ = s.state.releaseSlot(ctx, slotSpeaker, identity)
			} else if isVisitorIdentity(identity) {
				held, err := s.state.holdsSlot(ctx, slotSpeaker, identity)
				revoke = err == nil && !held
			} else if muted, err := s.state.agentMuted(ctx, identity); err == nil && muted {
				revoke = true
			}
		case "SCREEN_SHARE", "SCREEN_SHARE_AUDIO":
			revoke = true // 本系统从不授予屏幕共享
		}
		if revoke {
			if !st.VideoEnabled && ev.Track.Source == livekit.SourceCamera {
				_, _ = s.state.releaseSlot(ctx, slotCamera, identity)
			}
			_, err := s.syncPermission(ctx, identity, st)
			s.sec.Warn("hall_track_revoked", append(fields, zap.String("source", ev.Track.Source), zap.Bool("video_enabled", st.VideoEnabled), zap.Error(err))...)
			return
		}
		s.biz.Info("hall_track_published", append(fields, zap.String("source", ev.Track.Source))...)
	case "track_unpublished":
		src := ""
		if ev.Track != nil {
			src = ev.Track.Source
		}
		s.biz.Info("hall_track_unpublished", append(fields, zap.String("source", src))...)
	case "room_finished":
		_ = s.state.clearOnline(ctx)
		_ = s.state.clearSlots(ctx, slotSpeaker)
		_ = s.state.clearSlots(ctx, slotCamera)
		s.biz.Info("hall_room_finished", fields...)
	default:
		s.biz.Debug("hall_webhook_ignored", fields...)
	}
}
