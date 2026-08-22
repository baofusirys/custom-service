package ws

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap"
)

type messageSinkSpy struct {
	persisted int
	delivered int
	read      int
	last      Envelope
}

func (s *messageSinkSpy) OnVisitorConnect(context.Context, *Client) error    { return nil }
func (s *messageSinkSpy) OnVisitorDisconnect(context.Context, *Client) error { return nil }
func (s *messageSinkSpy) OnAgentConnect(context.Context, *Client) error      { return nil }
func (s *messageSinkSpy) OnAgentDisconnect(context.Context, *Client) error   { return nil }
func (s *messageSinkSpy) PreprocessVisitorMessage(context.Context, *Envelope, *Client) bool {
	return true
}
func (s *messageSinkSpy) PreprocessAgentMessage(context.Context, *Envelope, *Client) bool {
	return true
}
func (s *messageSinkSpy) PersistMessageAsync(e *Envelope, _ *Client, _ string) {
	s.persisted++
	s.last = *e
}
func (s *messageSinkSpy) PersistDeliveryAsync(e *Envelope, _ *Client) {
	s.delivered++
	s.last = *e
}
func (s *messageSinkSpy) AgentOwnsConv(context.Context, string, string) bool { return true }
func (s *messageSinkSpy) PersistReadAsync(e *Envelope, _ *Client, _ string) {
	s.read++
	s.last = *e
}
func (s *messageSinkSpy) OnPageNavigation(string, string, string, string)         {}
func (s *messageSinkSpy) OnVisitorVoiceCall(string, string)                       {}
func (s *messageSinkSpy) OnVoiceCallFinished(string, string, string, string, int) {}

func testHubAndClient(kind Kind, id, conv string) (*Hub, *Client, *messageSinkSpy) {
	spy := &messageSinkSpy{}
	log := zap.NewNop()
	hub := NewHub(HubConfig{NodeID: "node-test", BizLog: log, RawLog: log, SecLog: log, Sink: spy})
	client := &Client{
		hub: hub, Kind: kind, ID: id, convID: conv, ConnID: "conn-test",
		high: make(chan outboundFrame, 8), low: make(chan outboundFrame, 8), log: log, rawLog: log,
	}
	return hub, client, spy
}

func TestChatQueuesPersistenceWithoutPrematureAck(t *testing.T) {
	hub, client, spy := testHubAndClient(KindAgent, "7", "conv-a")
	hub.handleIncoming(context.Background(), incoming{Client: client, Env: &Envelope{
		Type: "chat", ID: "m2-0123456789abcdef0123456789abcdef", ConvID: "spoof", Content: "hello",
	}})
	if spy.persisted != 1 {
		t.Fatalf("消息应进入持久化流水线，实际=%d", spy.persisted)
	}
	if spy.last.ConvID != "conv-a" {
		t.Fatalf("必须使用服务器绑定 conv，实际=%s", spy.last.ConvID)
	}
	if len(client.high) != 0 {
		t.Fatal("数据库提交前不允许产生成功 ACK")
	}
}

func TestDeliveryReceiptRejectsAgentAndOverridesSpoofedConversation(t *testing.T) {
	hub, agent, spy := testHubAndClient(KindAgent, "7", "conv-a")
	hub.handleIncoming(context.Background(), incoming{Client: agent, Env: &Envelope{
		Type: "delivery", ID: "m2-0123456789abcdef0123456789abcdef", ConvID: "conv-other",
	}})
	if spy.delivered != 0 {
		t.Fatal("客服端不得伪造访客 delivery ACK")
	}

	hub, visitor, spy := testHubAndClient(KindVisitor, "visitor-a", "conv-a")
	hub.handleIncoming(context.Background(), incoming{Client: visitor, Env: &Envelope{
		Type: "delivery", ID: "m2-0123456789abcdef0123456789abcdef", ConvID: "conv-other",
	}})
	if spy.delivered != 1 {
		t.Fatal("访客合法 delivery ACK 应进入校验流水线")
	}
	if spy.last.ConvID != "conv-a" {
		t.Fatalf("伪造 conv 未被覆盖: %s", spy.last.ConvID)
	}
}

func TestReadReceiptUsesConnectionConversation(t *testing.T) {
	hub, visitor, spy := testHubAndClient(KindVisitor, "visitor-a", "conv-a")
	hub.handleIncoming(context.Background(), incoming{Client: visitor, Env: &Envelope{
		Type: "read", ID: "m2-0123456789abcdef0123456789abcdef", ConvID: "conv-other",
	}})
	if spy.read != 1 || spy.last.ConvID != "conv-a" {
		t.Fatalf("read ACK 必须绑定连接会话: count=%d conv=%s", spy.read, spy.last.ConvID)
	}
}

func TestLegacyReadReceiptKeepsEmptyTargetID(t *testing.T) {
	hub, visitor, spy := testHubAndClient(KindVisitor, "visitor-a", "conv-a")
	hub.handleIncoming(context.Background(), incoming{Client: visitor, Env: &Envelope{
		Type: "read", ConvID: "conv-other",
	}})
	if spy.read != 1 {
		t.Fatalf("旧版空 ID read ACK 应进入兼容流水线: count=%d", spy.read)
	}
	if spy.last.ID != "" {
		t.Fatalf("服务端不得为旧版 read ACK 伪造目标消息 ID: %s", spy.last.ID)
	}
}

func TestReadReceiptRejectsInvalidTargetID(t *testing.T) {
	hub, visitor, spy := testHubAndClient(KindVisitor, "visitor-a", "conv-a")
	hub.handleIncoming(context.Background(), incoming{Client: visitor, Env: &Envelope{
		Type: "read", ID: "../foreign-message", ConvID: "conv-a",
	}})
	if spy.read != 0 {
		t.Fatal("非法 read 目标 ID 不得进入持久化流水线")
	}
}

func TestConversationBindingConcurrentAccess(t *testing.T) {
	_, client, _ := testHubAndClient(KindAgent, "7", "conv-a")
	var group sync.WaitGroup
	for i := 0; i < 32; i++ {
		group.Add(2)
		go func(index int) {
			defer group.Done()
			client.SetConversationID("conv-write")
		}(i)
		go func() {
			defer group.Done()
			_ = client.ConversationID()
		}()
	}
	group.Wait()
	if client.ConversationID() == "" {
		t.Fatal("并发会话绑定后不应为空")
	}
}
