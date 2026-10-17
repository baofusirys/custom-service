package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/custom-service/backend/internal/hall"
)

// [123] 大厅开关（契约第 10 节）：任何启用中的客服账号都可读取与切换总开关、文字聊天、语音、视频。

// HallSwitchesGet GET /api/agent/hall/switches
func (h *HTTP) HallSwitchesGet(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	v, err := h.hall.Switches(c.Request.Context(), agentActor(c))
	if err != nil {
		h.hallFail(c, err)
		return
	}
	hallOK(c, v)
}

// HallSwitchesUpdate POST /api/agent/hall/switches
func (h *HTTP) HallSwitchesUpdate(c *gin.Context) {
	if !h.hallReady(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	var in hall.SwitchesInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, 40060, "参数错误")
		return
	}
	v, err := h.hall.UpdateSwitches(c.Request.Context(), agentActor(c), &in)
	if err != nil {
		h.hallFail(c, err)
		return
	}
	detail := map[string]any{"enabled": v.Enabled, "chat_enabled": v.ChatEnabled, "voice_enabled": v.VoiceEnabled, "video_enabled": v.VideoEnabled}
	h.auditAction(c, "hall_switches_update", "hall:"+h.hall.Room(), detail)
	hallOK(c, v)
}
