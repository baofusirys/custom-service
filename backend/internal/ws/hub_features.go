package ws

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
