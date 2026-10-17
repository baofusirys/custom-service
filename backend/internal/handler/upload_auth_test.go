package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/custom-service/backend/internal/config"
	"github.com/custom-service/backend/internal/security"
)

// TestUploadAgentAuthReadsSessionCookie 复现：网页后台只发 HttpOnly cs_session Cookie（不带 Authorization），
// 上传鉴权必须像 agentAuth 中间件一样回退读取 Cookie，否则返回「缺少 token」并把客服踢回登录页。
// 这里用另一把密钥签发的令牌放进 Cookie：修复后应进入验签并报「令牌无效」，而不是「缺少 token」（不触达数据库）。
func TestUploadAgentAuthReadsSessionCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &HTTP{cfg: &config.Config{JWTSecret: []byte("0123456789abcdef0123456789abcdef-upload")}}
	other, err := security.IssueAgentTokenWithVersion([]byte("ffffffffffffffffffffffffffffffff-other"), 1, "qa", "agent", 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/api/upload", nil)
	req.AddCookie(&http.Cookie{Name: "cs_session", Value: other})
	c.Request = req
	_, _, err = h.authorizeUpload(c, "agent", false)
	if err == nil {
		t.Fatal("伪造令牌不应通过")
	}
	if err.Error() == "缺少 token" {
		t.Fatalf("客服上传未读取 cs_session Cookie：%v", err)
	}
}
