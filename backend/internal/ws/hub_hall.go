package ws

import (
	"context"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// [119] 多人大厅的 Hub 能力：hall_* 上行帧交给 HallHandler（有界异步，不阻塞主循环）；
// 大厅广播只投递给已订阅大厅的连接，多副本经同一 Redis 频道（与 FanoutToConv 同机制）。

// HallHandler 由 hall 包实现。HandleHallFrame 在 Hub 主循环里被直接调用，实现必须立即返回（内部用有界工作池）。
type HallHandler interface {
	HandleHallFrame(c *Client, e *Envelope)
}

// 下行信封类型（服务端 → 客户端）。
const (
	HallTypeChat       = "hall_chat"
	HallTypeDeleted    = "hall_deleted"
	HallTypeSettings   = "hall_settings"
	HallTypeModeration = "hall_moderation"
)

// hall_moderation 的路由目标（只在服务端/Redis 内部使用，投递给客户端前清空）。
const (
	HallTargetVisitorsPrefix = "visitor:"
	HallTargetAgentPrefix    = "agent:"
	HallTargetAllVisitors    = "hall:visitors"
)

// hallSubs connID -> *Client（全部房间共用；当前只有 hall_default）。
type hallRegistry struct {
	subs sync.Map
}

// SetHallHandler 注入大厅处理器；必须在 Run 之前调用。
func (h *Hub) SetHallHandler(hh HallHandler) { h.hall = hh }

// HallSubscribe 把连接加入大厅广播集合。与注销并发时以 closed 标志兜底，保证不残留已关闭连接。
func (h *Hub) HallSubscribe(c *Client) {
	if c == nil || c.closed.Load() {
		return
	}
	h.hallReg.subs.Store(c.ConnID, c)
	if c.closed.Load() {
		h.hallReg.subs.Delete(c.ConnID)
	}
}

// HallUnsubscribe 退出大厅广播集合。
func (h *Hub) HallUnsubscribe(c *Client) {
	if c != nil {
		h.hallReg.subs.Delete(c.ConnID)
	}
}

// HallSubscribed 连接是否已订阅大厅。
func (h *Hub) HallSubscribed(c *Client) bool {
	if c == nil {
		return false
	}
	_, ok := h.hallReg.subs.Load(c.ConnID)
	return ok
}

// HallSubscriberCount 本节点大厅订阅连接数（监控用）。
func (h *Hub) HallSubscriberCount() int {
	n := 0
	h.hallReg.subs.Range(func(_, _ any) bool { n++; return true })
	return n
}

// BroadcastHall 本节点投递 + Redis 跨节点广播。只能由工作协程调用（不在 Hub 主循环里调用）。
func (h *Hub) BroadcastHall(ctx context.Context, e *Envelope) {
	if e == nil || !strings.HasPrefix(e.Type, "hall_") {
		return
	}
	e.Priority = 0
	e.Node = h.cfg.NodeID
	h.fanoutHallLocal(e)
	if h.rdb != nil {
		if err := h.rdb.Publish(ctx, h.pub, mustJSON(e)).Err(); err != nil {
			h.bizLog.Error("hall_redis_publish_failed", zap.Error(err), zap.String("type", e.Type), zap.String("msg_id", e.ID))
		}
	}
}

// handleHallIncoming 主循环内的大厅上行帧入口：只接受三种上行类型，其余 hall_* 视为伪造下行帧丢弃。
func (h *Hub) handleHallIncoming(c *Client, e *Envelope) {
	switch e.Type {
	case "hall_subscribe", "hall_unsubscribe", "hall_chat":
	default:
		h.secLog.Warn("hall_frame_type_rejected", zap.String("conn", c.ConnID), zap.String("actor", c.ID), zap.String("type", safeConvForLog(e.Type)))
		return
	}
	if h.hall == nil {
		c.Send(&Envelope{Type: "error", ID: safeFrameID(e.ID), Content: "大厅暂未开放", TS: NowMS()})
		return
	}
	h.hall.HandleHallFrame(c, e)
}

// safeFrameID 回显给客户端的帧 ID：只回显合法格式，防止把任意字符串反射回去。
func safeFrameID(id string) string {
	if ValidMessageID(id) {
		return id
	}
	return ""
}

// fanoutHallLocal 本节点投递（也用于 Redis 跨节点回环）。只读 sync.Map + 非阻塞 Send，可在任意协程调用。
func (h *Hub) fanoutHallLocal(e *Envelope) {
	switch e.Type {
	case HallTypeChat, HallTypeDeleted:
		h.hallReg.subs.Range(func(_, v any) bool {
			v.(*Client).Send(e)
			return true
		})
	case HallTypeSettings:
		// 发给所有在线访客与客服（不要求已订阅）：访客按连接索引去重，客服按账号下全部连接
		sent := make(map[string]struct{}, 64)
		h.visitors.Range(func(_, v any) bool {
			c := v.(*Client)
			sent[c.ConnID] = struct{}{}
			c.Send(e)
			return true
		})
		h.hallReg.subs.Range(func(_, v any) bool {
			c := v.(*Client)
			if _, dup := sent[c.ConnID]; !dup && c.Kind == KindVisitor {
				sent[c.ConnID] = struct{}{}
				c.Send(e)
			}
			return true
		})
		h.agents.Range(func(_, v any) bool {
			v.(*sync.Map).Range(func(_, cv any) bool {
				cv.(*Client).Send(e)
				return true
			})
			return true
		})
		if !hallSettingsEnabled(e) {
			// 大厅关闭：清空本节点订阅集合，之后的 hall_chat 都会因未订阅被拒
			h.hallReg.subs.Range(func(k, _ any) bool {
				h.hallReg.subs.Delete(k)
				return true
			})
		}
	case HallTypeModeration:
		h.deliverHallModeration(e)
	}
}

// deliverHallModeration 定向通知：路由字段 To 只在服务端使用，投递给客户端的副本清空 To/Node。
func (h *Hub) deliverHallModeration(e *Envelope) {
	out := *e
	out.To = ""
	out.Node = ""
	kicked := hallModerationAction(e) == "kicked"
	switch {
	case e.To == HallTargetAllVisitors:
		h.hallReg.subs.Range(func(_, v any) bool {
			if c := v.(*Client); c.Kind == KindVisitor {
				c.Send(&out)
			}
			return true
		})
	case strings.HasPrefix(e.To, HallTargetVisitorsPrefix):
		vid := strings.TrimPrefix(e.To, HallTargetVisitorsPrefix)
		if vid == "" {
			return
		}
		sent := map[string]struct{}{}
		h.hallReg.subs.Range(func(_, v any) bool {
			c := v.(*Client)
			if c.Kind == KindVisitor && c.ID == vid {
				sent[c.ConnID] = struct{}{}
				c.Send(&out)
				if kicked {
					h.hallReg.subs.Delete(c.ConnID)
				}
			}
			return true
		})
		if v, ok := h.visitors.Load(vid); ok {
			c := v.(*Client)
			if _, dup := sent[c.ConnID]; !dup {
				c.Send(&out)
			}
		}
	case strings.HasPrefix(e.To, HallTargetAgentPrefix):
		aid := strings.TrimPrefix(e.To, HallTargetAgentPrefix)
		if m, ok := h.agents.Load(aid); ok {
			m.(*sync.Map).Range(func(_, cv any) bool {
				c := cv.(*Client)
				c.Send(&out)
				if kicked {
					h.hallReg.subs.Delete(c.ConnID)
				}
				return true
			})
		}
	}
}

func hallExtraMap(e *Envelope) map[string]any {
	switch m := e.Extra.(type) {
	case map[string]any:
		return m
	}
	return nil
}

// hallSettingsEnabled 读取 hall_settings 的 extra.enabled（本节点为 map，跨节点经 JSON 后同为 map）。缺省视为开启。
func hallSettingsEnabled(e *Envelope) bool {
	m := hallExtraMap(e)
	if m == nil {
		return true
	}
	if v, ok := m["enabled"].(bool); ok {
		return v
	}
	return true
}

func hallModerationAction(e *Envelope) string {
	if m := hallExtraMap(e); m != nil {
		if v, ok := m["action"].(string); ok {
			return v
		}
	}
	return ""
}

// ============ Client 只读访问器（供 hall 包使用，避免直接依赖字段） ============

// IsVisitor 是否访客连接。
func (c *Client) IsVisitor() bool { return c.Kind == KindVisitor }

// ActorID 访客 vid 或客服 id。
func (c *Client) ActorID() string { return c.ID }

// ConnectionID 连接 ID。
func (c *Client) ConnectionID() string { return c.ConnID }
