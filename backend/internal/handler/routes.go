package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/custom-service/backend/internal/middleware"
	"github.com/custom-service/backend/internal/store"
)

// RegisterRoutes 注册全部 HTTP / WSS 路由（[119] 从 main.go 抽出，便于用真实路由表做越权测试）。
// 鉴权分组：visitor 组公开（各接口自行校验）；visitor/hall 组走 VisitorAuth；ag 组走客服 JWT + 账号状态；adm 组再加 AdminOnly。
func RegisterRoutes(r *gin.Engine, h *HTTP, st *store.Store) {
	secret := h.cfg.JWTSecret
	api := r.Group("/api")
	{
		api.GET("/health", h.Health)
		api.GET("/version", h.Version) // [053] 集成方对比 deployed vs upstream 最新版用

		// 访客侧（无需登录，靠 visitor JWT token、Origin/CORS、SSL 和 IP/会话限流）。
		visitor := api.Group("/visitor")
		{
			visitor.POST("/session", h.VisitorSession)
			visitor.GET("/settings", h.VisitorPublicSettings)
			// TURN/STUN 短期凭证（每次通话前 fetch；24h TTL；返回 username/credential/urls）
			visitor.GET("/turn-credential", h.TurnCredential)
		}
		// [119] 多人大厅（访客）：Authorization: Bearer <visitor_token>
		vh := api.Group("/visitor/hall", middleware.VisitorAuth(secret))
		{
			vh.POST("/join", h.HallVisitorJoin)
			vh.POST("/mic", h.HallVisitorMic)
			vh.POST("/camera", h.HallVisitorCamera)
			vh.GET("/messages", h.HallVisitorMessages)
		}

		// 客服登录：IP/账号限流、失败锁定/验证码和 bcrypt 共同防爆破。
		api.POST("/agent/login", h.AgentLogin)
		// [064] token 续期：解决 [068] iOS App 12h 后 401 死循环问题
		// 不挂 AgentAuth middleware 因为 token 可能已过期（ParseAgentTokenAllowExpired 自己处理）
		api.POST("/agent/login/refresh", h.RefreshAgentToken)

		// 客服已登录
		ag := api.Group("/agent", middleware.AgentAuthWithStore(secret, st))
		{
			ag.POST("/logout", h.AgentLogout)
			ag.GET("/conversations", h.ListConversations)
			ag.GET("/conversations/:id/messages", h.ListMessages)
			ag.POST("/conversations/:id/assign", h.AssignSelf)
			ag.POST("/conversations/:id/read", h.MarkRead)
			ag.POST("/conversations/:id/close", h.CloseConv)
			// TURN/STUN 短期凭证（客服 web/iPhone 通话前 fetch；与 visitor 接口同源 service.GenerateTurnCredential）
			ag.GET("/turn-credential", h.TurnCredential)
			// [055] 关联访客（同 IP 30 天内出现的其他 vid，给客服参考"疑似同一人"）
			ag.GET("/visitor/:vid/related", h.RelatedVisitors)
			// [090] 按客户聚合的完整历史对话（跨该访客所有会话段，含已结束）
			ag.GET("/visitor/:vid/messages", h.ListMessagesByVisitor)
			// [113] 新功能（契约见 _internal/reports/api-0.8.0-features.md）
			ag.POST("/visitors/delete", h.DeleteVisitors)
			ag.GET("/quick-replies", h.ListQuickReplies)
			ag.POST("/quick-replies", h.CreateQuickReply)
			ag.POST("/quick-replies/:id/update", h.UpdateQuickReply)
			ag.POST("/quick-replies/:id/delete", h.DeleteQuickReply)
			ag.GET("/visitor/:vid/profile", h.GetVisitorProfile)
			ag.POST("/visitor/:vid/profile", h.SaveVisitorProfile)
			ag.POST("/visitor/:vid/pin", h.SetVisitorPinned)
			ag.POST("/visitor/:vid/unread", h.SetVisitorUnread)
			ag.POST("/visitor/:vid/block", h.SetVisitorBlocked)
			ag.GET("/visitor/:vid/export", h.ExportVisitor)
			ag.POST("/export", h.ExportVisitors)
			ag.GET("/tags", h.ListTags)
			ag.GET("/blocked", h.ListBlockedVisitors)
			ag.GET("/search", h.SearchMessages)
			ag.POST("/messages/:id/recall", h.RecallMessage)
			// [119] 多人大厅（客服）
			ag.POST("/hall/join", h.HallAgentJoin)
			ag.POST("/hall/camera", h.HallAgentCamera)
			ag.GET("/hall/state", h.HallAgentState)
			ag.POST("/hall/moderate", h.HallAgentModerate)
			ag.GET("/hall/messages", h.HallAgentMessages)
			ag.POST("/hall/messages/:id/delete", h.HallAgentDeleteMessage)
		}

		// 管理（仅 admin）
		adm := api.Group("/admin", middleware.AgentAuthWithStore(secret, st), middleware.AdminOnly())
		{
			adm.GET("/agents", h.ListAgents)
			adm.POST("/agents", h.CreateAgent)
			adm.POST("/agents/active", h.DisableAgent)
			adm.GET("/settings", h.GetSettings)
			adm.POST("/settings", h.UpdateSettings)
			// [113] 删除全部与回收站
			adm.POST("/visitors/delete-all", h.DeleteAllVisitors)
			adm.GET("/recycle-bin", h.ListRecycleBin)
			adm.POST("/recycle-bin/restore", h.RestoreRecycleBin)
			adm.POST("/recycle-bin/purge", h.PurgeRecycleBin)
		}

		// 文件上传 / 下载
		api.POST("/upload", h.Upload)

		// [119] LiveKit 回调（仅 cs-livekit 在容器内网直连；cs-nginx 对外一律 404）
		api.POST("/livekit/webhook", h.LiveKitWebhook)
	}

	// WSS endpoint
	r.GET("/ws/visitor", h.VisitorWS)
	r.GET("/ws/agent", h.AgentWS)

	// 文件直出（Nginx 也可以代理）
	r.GET("/files/*key", h.ServeFile)
}
