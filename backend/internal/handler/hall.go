package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/hall"
)

// [119] 多人大厅 HTTP 接口（契约 _internal/reports/hall-contract.md 第 4 节）。
// 处理器只做：身份取自中间件（访客 visitor_token / 客服 JWT）→ 参数解析 → 调 hall.Service → 统一响应与审计。
// 限流、名额、禁入、静默等业务规则全部在 hall 包内，HTTP 与 WS 共用同一套实现。

const maxWebhookBody = 1 << 20

func visitorActor(c *gin.Context) hall.Actor {
	return hall.Actor{Visitor: true, VisitorID: c.GetString("visitor_id"), SiteID: c.GetString("site_id")}
}

func agentActor(c *gin.Context) hall.Actor {
	a := currentAgent(c)
	return hall.Actor{AgentID: a.ID, AgentUsername: a.Username, Admin: a.Admin}
}

// hallReady 大厅服务未注入（不应发生）时一律按「已关闭」处理。
func (h *HTTP) hallReady(c *gin.Context) bool {
	if h.hall == nil {
		fail(c, http.StatusForbidden, 40371, "大厅已关闭")
		return false
	}
	return true
}

// hallFail 业务错误按 hall.Error 返回；其它错误记日志后返回统一 500（不带堆栈）。
func (h *HTTP) hallFail(c *gin.Context, err error) {
	var he *hall.Error
	if errors.As(err, &he) {
		body := gin.H{"code": he.Code, "msg": he.Msg}
		if he.Data != nil {
			body["data"] = he.Data
		}
		c.JSON(he.Status, body)
		return
	}
	h.serverError(c, "hall_unexpected_error", err)
}

func hallOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok", "data": data})
}

type hallToggleReq struct {
	On *bool `json:"on"`
}

func bindToggle(c *gin.Context) (bool, bool) {
	var r hallToggleReq
	if err := c.ShouldBindJSON(&r); err != nil || r.On == nil {
		fail(c, http.StatusBadRequest, 40060, "参数错误：需要 {on: true|false}")
		return false, false
	}
	return *r.On, true
}

func hallLimit(c *gin.Context) int {
	n, err := strconv.Atoi(c.Query("limit"))
	if err != nil || n <= 0 || n > 50 {
		return 50
	}
	return n
}

// ============ 访客 ============

func (h *HTTP) HallVisitorJoin(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	res, err := h.hall.Join(c.Request.Context(), visitorActor(c))
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, res)
}

func (h *HTTP) HallVisitorMic(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	on, ok := bindToggle(c)
	if !ok {
		return
	}
	res, err := h.hall.SetMic(c.Request.Context(), visitorActor(c), on)
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, res)
}

func (h *HTTP) HallVisitorCamera(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	on, ok := bindToggle(c)
	if !ok {
		return
	}
	res, err := h.hall.SetCamera(c.Request.Context(), visitorActor(c), on)
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, res)
}

func (h *HTTP) HallVisitorMessages(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	res, err := h.hall.Messages(c.Request.Context(), visitorActor(c), c.Query("before"), hallLimit(c))
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, res)
}

// ============ 客服 ============

func (h *HTTP) HallAgentJoin(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	res, err := h.hall.Join(c.Request.Context(), agentActor(c))
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, res)
}

func (h *HTTP) HallAgentCamera(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	on, ok := bindToggle(c)
	if !ok {
		return
	}
	res, err := h.hall.SetCamera(c.Request.Context(), agentActor(c), on)
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, res)
}

func (h *HTTP) HallAgentState(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	res, err := h.hall.State(c.Request.Context(), agentActor(c))
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, res)
}

func (h *HTTP) HallAgentMessages(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	res, err := h.hall.Messages(c.Request.Context(), agentActor(c), c.Query("before"), hallLimit(c))
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, res)
}

type hallModerateReq struct {
	Action   string `json:"action"`
	Identity string `json:"identity"`
	Minutes  int    `json:"minutes"`
}

// HallAgentModerate 客服管理动作；成功与失败都写审计（操作者、对象、动作、结果）。
func (h *HTTP) HallAgentModerate(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	var r hallModerateReq
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40060, "参数错误")
		return
	}
	res, err := h.hall.Moderate(c.Request.Context(), agentActor(c), r.Action, r.Identity, r.Minutes)
	detail := map[string]any{"room": h.hall.Room(), "action": r.Action, "result": "ok"}
	if res != nil {
		detail["affected"] = res.Affected
		if res.Minutes > 0 {
			detail["minutes"] = res.Minutes
		}
	}
	if err != nil {
		var he *hall.Error
		if errors.As(err, &he) {
			detail["result"] = "error"
			detail["code"] = he.Code
		} else {
			detail["result"] = "internal_error"
		}
	}
	if hall.ValidIdentity(r.Identity) || r.Action == hall.ActionMuteAll {
		target := "hall:" + h.hall.Room()
		if r.Identity != "" {
			target += ":" + r.Identity
		}
		h.auditAction(c, "hall_"+safeAction(r.Action), target, detail)
	}
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, res)
}

// safeAction 审计动作名只取白名单，防日志注入。
func safeAction(a string) string {
	switch a {
	case hall.ActionMute, hall.ActionMuteAll, hall.ActionCameraOff, hall.ActionKick:
		return a
	}
	return "moderate_invalid"
}

func (h *HTTP) HallAgentDeleteMessage(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	id := c.Param("id")
	err := h.hall.DeleteMessage(c.Request.Context(), agentActor(c), id)
	if err != nil {
		h.hallFail(c, err)
		return
	}
	h.auditAction(c, "hall_delete_message", "hall_message:"+id, map[string]any{"room": h.hall.Room()})
	hallOK(c, gin.H{"id": id})
}

// ============ LiveKit 回调 ============

// LiveKitWebhook 只接受 cs-livekit 在容器内网直连：经反向代理转发的请求（带 X-Forwarded-For / X-Real-IP）一律 404；
// 之后校验 JWT 签名与请求体摘要（cs-nginx 层也对 /api/livekit/ 一律 404，这里是纵深防御）。
func (h *HTTP) LiveKitWebhook(c *gin.Context) {
	if c.GetHeader("X-Forwarded-For") != "" || c.GetHeader("X-Real-IP") != "" || h.hall == nil {
		h.svc.SecurityLog().Warn("hall_webhook_proxied_rejected", zap.String("event", "hall_webhook_rejected"), zap.String("reason", "proxied_or_disabled"))
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBody)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		fail(c, http.StatusRequestEntityTooLarge, 41302, "回调请求体过大")
		return
	}
	if err := h.hall.HandleWebhook(c.Request.Context(), c.GetHeader("Authorization"), body); err != nil {
		if errors.Is(err, hall.ErrWebhookUnauthorized) {
			fail(c, http.StatusUnauthorized, 40120, "回调签名无效")
			return
		}
		h.serverError(c, "hall_webhook_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0})
}
