package ws

import (
	"context"
	"sync"
	"testing"
)

// [117] 串台回归：客服消息必须投递到客户端声明的会话（且通过归属校验），绝不改投到连接残留绑定的其他会话。
const (
	convA = "aaaaaaaa-0000-4000-8000-000000000001"
	convB = "bbbbbbbb-0000-4000-8000-000000000002"
	msgID = "m2-0123456789abcdef0123456789abcdef"
)

func sendAgentChat(hub *Hub, c *Client, conv string) {
	hub.handleIncoming(context.Background(), incoming{Client: c, Env: &Envelope{Type: "chat", ID: msgID, ConvID: conv, Content: "hi"}})
}

func TestAgentChatToUnownedConvIsRejectedNotRedirected(t *testing.T) {
	hub, c, spy := testHubAndClient(KindAgent, "8", convA)
	spy.owned = map[string]bool{convA: true} // B 由他人接待
	sendAgentChat(hub, c, convB)
	if spy.persisted != 0 {
		t.Fatalf("发往他人接待会话的消息不得落库/改投，实际 persisted=%d last.conv=%s", spy.persisted, spy.last.ConvID)
	}
	if c.ConversationID() != convA {
		t.Fatalf("拒绝时不得改动连接绑定，实际=%s", c.ConversationID())
	}
	if len(c.high)+len(c.low) == 0 {
		t.Fatal("拒绝时必须回 error 帧，客户端才能把该条标记为发送失败")
	}
}

func TestAgentChatToOwnedConvRebindsAndPersistsThere(t *testing.T) {
	hub, c, spy := testHubAndClient(KindAgent, "8", convA)
	spy.owned = map[string]bool{convA: true, convB: true}
	sendAgentChat(hub, c, convB)
	if spy.persisted != 1 || spy.last.ConvID != convB {
		t.Fatalf("应落库到声明的会话 B，实际 persisted=%d conv=%s", spy.persisted, spy.last.ConvID)
	}
	if c.ConversationID() != convB {
		t.Fatalf("校验通过后连接应切换到 B，实际=%s", c.ConversationID())
	}
}

func TestAgentChatAfterReconnectSelfHeals(t *testing.T) {
	hub, c, spy := testHubAndClient(KindAgent, "8", "")
	spy.owned = map[string]bool{convB: true}
	sendAgentChat(hub, c, convB)
	if spy.persisted != 1 || spy.last.ConvID != convB {
		t.Fatalf("重连后空绑定 + 声明自己的会话应正常发送，实际 persisted=%d conv=%s", spy.persisted, spy.last.ConvID)
	}
}

func TestAgentChatWithMalformedConvRejected(t *testing.T) {
	hub, c, spy := testHubAndClient(KindAgent, "8", convA)
	sendAgentChat(hub, c, "x' OR 1=1 --")
	if spy.persisted != 0 || c.ConversationID() != convA {
		t.Fatalf("非法会话 ID 必须拒绝，实际 persisted=%d bound=%s", spy.persisted, c.ConversationID())
	}
}

func TestLegacyAgentChatWithoutConvUsesBinding(t *testing.T) {
	hub, c, spy := testHubAndClient(KindAgent, "8", convA)
	sendAgentChat(hub, c, "")
	if spy.persisted != 1 || spy.last.ConvID != convA {
		t.Fatalf("旧客户端不带 conv 时沿用绑定，实际 persisted=%d conv=%s", spy.persisted, spy.last.ConvID)
	}
}

func TestAgentTypingToUnownedConvDropped(t *testing.T) {
	hub, c, spy := testHubAndClient(KindAgent, "8", convA)
	spy.owned = map[string]bool{convA: true}
	hub.handleIncoming(context.Background(), incoming{Client: c, Env: &Envelope{Type: "typing", ConvID: convB}})
	if c.ConversationID() != convA {
		t.Fatalf("输入状态声明他人会话时不得切换绑定，实际=%s", c.ConversationID())
	}
	if c.lastTypingMS.Load() != 0 {
		t.Fatal("校验不过的输入状态不得转发（不应消耗节流窗口）")
	}
}

func TestAgentReadReceiptUsesDeclaredReadableConv(t *testing.T) {
	hub, c, spy := testHubAndClient(KindAgent, "8", convA)
	spy.readable = map[string]bool{convB: true}
	hub.handleIncoming(context.Background(), incoming{Client: c, Env: &Envelope{Type: "read", ConvID: convB}})
	if spy.read != 1 || spy.last.ConvID != convB {
		t.Fatalf("可查看的会话应按声明记已读，实际 read=%d conv=%s", spy.read, spy.last.ConvID)
	}
	if c.ConversationID() != convA {
		t.Fatalf("已读不改变连接绑定，实际=%s", c.ConversationID())
	}
	spy.readable = map[string]bool{}
	hub.handleIncoming(context.Background(), incoming{Client: c, Env: &Envelope{Type: "read", ConvID: "cccccccc-0000-4000-8000-000000000003"}})
	if spy.read != 1 {
		t.Fatalf("无权查看的会话已读必须丢弃，绝不记到绑定会话上，实际 read=%d", spy.read)
	}
}

func TestValidConvIDAndSafeLog(t *testing.T) {
	if !validConvID(convA) || validConvID("conv-a") || validConvID("") || validConvID(convA+"0") {
		t.Fatal("会话 ID 校验错误")
	}
	if got := safeConvForLog("a\nb\x00c"); got != "a?b?c" {
		t.Fatalf("日志参数应替换控制字符，实际=%q", got)
	}
}

func TestAttachAgentConnOnlyRebindsThatDevice(t *testing.T) {
	hub, c1, _ := testHubAndClient(KindAgent, "8", convA)
	c2 := &Client{hub: hub, Kind: KindAgent, ID: "8", convID: convA, ConnID: "conn-2",
		high: make(chan outboundFrame, 8), low: make(chan outboundFrame, 8), log: c1.log, rawLog: c1.rawLog}
	m := &sync.Map{}
	m.Store(c1.ConnID, c1)
	m.Store(c2.ConnID, c2)
	hub.agents.Store("8", m)
	if !hub.AttachAgentConnToConv("8", "conn-2", convB) {
		t.Fatal("指定连接存在时应只绑定该连接")
	}
	if c2.ConversationID() != convB || c1.ConversationID() != convA {
		t.Fatalf("只应切换设备 2，实际 c1=%s c2=%s", c1.ConversationID(), c2.ConversationID())
	}
	if hub.AttachAgentConnToConv("8", "conn-x", convB) || hub.AttachAgentConnToConv("9", "conn-2", convB) {
		t.Fatal("不存在或不属于该账号的连接必须返回 false")
	}
}

func TestTypingNotEchoedToSender(t *testing.T) {
	hub, sender, _ := testHubAndClient(KindVisitor, "v1", convA)
	other := &Client{hub: hub, Kind: KindAgent, ID: "8", convID: convA, ConnID: "conn-agent",
		high: make(chan outboundFrame, 8), low: make(chan outboundFrame, 8), log: sender.log, rawLog: sender.rawLog}
	hub.attachConv(sender)
	hub.attachConv(other)
	env := buildTypingEnvelope(sender, 10_000)
	hub.fanoutLocal(env)
	if len(sender.high)+len(sender.low) != 0 {
		t.Fatal("输入状态不应回送给发送者")
	}
	if len(other.high)+len(other.low) != 1 {
		t.Fatal("同会话客服应收到输入状态")
	}
}
