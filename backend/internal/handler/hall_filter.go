package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/custom-service/backend/internal/hall"
)

// [122] 多人大厅敏感词屏蔽与告警邮件管理接口（契约 _internal/reports/hall-contract.md 第 9 节，仅管理员）。
// 处理器只做：请求体大小限制 → 绑定 → 调 hall.Service → 统一响应与审计；校验与业务规则都在 hall 包。

const maxFilterBody = 256 << 10

func (h *HTTP) bindLimited(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxFilterBody)
	if err := c.ShouldBindJSON(v); err != nil {
		fail(c, http.StatusBadRequest, 40060, "参数错误")
		return false
	}
	return true
}

// HallFilterGet GET /api/admin/hall/filter
func (h *HTTP) HallFilterGet(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	v, err := h.hall.FilterSettings(c.Request.Context(), agentActor(c))
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, v)
}

// HallFilterSave POST /api/admin/hall/filter
func (h *HTTP) HallFilterSave(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	var in hall.FilterInput
	if !h.bindLimited(c, &in) {
		return
	}
	v, err := h.hall.SaveFilterSettings(c.Request.Context(), agentActor(c), &in)
	if err != nil {
		h.hallFail(c, err)
		return
	}
	h.auditAction(c, "hall_filter_update", "hall:"+h.hall.Room(), map[string]any{
		"enabled": v.Enabled, "mode": v.Mode, "categories": v.Categories, "custom_words": len(v.CustomWords),
		"allow_words": len(v.AllowWords), "allow_domains": v.AllowDomains,
	})
	hallOK(c, v)
}

type hallFilterTestReq struct {
	Content string            `json:"content"`
	Config  *hall.FilterInput `json:"config"`
}

// HallFilterTest POST /api/admin/hall/filter/test
func (h *HTTP) HallFilterTest(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	var req hallFilterTestReq
	if !h.bindLimited(c, &req) {
		return
	}
	res, err := h.hall.TestFilter(c.Request.Context(), agentActor(c), req.Content, req.Config)
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, res)
}

// HallFilterEvents GET /api/admin/hall/filter/events?before=<id>&limit=20
func (h *HTTP) HallFilterEvents(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	var before int64
	limit := 20
	if s := c.Query("before"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			fail(c, http.StatusBadRequest, 40060, "参数错误：before")
			return
		}
		before = n
	}
	if s := c.Query("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 50 {
			fail(c, http.StatusBadRequest, 40060, "参数错误：limit 必须是 1-50")
			return
		}
		limit = n
	}
	list, err := h.hall.FilterEvents(c.Request.Context(), agentActor(c), before, limit)
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, list)
}

// HallAlertMailGet GET /api/admin/hall/alert-mail
func (h *HTTP) HallAlertMailGet(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	v, err := h.hall.AlertMail(c.Request.Context(), agentActor(c))
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, v)
}

// HallAlertMailSave POST /api/admin/hall/alert-mail（密码不进审计）
func (h *HTTP) HallAlertMailSave(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	var in hall.AlertMailInput
	if !h.bindLimited(c, &in) {
		return
	}
	v, err := h.hall.SaveAlertMail(c.Request.Context(), agentActor(c), &in)
	if err != nil {
		h.hallFail(c, err)
		return
	}
	h.auditAction(c, "hall_alert_mail_update", "hall:"+h.hall.Room(), map[string]any{
		"enabled": v.Enabled, "to": v.To, "from": v.From, "smtp_host": v.SMTPHost, "smtp_port": v.SMTPPort,
		"smtp_security": v.SMTPSecurity, "password_changed": in.SMTPPassword != nil && *in.SMTPPassword != "",
	})
	hallOK(c, v)
}

// HallAlertMailTest POST /api/admin/hall/alert-mail/test
func (h *HTTP) HallAlertMailTest(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	to, err := h.hall.TestAlertMail(c.Request.Context(), agentActor(c))
	if err != nil {
		h.hallFail(c, err)
		return
	}
	h.auditAction(c, "hall_alert_mail_test", "hall:"+h.hall.Room(), map[string]any{"to": to})
	hallOK(c, gin.H{"to": to})
}
