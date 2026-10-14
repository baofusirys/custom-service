package ws

import (
	"context"
	"sync"

	"go.uber.org/zap"
)

// [113] 新功能相关的 Hub 能力：输入状态信封、访客会话重置（删除聊天记录后）、拉黑状态实时同步。

// typingMinIntervalMS 每个连接转发「正在输入」的最小间隔。
const typingMinIntervalMS = 1000

// buildTypingEnvelope 按连接的服务器权威绑定构造输入状态信封；未绑定会话、被节流或已拉黑时返回 nil。
func buildTypingEnvelope(c *Client, nowMS int64) *Envelope {
	if c == nil {
		return nil
	}
	conv := c.ConversationID()
	if conv == "" {
		return nil
	}
	if c.Kind == KindVisitor && c.Blocked.Load() {
		return nil
	}
	last := c.lastTypingMS.Load()
	if nowMS-last < typingMinIntervalMS {
		return nil
	}
	if !c.lastTypingMS.CompareAndSwap(last, nowMS) {
		return nil // 并发帧竞争：只放行一个
	}
	from := "agent:" + c.ID
	if c.Kind == KindVisitor {
		from = "visitor:" + c.ID
	}
	return &Envelope{Type: "typing", From: from, ConvID: conv, TS: nowMS, ConnID: c.ConnID}
}

// VisitorOnline 访客在本节点是否在线。
func (h *Hub) VisitorOnline(visitorID string) bool {
	_, ok := h.visitors.Load(visitorID)
	return ok
}

// OnlineVisitorIDs 本节点在线访客 ID 列表（删除全部聊天记录时用于逐个切换新会话）。
func (h *Hub) OnlineVisitorIDs() []string {
	out := []string{}
	h.visitors.Range(func(k, _ any) bool {
		out = append(out, k.(string))
		return true
	})
	return out
}

// RebindVisitor 把在线访客切换到新会话；notifyCleared 时同时通知访客端清空界面与本地缓存。返回访客是否在本节点在线。
func (h *Hub) RebindVisitor(visitorID, newConvID string, notifyCleared bool) bool {
	v, ok := h.visitors.Load(visitorID)
	if !ok {
		return false
	}
	c := v.(*Client)
	h.RebindClient(c, newConvID)
	if notifyCleared {
		c.Send(&Envelope{Type: "history_cleared", ConvID: newConvID, TS: NowMS()})
	}
	return true
}

// RebindClient 就地切换连接绑定的会话（访客在已删除会话中继续发消息时也用它，不发清空通知）。
func (h *Hub) RebindClient(c *Client, newConvID string) {
	if c == nil || newConvID == "" || c.ConversationID() == newConvID {
		return
	}
	h.detachConv(c)
	c.SetConversationID(newConvID)
	h.attachConv(c)
}

// SetVisitorBlocked 拉黑 / 解除拉黑后实时更新在线访客连接的状态。
func (h *Hub) SetVisitorBlocked(visitorID string, blocked bool) {
	if v, ok := h.visitors.Load(visitorID); ok {
		v.(*Client).Blocked.Store(blocked)
	}
}

// AgentConvForbiddenText [117] 客服在不属于自己的会话发送时的提示（客户端据此显示「由其他客服接待」）。
const AgentConvForbiddenText = "该客户正由其他客服接待，请先接管后再发送"

// validConvID 会话 ID 格式（UUID 36 位：十六进制与连字符）；不合法直接拒绝，避免无意义的数据库查询。
func validConvID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		if (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F') || b == '-' {
			continue
		}
		return false
	}
	return true
}

// safeConvForLog 日志中的会话参数：合法 ID 原样，非法输入截断并替换控制字符，防日志注入。
func safeConvForLog(id string) string {
	if validConvID(id) {
		return id
	}
	b := []byte(id)
	if len(b) > 40 {
		b = b[:40]
	}
	for i, ch := range b {
		if ch < 0x20 || ch == 0x7f {
			b[i] = '?'
		}
	}
	return string(b)
}

// bindAgentConv [117] 客服连接的会话以客户端声明为准：
//   - 未声明或与当前绑定一致：保持不变（返回 true）；
//   - 声明了其他会话：格式合法且归属校验通过（本人接待或未分配）才把「本连接」切换过去，否则返回 false。
//
// 只切换发出该帧的这一个连接，不影响同账号的其他设备，彻底杜绝把消息改投到残留绑定会话的串台。
func (h *Hub) bindAgentConv(ctx context.Context, c *Client, requested, kind string) bool {
	if c == nil || c.Kind != KindAgent {
		return false
	}
	bound := c.ConversationID()
	if requested == "" || requested == bound {
		return true
	}
	if !validConvID(requested) || !h.sink.AgentOwnsConv(ctx, requested, c.ID) {
		h.secLog.Warn("agent_conv_mismatch_rejected",
			zap.String("agent", c.ID), zap.String("conn", c.ConnID), zap.String("kind", kind),
			zap.String("requested", safeConvForLog(requested)), zap.String("bound", bound))
		return false
	}
	h.RebindClient(c, requested)
	h.bizLog.Info("agent_conv_rebind",
		zap.String("agent", c.ID), zap.String("conn", c.ConnID), zap.String("kind", kind),
		zap.String("from", bound), zap.String("to", requested))
	return true
}

// AgentOnline [117] 该客服账号在本节点是否有实时连接（接管规则用：原接待客服离线时允许他人接管）。
func (h *Hub) AgentOnline(agentID string) bool {
	v, ok := h.agents.Load(agentID)
	if !ok {
		return false
	}
	online := false
	v.(*sync.Map).Range(func(_, _ any) bool {
		online = true
		return false
	})
	return online
}

// DetachAgentFromConv [117] 会话被他人接管后，把原客服绑定在该会话上的连接解除绑定（其后发送会被归属校验拒绝）。
func (h *Hub) DetachAgentFromConv(agentID, convID string) {
	if v, ok := h.agents.Load(agentID); ok {
		v.(*sync.Map).Range(func(_, cv any) bool {
			c := cv.(*Client)
			if c.ConversationID() == convID {
				h.detachConv(c)
				c.SetConversationID("")
			}
			return true
		})
	}
}

// AttachAgentConnToConv [117] 只把发起接管的那一个连接（同账号的指定设备）挂到会话上；
// 同账号其他设备保持各自打开的会话，互不干扰。连接不存在或不属于该账号时返回 false（调用方回退为全部连接）。
func (h *Hub) AttachAgentConnToConv(agentID, connID, convID string) bool {
	if connID == "" || len(connID) > 64 {
		return false
	}
	v, ok := h.agents.Load(agentID)
	if !ok {
		return false
	}
	cv, ok := v.(*sync.Map).Load(connID)
	if !ok {
		return false
	}
	h.RebindClient(cv.(*Client), convID)
	return true
}
