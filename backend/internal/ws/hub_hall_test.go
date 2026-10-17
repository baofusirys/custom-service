package ws

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"go.uber.org/zap"
)

type hallHandlerSpy struct {
	mu     sync.Mutex
	frames []Envelope
}

func (s *hallHandlerSpy) HandleHallFrame(_ *Client, e *Envelope) {
	s.mu.Lock()
	s.frames = append(s.frames, *e)
	s.mu.Unlock()
}

func hallClient(hub *Hub, kind Kind, id, conn string) *Client {
	log := zap.NewNop()
	return &Client{hub: hub, Kind: kind, ID: id, ConnID: conn,
		high: make(chan outboundFrame, 16), low: make(chan outboundFrame, 16), log: log, rawLog: log}
}

func drain(c *Client) []Envelope {
	var out []Envelope
	for {
		select {
		case f := <-c.high:
			var e Envelope
			_ = json.Unmarshal(f.data, &e)
			out = append(out, e)
		default:
			return out
		}
	}
}

// 大厅上行帧：不补服务端 ID、交给大厅处理器；伪造的下行类型被丢弃；未注入处理器回中文错误。
func TestHallFramesRoutedToHandlerWithoutServerID(t *testing.T) {
	hub, c, spy := testHubAndClient(KindVisitor, "vid-1", "conv-a")
	h := &hallHandlerSpy{}
	hub.handleIncoming(context.Background(), incoming{Client: c, Env: &Envelope{Type: "hall_subscribe"}})
	if got := drain(c); len(got) != 1 || got[0].Type != "error" {
		t.Fatalf("未注入大厅处理器时应回 error：%+v", got)
	}
	hub.SetHallHandler(h)
	hub.handleIncoming(context.Background(), incoming{Client: c, Env: &Envelope{Type: "hall_chat", Content: "hi"}})
	hub.handleIncoming(context.Background(), incoming{Client: c, Env: &Envelope{Type: "hall_settings", Extra: map[string]any{"enabled": false}}})
	if len(h.frames) != 1 || h.frames[0].Type != "hall_chat" || h.frames[0].ID != "" || h.frames[0].TS == 0 || h.frames[0].ConnID != c.ConnID {
		t.Fatalf("hall_chat 应原样（不补 ID、盖时间与连接）交给处理器，伪造下行帧丢弃：%+v", h.frames)
	}
	if spy.persisted != 0 {
		t.Fatal("大厅帧不得进入私聊持久化流水线")
	}
}

func TestHallBroadcastOnlyToSubscribers(t *testing.T) {
	hub, _, _ := testHubAndClient(KindVisitor, "x", "")
	v1 := hallClient(hub, KindVisitor, "vid-1", "c1")
	v2 := hallClient(hub, KindVisitor, "vid-2", "c2")
	a1 := hallClient(hub, KindAgent, "7", "c3")
	hub.visitors.Store("vid-1", v1)
	hub.visitors.Store("vid-2", v2)
	m := &sync.Map{}
	m.Store(a1.ConnID, a1)
	hub.agents.Store("7", m)
	hub.HallSubscribe(v1)
	hub.HallSubscribe(a1)
	hub.BroadcastHall(context.Background(), &Envelope{Type: HallTypeChat, ID: "m1", Content: "hi"})
	if len(drain(v1)) != 1 || len(drain(a1)) != 1 || len(drain(v2)) != 0 {
		t.Fatal("hall_chat 只发给已订阅连接")
	}
	// hall_settings 发给所有在线访客与客服（不要求订阅）；enabled=false 时清空订阅
	hub.BroadcastHall(context.Background(), &Envelope{Type: HallTypeSettings, Extra: map[string]any{"enabled": false}})
	if len(drain(v1)) != 1 || len(drain(v2)) != 1 || len(drain(a1)) != 1 {
		t.Fatal("hall_settings 应发给全部在线连接")
	}
	if hub.HallSubscribed(v1) || hub.HallSubscribed(a1) {
		t.Fatal("大厅关闭后订阅应清空")
	}
	// 定向通知：只发给目标访客，To/Node 不下发；kicked 时移出订阅
	hub.HallSubscribe(v1)
	hub.HallSubscribe(v2)
	hub.BroadcastHall(context.Background(), &Envelope{Type: HallTypeModeration, To: HallTargetVisitorsPrefix + "vid-1", Extra: map[string]any{"action": "kicked", "minutes": 30}})
	got := drain(v1)
	if len(got) != 1 || got[0].To != "" || got[0].Node != "" || len(drain(v2)) != 0 {
		t.Fatalf("定向通知错误：%+v", got)
	}
	if hub.HallSubscribed(v1) || !hub.HallSubscribed(v2) {
		t.Fatal("被移出者应退出订阅，其他人不受影响")
	}
	// mute_all 只发给已订阅的访客
	hub.HallSubscribe(a1)
	hub.BroadcastHall(context.Background(), &Envelope{Type: HallTypeModeration, To: HallTargetAllVisitors, Extra: map[string]any{"action": "mute_all"}})
	if len(drain(v2)) != 1 || len(drain(a1)) != 0 || len(drain(v1)) != 0 {
		t.Fatal("mute_all 只发给已订阅访客")
	}
	// 非 hall_ 类型不允许走大厅广播
	hub.BroadcastHall(context.Background(), &Envelope{Type: "chat", ConvID: "c"})
	if len(drain(v2)) != 0 {
		t.Fatal("非大厅类型不得经大厅广播")
	}
}

func TestHallUnsubscribeOnUnregisterAndClosedGuard(t *testing.T) {
	hub, _, _ := testHubAndClient(KindVisitor, "x", "")
	v := hallClient(hub, KindVisitor, "vid-1", "c1")
	hub.visitors.Store("vid-1", v)
	hub.HallSubscribe(v)
	hub.handleUnregister(context.Background(), v)
	if hub.HallSubscribed(v) {
		t.Fatal("断线后应自动退出大厅订阅")
	}
	v.closed.Store(true)
	hub.HallSubscribe(v)
	if hub.HallSubscribed(v) {
		t.Fatal("已关闭连接不得订阅")
	}
}
