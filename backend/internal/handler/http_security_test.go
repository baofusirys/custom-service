package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWebsocketTokenNeverReadsQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		header string
		want   string
	}{
		{"subprotocol", "cs-auth, eyJhbGciOiJIUzI1NiJ9.payload.signature", "eyJhbGciOiJIUzI1NiJ9.payload.signature"},
		{"authorization", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			r := httptest.NewRequest("GET", "/ws/agent?token=query-secret", nil)
			if tc.header != "" {
				r.Header.Set("Sec-WebSocket-Protocol", tc.header)
			}
			if tc.name == "authorization" {
				r.Header.Set("Authorization", "Bearer header-token")
				tc.want = "header-token"
			}
			c.Request = r
			if got := websocketToken(c); got != tc.want {
				t.Fatalf("token extraction = %q, want %q", got, tc.want)
			}
		})
	}
}
