package ws

import "testing"

// [113] 输入状态：服务器权威会话、节流、拉黑、内容剥离。
func TestBuildTypingEnvelope(t *testing.T) {
	visitor := &Client{Kind: KindVisitor, ID: "v1", ConnID: "conn-v"}
	if buildTypingEnvelope(visitor, 10_000) != nil {
		t.Fatal("未绑定会话不应转发")
	}
	visitor.SetConversationID("conv-1")
	e := buildTypingEnvelope(visitor, 10_000)
	if e == nil || e.ConvID != "conv-1" || e.From != "visitor:v1" || e.Type != "typing" {
		t.Fatalf("unexpected envelope %+v", e)
	}
	if e.Content != "" || e.MediaURL != "" || e.Extra != nil {
		t.Fatal("输入状态不得携带内容")
	}
	if buildTypingEnvelope(visitor, 10_500) != nil {
		t.Fatal("1 秒内应被节流")
	}
	if buildTypingEnvelope(visitor, 11_000) == nil {
		t.Fatal("满 1 秒应放行")
	}
	visitor.Blocked.Store(true)
	if buildTypingEnvelope(visitor, 20_000) != nil {
		t.Fatal("已拉黑访客不应转发")
	}
	agent := &Client{Kind: KindAgent, ID: "7", ConnID: "conn-a"}
	agent.SetConversationID("conv-1")
	agent.Blocked.Store(true) // 客服连接不受拉黑标记影响
	if e := buildTypingEnvelope(agent, 30_000); e == nil || e.From != "agent:7" {
		t.Fatalf("客服输入状态应转发: %+v", e)
	}
}
