package ws

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// 跨站点嵌入：Widget 可以从任意域名加载，因此 CheckOrigin 必须放开。
// 实际安全靠：JWT(VisitorToken) + Nginx 限流 + IP 限流 + WSS 握手频率限制。
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
	EnableCompression: true,
}

func UpgradeVisitor(h *Hub, w http.ResponseWriter, r *http.Request, visitorID, siteID, convID string) (*Client, error) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	c := newClient(h, conn, KindVisitor, visitorID, siteID, convID, uuid.NewString(),
		h.bizLog, h.rawLog)
	h.Register(c)
	return c, nil
}

func UpgradeAgent(h *Hub, w http.ResponseWriter, r *http.Request, agentID, convID string) (*Client, error) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	c := newClient(h, conn, KindAgent, agentID, "", convID, uuid.NewString(),
		h.bizLog, h.rawLog)
	h.Register(c)
	return c, nil
}

