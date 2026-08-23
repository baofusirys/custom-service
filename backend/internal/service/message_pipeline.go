package service

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

const (
	messagePersistTimeout  = 8 * time.Second
	receiptPersistTimeout  = 5 * time.Second
	undeliveredReplayLimit = 200
)

func (s *Service) runBounded(slots chan struct{}, event string, c *ws.Client, messageID string, task func()) bool {
	select {
	case slots <- struct{}{}:
		go func() {
			defer func() {
				<-slots
				if recovered := recover(); recovered != nil {
					s.bizLog.Error(event+"_panic", zap.Any("error", recovered), zap.String("message_id", messageID), zap.Stack("stack"))
					if c != nil {
						c.Send(&ws.Envelope{Type: "error", ID: messageID, Content: "消息处理异常，请重试", TS: ws.NowMS()})
					}
				}
			}()
			task()
		}()
		return true
	default:
		s.secLog.Warn(event+"_queue_full",
			zap.String("message_id", messageID), zap.String("actor", c.ID), zap.String("conn", c.ConnID))
		c.Send(&ws.Envelope{Type: "error", ID: messageID, Content: "服务繁忙，请稍后重试", TS: ws.NowMS()})
		return false
	}
}

// PersistMessageAsync 使用有界并发完成事务持久化；成功后才 ACK 和广播。
func (s *Service) PersistMessageAsync(e *ws.Envelope, c *ws.Client, sender string) {
	snap := *e
	convID, siteID, clientID := c.ConversationID(), c.SiteID, c.ID
	s.runBounded(s.messageSlots, "message_persist", c, snap.ID, func() {
		ctx, cancel := context.WithTimeout(context.Background(), messagePersistTimeout)
		defer cancel()
		if convID == "" && sender == "visitor" {
			conv, err := s.store.OpenOrGetConversation(ctx, siteID, clientID)
			if err != nil {
				s.failMessage(c, &snap, "persist_open_conversation_failed", err)
				return
			}
			convID = conv.ID
			c.SetConversationID(conv.ID)
		}
		snap.ConvID = convID
		message := buildMsg(&snap, sender, clientID)
		inserted, err := s.store.InsertMessageIdempotent(ctx, message)
		if err != nil {
			if errors.Is(err, store.ErrMessageIDConflict) {
				s.secLog.Warn("message_id_payload_conflict",
					zap.String("message_id", snap.ID), zap.String("conv", convID),
					zap.String("actor", sender+":"+clientID), zap.String("conn", c.ConnID))
				c.Send(&ws.Envelope{Type: "error", ID: snap.ID, ClientID: snap.ClientID, ConvID: convID, Content: "消息标识冲突，已拒绝发送", TS: ws.NowMS()})
				return
			}
			s.failMessage(c, &snap, "message_persist_failed", err)
			return
		}

		// ACK 的唯一语义是数据库事务已经成功提交；重复重试也返回同一成功 ACK。
		// agent ACK 按账号同步到所有连接，client_id 让旧 local-* outbox 与服务端稳定 ID 可归并。
		ack := &ws.Envelope{Type: "ack", ID: snap.ID, ClientID: snap.ClientID, ConvID: convID, TS: ws.NowMS(),
			Extra: map[string]any{"status": "persisted", "client_id": snap.ClientID, "duplicate": !inserted}}
		ackTargets := 0
		if sender == "agent" && s.hub != nil {
			ackTargets = s.hub.FanoutToAgent(ctx, clientID, ack)
		} else {
			c.Send(ack)
			ackTargets = 1
		}
		if ackTargets == 0 && c.Send(ack) {
			ackTargets = 1
		}
		if !inserted {
			s.bizLog.Info("message_idempotent_retry",
				zap.String("message_id", snap.ID), zap.String("client_message_id", snap.ClientID),
				zap.String("conv", convID), zap.String("actor", sender+":"+clientID), zap.Int("ack_targets", ackTargets))
			return
		}

		result := ws.FanoutResult{}
		if s.hub != nil {
			result = s.hub.FanoutToConv(ctx, &snap)
		}
		s.bizLog.Info("message_persisted_and_fanout",
			zap.String("trace_id", snap.ID), zap.String("request_id", snap.ID),
			zap.String("message_id", snap.ID), zap.String("client_message_id", snap.ClientID),
			zap.String("conv", convID), zap.String("sender", sender), zap.Int("ack_targets", ackTargets),
			zap.Int("visitor_targets", result.VisitorTargets), zap.Int("visitor_queued", result.VisitorQueued),
			zap.Int("agent_targets", result.AgentTargets), zap.Int("agent_queued", result.AgentQueued))

		if sender == "agent" {
			if err := s.store.MarkAgentReplied(ctx, convID); err != nil {
				s.bizLog.Warn("mark_agent_replied_failed", zap.Error(err), zap.String("conv", convID), zap.String("message_id", snap.ID))
			}
		} else if sender == "visitor" {
			if preview := buildPushPreview(&snap); preview != "" {
				go s.pushVisitorMessageAPNs(preview, clientID)
			}
		}
	})
}

func (s *Service) failMessage(c *ws.Client, message *ws.Envelope, event string, err error) {
	s.bizLog.Error(event, zap.Error(err), zap.String("message_id", message.ID), zap.String("conv", message.ConvID), zap.Stack("stack"))
	c.Send(&ws.Envelope{Type: "error", ID: message.ID, ClientID: message.ClientID, ConvID: message.ConvID, Content: "消息保存失败，请重试", TS: ws.NowMS()})
}

// PersistDeliveryAsync 只在数据库确认目标是当前访客会话中的客服消息后广播 delivery。
func (s *Service) PersistDeliveryAsync(e *ws.Envelope, c *ws.Client) {
	snap := *e
	s.runBounded(s.receiptSlots, "delivery_receipt", c, snap.ID, func() {
		ctx, cancel := context.WithTimeout(context.Background(), receiptPersistTimeout)
		defer cancel()
		if err := s.store.MarkMessageDelivered(ctx, snap.ConvID, snap.ID); err != nil {
			if errors.Is(err, store.ErrReceiptTarget) {
				s.secLog.Warn("delivery_receipt_forbidden", zap.String("message_id", snap.ID), zap.String("conv", snap.ConvID), zap.String("visitor", c.ID))
				return
			}
			s.bizLog.Error("delivery_receipt_persist_failed", zap.Error(err), zap.String("message_id", snap.ID), zap.String("conv", snap.ConvID), zap.Stack("stack"))
			return
		}
		snap.Type, snap.TS = "delivery", ws.NowMS()
		if s.hub != nil {
			s.hub.FanoutToConv(ctx, &snap)
		}
		s.bizLog.Info("message_delivered", zap.String("trace_id", snap.ID), zap.String("message_id", snap.ID), zap.String("conv", snap.ConvID))
	})
}

// PersistReadAsync 用服务端查询到的目标消息时间推进读游标，校验失败绝不广播。
func (s *Service) PersistReadAsync(e *ws.Envelope, c *ws.Client, role string) {
	snap := *e
	s.runBounded(s.receiptSlots, "read_receipt", c, snap.ID, func() {
		ctx, cancel := context.WithTimeout(context.Background(), receiptPersistTimeout)
		defer cancel()
		readAt, err := s.store.ResolveReadReceipt(ctx, snap.ConvID, role, snap.ID, time.Now())
		if err != nil {
			if errors.Is(err, store.ErrReceiptTarget) {
				s.secLog.Warn("read_receipt_forbidden", zap.String("message_id", snap.ID), zap.String("conv", snap.ConvID), zap.String("actor", snap.From))
				return
			}
			s.bizLog.Error("read_receipt_persist_failed", zap.Error(err), zap.String("message_id", snap.ID), zap.String("conv", snap.ConvID), zap.Stack("stack"))
			return
		}
		snap.Type, snap.TS = "read", readAt.UnixMilli()
		if s.hub != nil {
			s.hub.FanoutToConv(ctx, &snap)
		}
		s.bizLog.Info("message_read", zap.String("trace_id", snap.ID), zap.String("message_id", snap.ID), zap.String("conv", snap.ConvID), zap.String("role", role))
	})
}

// ReplayUndeliveredAsync 在访客重连时补发未确认送达的客服消息；Widget 按消息 ID 去重。
func (s *Service) ReplayUndeliveredAsync(c *ws.Client) {
	if c == nil {
		return
	}
	convID := c.ConversationID()
	if convID == "" {
		return
	}
	s.runBounded(s.receiptSlots, "undelivered_replay", c, "", func() {
		ctx, cancel := context.WithTimeout(context.Background(), receiptPersistTimeout)
		defer cancel()
		messages, err := s.store.ListUndeliveredAgentMessages(ctx, convID, undeliveredReplayLimit)
		if err != nil {
			s.bizLog.Error("undelivered_replay_query_failed", zap.Error(err), zap.String("conv", convID), zap.String("visitor", c.ID), zap.Stack("stack"))
			return
		}
		queued := 0
		for _, message := range messages {
			envelope := &ws.Envelope{
				Type: "chat", ID: message.ID, From: "agent:" + message.SenderRef, ConvID: message.ConvID,
				Content: message.Content, TS: message.CreatedAt.UnixMilli(), Priority: 0,
				Extra: map[string]any{"status": "persisted", "replayed": true},
			}
			if message.MediaURL.Valid {
				envelope.MediaURL = message.MediaURL.String
			}
			if message.MediaKind.Valid {
				envelope.MediaKind = message.MediaKind.String
			}
			if message.MediaName.Valid {
				envelope.MediaName = message.MediaName.String
			}
			if message.MediaSize.Valid {
				envelope.MediaSize = message.MediaSize.Int64
			}
			if !c.Send(envelope) {
				break
			}
			queued++
		}
		s.bizLog.Info("undelivered_replay_completed", zap.String("conv", convID), zap.String("visitor", c.ID), zap.Int("found", len(messages)), zap.Int("queued", queued))
	})
}
