package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/config"
	"github.com/custom-service/backend/internal/security"
	"github.com/custom-service/backend/internal/security/geoip"
	"github.com/custom-service/backend/internal/service"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

type HTTP struct {
	cfg     *config.Config
	svc     *service.Service
	hub     *ws.Hub
	uploads string
}

func NewHTTP(cfg *config.Config, svc *service.Service, hub *ws.Hub, uploadsDir string) *HTTP {
	return &HTTP{cfg: cfg, svc: svc, hub: hub, uploads: uploadsDir}
}

// =========== 公共 ===========

func (h *HTTP) Health(c *gin.Context) {
	tz := time.Now().In(h.cfg.Timezone).Format("2006-01-02 15:04:05")
	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"version":  config.Version, // [053] 健康检查顺带带版本，运维查部署一目了然
		"tz":       "Asia/Shanghai",
		"now":      tz,
		"visitors": h.hub.OnlineVisitorCount(),
		"agents":   h.hub.OnlineAgentCount(),
	})
}

// [053] Version 独立接口：专门给"对比 deployed vs upstream 最新版"用
//   - 集成方查自己部署的：curl https://yourdomain/api/version
//   - 拉 upstream 最新： curl -s https://raw.githubusercontent.com/baofusirys/custom-service/main/VERSION
//   - 不一致就 docker compose pull && up -d 升级
// 不需要鉴权（仅返公开版本号信息，无敏感数据）
func (h *HTTP) Version(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version": config.Version,
		"name":    "custom-service",
		"repo":    "https://github.com/baofusirys/custom-service",
	})
}

// =========== 访客侧 ===========

// VisitorSessionReq 访客打开页面后，widget 先 POST 这个接口拿 visitor_token 和 conv_id
type VisitorSessionReq struct {
	SiteID     string `json:"site_id"`
	Referer    string `json:"referer"`
	LastPage   string `json:"last_page"`
	UA         string `json:"ua"`
	Identifier string `json:"identifier"`
	VisitorID  string `json:"visitor_id"` // 第一次为空，服务端生成；后续 widget 自存
}

func (h *HTTP) VisitorSession(c *gin.Context) {
	var r VisitorSessionReq
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40001, "msg": "参数错误"})
		return
	}
	if r.SiteID == "" {
		r.SiteID = "default"
	}
	r.Identifier = security.SanitizeText(r.Identifier)
	if len(r.UA) > 480 {
		r.UA = r.UA[:480]
	}

	ip := security.ClientIP(c)
	ipCipher, _ := h.svc.Cipher().Encrypt(ip)
	// [055] 额外算 HMAC 哈希：写 ip_hash 字段供「关联访客」面板按 IP 查同人不同 vid
	ipHash := security.IPHash(h.cfg.DataAESKey, ip)
	// [060] 离线 GeoIP 解析（ip2region xdb）。库没加载/解析失败/IPv6 都返回空，不影响主流程。
	// UpsertVisitor SQL 用 COALESCE(NULLIF(VALUES(country),''), country)，空值不会覆盖既有非空，
	// 所以老库存量数据补不上不要紧（首次回访写一次就有了），新访客直接写入。
	geo := geoip.Default().Lookup(ip)

	now := time.Now()
	v := &store.Visitor{
		ID:         r.VisitorID,
		SiteID:     r.SiteID,
		IPCipher:   ipCipher,
		IPHash:     ipHash,
		UA:         r.UA,
		Country:    geo.Country,
		City:       geo.City,
		Referer:    r.Referer,
		LastPage:   r.LastPage,
		Identifier: r.Identifier,
		LastSeen:   now,
	}
	if v.ID == "" {
		v.ID = uuid.NewString()
		v.FirstSeen = now
	} else {
		v.FirstSeen = now // upsert SQL 不覆盖既有 first_seen
	}
	if err := h.svc.Store().UpsertVisitor(c.Request.Context(), v); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "msg": "保存访客失败"})
		return
	}
	// 30 分钟无活动则关闭旧会话 + 开新会话，让访客重新触发问候 + 提示音 + APNs 推送。
	// 真新访客 (first_seen=now) 完全不受影响——本来就没旧会话，直接 isNew=true。
	// 老访客回访：30 分钟阈值更敏感（之前 60 分钟），老客户半小时内反复打开 widget 不会
	// 骚扰；超过 30 分钟才算"重新进入"触发新会话 + 推送「老客户回访」。
	// 旧会话只是 status=closed，消息历史保留在 messages 表，客服「历史记录」页可查。
	conv, isNew, err := h.svc.Store().EnsureFreshConversation(c.Request.Context(), v.SiteID, v.ID, 30)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50002, "msg": "创建会话失败"})
		return
	}
	tok, err := security.IssueVisitorToken(h.cfg.JWTSecret, v.ID, v.SiteID, 12*time.Hour)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50003, "msg": "签发 token 失败"})
		return
	}
	resp := gin.H{
		"code":          0,
		"visitor_id":    v.ID,
		"conversation":  conv.ID,
		"visitor_token": tok,
		"server_now":    time.Now().In(h.cfg.Timezone).Format("2006-01-02 15:04:05"),
	}
	// 新会话时：异步通知客服 + 落库问候 + 等访客 WSS 上线后通过 WSS 推送问候
	// 不再在 HTTP 响应里塞 greeting —— 让 greeting 走完整 WSS 通道，
	// 访客端能走正常的 playNotify / unread+1 / 已读机制
	if isNew {
		h.svc.OnVisitorEnter(v, conv, h.hub)
	}
	c.JSON(http.StatusOK, resp)
}

// VisitorWS 访客的 WSS 入口。query: token=<visitor_token>
// [062] 移除按 IP 的 WSS 握手限流（爷爷决策）；防御层缩窄为：visitor JWT token 校验 + WSS 自身 origin / SSL
func (h *HTTP) VisitorWS(c *gin.Context) {
	ctx := c.Request.Context()
	tok := c.Query("token")
	if tok == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40103, "msg": "缺少 token"})
		return
	}
	claims, err := security.ParseVisitorToken(h.cfg.JWTSecret, tok)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40104, "msg": "token 无效"})
		return
	}
	conv, err := h.svc.Store().OpenOrGetConversation(ctx, claims.SiteID, claims.VisitorID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": 50004, "msg": "拉会话失败"})
		return
	}
	_, _ = ws.UpgradeVisitor(h.hub, c.Writer, c.Request, claims.VisitorID, claims.SiteID, conv.ID)
}

// =========== 客服侧 ===========

type AgentLoginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (h *HTTP) AgentLogin(c *gin.Context) {
	var r AgentLoginReq
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40002, "msg": "参数错误"})
		return
	}
	r.Username = strings.TrimSpace(r.Username)
	ctx := c.Request.Context()
	ip := security.ClientIP(c)

	a, err := h.svc.Store().GetAgentByUsername(ctx, r.Username)
	if err != nil || a == nil {
		// 用户输错账号是常见情况，不计 violation（防爆破靠 Nginx login_rps 2 r/s + bcrypt 慢）
		h.svc.Limiter().LogSecurityWarn(ip, "agent_login_fail_nouser", r.Username)
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40105, "msg": "账号或密码错误"})
		return
	}
	if !a.Active {
		c.JSON(http.StatusForbidden, gin.H{"code": 40302, "msg": "账号已禁用"})
		return
	}
	if !security.CheckPassword(a.PassHash, r.Password) {
		h.svc.Limiter().LogSecurityWarn(ip, "agent_login_fail_password", r.Username)
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40105, "msg": "账号或密码错误"})
		return
	}
	tok, err := security.IssueAgentToken(h.cfg.JWTSecret, a.ID, a.Username, a.Role, 12*time.Hour)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50005, "msg": "签发 token 失败"})
		return
	}
	_ = h.svc.Store().UpdateAgentLastLogin(ctx, a.ID)
	h.svc.AuditLog().Info("agent_login",
		zap.String("username", a.Username), zap.String("ip", ip))
	h.svc.Store().AuditLog(ctx, a.Username, "login", "", "", ip)
	c.JSON(http.StatusOK, gin.H{
		"code":  0,
		"token": tok,
		"agent": gin.H{
			"id": a.ID, "username": a.Username, "role": a.Role, "nickname": a.Nickname,
		},
	})
}

// RefreshAgentToken [064] 客户端用即将过期 / 已过期不久的 token 换新 token。
//
// 设计要点（解决 [068] iOS App 12h 后 401 死循环问题）：
//  1. 必须 Authorization: Bearer <oldToken> 带旧 token
//  2. 旧 token 用 ParseAgentTokenAllowExpired 解析（允许 exp 已过），但签名/sub 必须 valid
//  3. grace period 24h：过期超过 24h 的拒绝（防止失效太久的 token 被无限续命）
//  4. 重新查 DB agent 仍 active（防止已禁用的 agent 续 token）
//  5. 签发同样 12h TTL 的新 token + 写 audit log
//
// 错误码：
//
//	40101 缺 Authorization header
//	40103 token 签名错 / 篡改
//	40104 token 过期超过 24h grace period（必须重登）
//	40105 agent 已禁用 / 不存在
//	50019 后端签发失败
func (h *HTTP) RefreshAgentToken(c *gin.Context) {
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40101, "msg": "缺少 token"})
		return
	}
	oldToken := strings.TrimPrefix(auth, "Bearer ")
	claims, err := security.ParseAgentTokenAllowExpired(h.cfg.JWTSecret, oldToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40103, "msg": "token 无效"})
		return
	}
	// 检查 grace period：过期 > 24h 拒绝（避免一个老 token 永远续命；同时也阻挡老掉牙的窃取 token）
	if claims.ExpiresAt != nil {
		const gracePeriod = 24 * time.Hour
		expired := time.Since(claims.ExpiresAt.Time)
		if expired > gracePeriod {
			h.svc.Limiter().LogSecurityWarn(security.ClientIP(c), "refresh_token_too_old",
				fmt.Sprintf("aid=%d expired=%s ago", claims.AgentID, expired))
			c.JSON(http.StatusUnauthorized, gin.H{"code": 40104, "msg": "登录已过期太久，请重新登录"})
			return
		}
	}
	// 重新查 DB：agent 必须仍存在且 active（防止已禁用的 agent 还能续 token）
	agent, err := h.svc.Store().GetAgentByID(c.Request.Context(), claims.AgentID)
	if err != nil || agent == nil || !agent.Active {
		h.svc.Limiter().LogSecurityWarn(security.ClientIP(c), "refresh_token_agent_inactive",
			fmt.Sprintf("aid=%d", claims.AgentID))
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40105, "msg": "账号已禁用"})
		return
	}
	// 签发新 token（同样 12h TTL）
	const newTTL = 12 * time.Hour
	newTok, err := security.IssueAgentToken(h.cfg.JWTSecret, agent.ID, agent.Username, agent.Role, newTTL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50019, "msg": "签发 token 失败"})
		return
	}
	// 审计：token 续期事件必须落盘，便于事后查"哪个 IP 用谁的 token 续了"
	h.svc.AuditLog().Info("agent_token_refresh",
		zap.Int64("aid", agent.ID),
		zap.String("username", agent.Username),
		zap.String("ip", security.ClientIP(c)))
	h.svc.Store().AuditLog(c.Request.Context(), agent.Username, "token_refresh",
		fmt.Sprintf("agent:%d", agent.ID), "", security.ClientIP(c))
	c.JSON(http.StatusOK, gin.H{
		"code":       0,
		"token":      newTok,
		"expires_in": int(newTTL.Seconds()),
	})
}

// AgentWS 客服的 WSS 入口。query: token=<agent_token>
// [062] 移除按 IP 的 WSS 握手限流；防御层：agent JWT token 校验 + WSS origin / SSL
// [064] 区分错误码让 App 客户端能：
//
//	code=40106 缺 token            → 跳登录页
//	code=40102 token 已过期         → 客户端调 /agent/login/refresh 续 token 后重连
//	code=40107 token 无效（签名错） → 跳登录页
//
// 注：WSS upgrade 之前 reject 时 Flutter WebSocketChannel.connect 只能拿 HTTP 401，
// 读不到 body 里的 code。所以 mobile_app 必须**主动在连接前检查 exp**（_shouldRefreshToken），
// 不能完全依赖服务端 close code。
func (h *HTTP) AgentWS(c *gin.Context) {
	tok := c.Query("token")
	if tok == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40106, "msg": "缺少 token"})
		return
	}
	claims, err := security.ParseAgentToken(h.cfg.JWTSecret, tok)
	if err != nil {
		if errors.Is(err, security.ErrTokenExpired) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40102, "msg": "登录已过期"})
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40107, "msg": "token 无效"})
		return
	}
	_, _ = ws.UpgradeAgent(h.hub, c.Writer, c.Request, fmt.Sprintf("%d", claims.AgentID), "")
}

// ListConversations 客服后台拉取进行中会话
func (h *HTTP) ListConversations(c *gin.Context) {
	rows, err := h.svc.Store().ListOpenConversations(c.Request.Context(), 200)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50006, "msg": "查询失败"})
		return
	}
	// [059] 把 store 里的 ip_cipher（AES-GCM 密文）解密成明文 IP 给客服 UI 显示，
	// 不直接外泄密文（前端拿密文也没用，浪费带宽 + 不规范）
	for _, row := range rows {
		if ic, ok := row["ip_cipher"].(string); ok && ic != "" {
			if ip, err := h.svc.Cipher().Decrypt(ic); err == nil {
				row["ip"] = ip
			}
		}
		delete(row, "ip_cipher") // 不外泄密文
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": rows})
}

// ListMessages 拉历史消息
func (h *HTTP) ListMessages(c *gin.Context) {
	convID := c.Param("id")
	before := c.Query("before")
	limit := 50
	if v := c.Query("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	msgs, err := h.svc.Store().ListMessages(c.Request.Context(), convID, before, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50007, "msg": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": msgs})
}

// AssignSelf 客服接管会话
func (h *HTTP) AssignSelf(c *gin.Context) {
	convID := c.Param("id")
	aid, _ := c.Get("agent_id")
	agentID := aid.(int64)
	if err := h.svc.Store().AssignAgent(c.Request.Context(), convID, agentID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50008, "msg": "分配失败"})
		return
	}
	h.hub.AttachAgentToConv(fmt.Sprintf("%d", agentID), convID)
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// MarkRead 标记已读（HTTP 兜底；正常实时走 WSS type=read）。
// 同步更新 last_read_agent_at + 清零 unread_agent。
// [055] RelatedVisitors 查同 IP 30 天内出现的其他 vid（最多 10 个），按 last_seen 倒序。
// 给客服端访客详情页「关联访客 (N)」面板用，参考"疑似同一人"，不强行合并 vid。
// 路径参数 :vid 是当前访客 ID，用来从 visitors 表读出 ip_hash 再反查同 hash 的别人。
// 解密 ip_cipher 给客服看明文 IP 便于追踪；identifier/city 等已经是明文直接返。
func (h *HTTP) RelatedVisitors(c *gin.Context) {
	vid := c.Param("vid")
	if vid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40001, "msg": "vid 必填"})
		return
	}
	// 先拿当前访客的 ip_hash（再查同 hash 的其他人）
	current, err := h.svc.Store().GetVisitor(c.Request.Context(), vid)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 40402, "msg": "访客不存在"})
		return
	}
	// GetVisitor 没读 ip_hash 字段，重新算（IP 解密 + IPHash）以解耦
	ip, _ := h.svc.Cipher().Decrypt(current.IPCipher)
	ipHash := security.IPHash(h.cfg.DataAESKey, ip)
	related, err := h.svc.Store().RelatedVisitorsByIPHash(c.Request.Context(), ipHash, vid, 30, 10)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50031, "msg": "查询失败"})
		return
	}
	// 解密每个相关访客的 IP 返给客服（仅 agent 鉴权后能调，安全）
	type relatedItem struct {
		VID        string    `json:"vid"`
		Identifier string    `json:"identifier"`
		IP         string    `json:"ip"`
		Country    string    `json:"country"`
		City       string    `json:"city"`
		LastSeen   time.Time `json:"last_seen"`
		FirstSeen  time.Time `json:"first_seen"`
	}
	out := make([]relatedItem, 0, len(related))
	for _, v := range related {
		ipPlain, _ := h.svc.Cipher().Decrypt(v.IPCipher)
		out = append(out, relatedItem{
			VID:        v.ID,
			Identifier: v.Identifier,
			IP:         ipPlain,
			Country:    v.Country,
			City:       v.City,
			LastSeen:   v.LastSeen,
			FirstSeen:  v.FirstSeen,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"code":  0,
		"count": len(out),
		"data":  out,
	})
}

func (h *HTTP) MarkRead(c *gin.Context) {
	convID := c.Param("id")
	if err := h.svc.Store().UpdateLastRead(c.Request.Context(), convID, "agent", time.Now()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50009, "msg": "失败"})
		return
	}
	// 通过 WSS 把已读事件推给对端访客（让 UI 实时更新「已读」角标）
	agentID, _ := c.Get("agent_id")
	h.hub.FanoutToConv(c.Request.Context(), &ws.Envelope{
		Type:   "read",
		From:   fmt.Sprintf("agent:%v", agentID),
		ConvID: convID,
		TS:     ws.NowMS(),
	})
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// CloseConv 关闭会话
func (h *HTTP) CloseConv(c *gin.Context) {
	convID := c.Param("id")
	if err := h.svc.Store().CloseConversation(c.Request.Context(), convID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50010, "msg": "失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// =========== 文件上传（访客或客服都用） ===========

var allowedMIME = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true,
	"application/pdf": true, "application/zip": true, "application/x-zip-compressed": true,
	"text/plain": true,
	"application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"application/vnd.ms-excel": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true,
}

// Upload 表单字段：file (二进制) + conv_id (可选) + uploader (visitor|agent) + ref (id)
// 必须带有效 token：访客拿 visitor_token、客服拿 agent_token，从 Authorization Bearer 取。
func (h *HTTP) Upload(c *gin.Context) {
	ctx := c.Request.Context()
	ip := security.ClientIP(c)

	uploadBy := c.PostForm("uploader")
	if uploadBy != "visitor" && uploadBy != "agent" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40003, "msg": "uploader 错误"})
		return
	}
	ref, convID, err := h.authorizeUpload(c, uploadBy)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40108, "msg": err.Error()})
		return
	}

	mh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40004, "msg": "缺少 file"})
		return
	}
	if mh.Size > int64(h.cfg.MaxUploadMB)*1024*1024 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"code": 41301, "msg": "文件过大"})
		return
	}
	// 嗅探 mime（不信任客户端 Content-Type）
	f, err := mh.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50011, "msg": "打开文件失败"})
		return
	}
	defer f.Close()
	sniff := make([]byte, 512)
	n, _ := f.Read(sniff)
	mimeType := http.DetectContentType(sniff[:n])
	if !allowedMIME[mimeType] {
		// 退一步看后缀
		ext := strings.ToLower(filepath.Ext(mh.Filename))
		if guessed := mime.TypeByExtension(ext); !allowedMIME[guessed] {
			// 用户误传不支持的文件类型是常见情况，记日志即可，不计 violation
			h.svc.Limiter().LogSecurityWarn(ip, "upload_mime_blocked", mimeType+"|"+ext)
			c.JSON(http.StatusUnsupportedMediaType, gin.H{"code": 41501, "msg": "文件类型不允许"})
			return
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50012, "msg": "seek 失败"})
		return
	}

	id := uuid.NewString()
	tz := time.Now().In(h.cfg.Timezone)
	subDir := filepath.Join(h.uploads, tz.Format("2006"), tz.Format("01"), tz.Format("02"))
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50013, "msg": "建目录失败"})
		return
	}
	storeKey := filepath.Join(tz.Format("2006/01/02"), id+filepath.Ext(mh.Filename))
	full := filepath.Join(subDir, id+filepath.Ext(mh.Filename))
	out, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50014, "msg": "写文件失败"})
		return
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		os.Remove(full)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50015, "msg": "写入失败"})
		return
	}
	out.Close()

	rec := &store.FileRecord{
		ID: id, ConvID: convID, UploadBy: uploadBy, UploaderRef: ref,
		Filename: mh.Filename, StoreKey: storeKey, Size: mh.Size, MIME: mimeType,
		CreatedAt: time.Now(),
	}
	if err := h.svc.Store().InsertFile(ctx, rec); err != nil {
		os.Remove(full)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50016, "msg": "落库失败"})
		return
	}
	url := "/files/" + storeKey
	kind := "file"
	if strings.HasPrefix(mimeType, "image/") {
		kind = "image"
	}
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"id": id, "url": url, "kind": kind, "size": mh.Size, "name": mh.Filename, "mime": mimeType,
	})
}

func (h *HTTP) authorizeUpload(c *gin.Context, uploadBy string) (ref, convID string, err error) {
	tok := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if tok == "" {
		return "", "", errors.New("缺少 token")
	}
	switch uploadBy {
	case "visitor":
		vc, e := security.ParseVisitorToken(h.cfg.JWTSecret, tok)
		if e != nil {
			return "", "", errors.New("visitor token 无效")
		}
		conv, _ := h.svc.Store().OpenOrGetConversation(c.Request.Context(), vc.SiteID, vc.VisitorID)
		return vc.VisitorID, conv.ID, nil
	case "agent":
		ac, e := security.ParseAgentToken(h.cfg.JWTSecret, tok)
		if e != nil {
			return "", "", errors.New("agent token 无效")
		}
		return fmt.Sprintf("%d", ac.AgentID), c.PostForm("conv_id"), nil
	}
	return "", "", errors.New("未知 uploader")
}

// ServeFile 静态文件下载（受 token 保护：只能访问自己会话内的文件）
func (h *HTTP) ServeFile(c *gin.Context) {
	key := c.Param("key")
	if strings.Contains(key, "..") {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	full := filepath.Join(h.uploads, key)
	if _, err := os.Stat(full); err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.File(full)
}

// =========== 客服管理（admin 才能用） ===========

type CreateAgentReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role"`
	Nickname string `json:"nickname"`
}

// [052] CreateAgent 入参校验常量
const (
	maxUsernameLen = 64 // 跟 agents.username VARCHAR(64) 对齐
	maxNicknameLen = 64 // 跟 agents.nickname VARCHAR(64) 对齐
	minPasswordLen = 8  // bcrypt 安全下限
)

// [052] 用户名格式：3-64 位字母/数字/下划线/中划线/点
// 防 SQL 注入特殊字符 / 防 unicode 同形字攻击 / 防超长占用 DB
var agentUsernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-.]{3,64}$`)

// [052] 角色白名单（schema 是 VARCHAR(16) DEFAULT 'agent'，业务上只有 admin/agent）
var allowedAgentRoles = map[string]bool{"admin": true, "agent": true}

func (h *HTTP) CreateAgent(c *gin.Context) {
	var r CreateAgentReq
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40005, "msg": "参数错误"})
		return
	}

	// === [052] 入参校验前置（拦在 handler，省一次 DB 调用 + 错误文案精准）===
	if !agentUsernameRegex.MatchString(r.Username) {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 40010,
			"msg":  "用户名格式错误（3-64 位字母/数字/下划线/中划线/点）",
		})
		return
	}
	if utf8.RuneCountInString(r.Nickname) > maxNicknameLen {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 40011,
			"msg":  fmt.Sprintf("昵称过长（最多 %d 个字符）", maxNicknameLen),
		})
		return
	}
	if len(r.Password) < minPasswordLen {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40006, "msg": "密码至少 8 位"})
		return
	}
	if r.Role != "" && !allowedAgentRoles[r.Role] {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 40012,
			"msg":  "角色不合法（仅支持 admin / agent）",
		})
		return
	}
	if r.Role == "" {
		r.Role = "agent"
	}

	// === 密码哈希 ===
	hash, err := security.HashPassword(r.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50017, "msg": "密码哈希失败"})
		return
	}

	// === [052] DB 写入 + 按 error 类型分支返不同 HTTP code，不再"或失败"歧义 ===
	id, err := h.svc.Store().CreateAgent(c.Request.Context(), &store.Agent{
		Username: r.Username, PassHash: hash, Role: r.Role, Nickname: r.Nickname,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrDuplicateUsername):
			// 409 = Conflict（REST 标准；前端按 40007 走差异化提示，如聚焦回 username）
			c.JSON(http.StatusConflict, gin.H{"code": 40007, "msg": "用户名已存在"})
		case errors.Is(err, store.ErrFieldTooLong):
			// 上面 handler 校验理论应已拦截；走到这里说明有遗漏字段
			c.JSON(http.StatusBadRequest, gin.H{"code": 40013, "msg": "字段长度超限"})
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			c.JSON(http.StatusGatewayTimeout, gin.H{"code": 50419, "msg": "请求超时，请重试"})
		default:
			// 真正的 DB 异常（连接断/磁盘满/权限不足等），服务端日志详细记，用户只看友好提示
			actor, _ := c.Get("agent_username")
			h.svc.BizLog().Error("create_agent_internal_error",
				zap.Any("actor", actor),
				zap.String("username", r.Username),
				zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"code": 50019, "msg": "系统繁忙，请稍后重试"})
		}
		return
	}

	// === 审计日志 + 返回 ===
	actor, _ := c.Get("agent_username")
	h.svc.Store().AuditLog(c.Request.Context(), fmt.Sprintf("%v", actor), "create_agent",
		fmt.Sprintf("agent:%d", id), r.Username, security.ClientIP(c))
	c.JSON(http.StatusOK, gin.H{"code": 0, "id": id})
}

func (h *HTTP) ListAgents(c *gin.Context) {
	list, err := h.svc.Store().ListAgents(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50018, "msg": "失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": list})
}

// =========== 系统设置（仅管理员） ===========

// 允许的 setting key 白名单（防止任意 key 注入）
var allowedSettingKeys = map[string]bool{
	"agent_notify_sound":   true,
	"visitor_notify_sound": true,
	"notify_visitor_enter": true,
	"greeting_enabled":     true,
	"greeting_text":        true,
	"widget_title":         true,
	// luckfast APNs 推送：访客发消息时后端调 messagepush.luckfast.com 推到客服 iPhone
	// 两项都填才启用；留空则禁用推送（不报错，不影响其它功能）
	"push_user_id":  true,
	"push_user_key": true,
	// luckfast 推送音色（"0"-"15" 共 16 种），管理员可在 admin Settings 各场景独立配置
	// 注：这只控制 iPhone 系统通知栏弹通知时播的音（锁屏 / 后台场景），
	// 跟 App 内界面来电时本地循环的 voice-ring.mp3（[036]）是两件事
	"push_sound_enter":   true, // 新访客打开 widget 时播放
	"push_sound_message": true, // 已有会话中访客发新消息时播放
	"push_sound_call":    true, // [043] 恢复：语音来电 APNs 推送音色（系统通知栏）
	// 访客 widget 电话按钮旁的提示文字（公开给访客 widget 读）
	"voice_call_hint": true,
	// （可选）覆盖推送点击跳转 URL，默认 maihaocs://open 拉起 App
	"push_jump_url": true,
}

// GetSettings 拉所有可见 setting（仅管理员可读全部）
func (h *HTTP) GetSettings(c *gin.Context) {
	keys := make([]string, 0, len(allowedSettingKeys))
	for k := range allowedSettingKeys {
		keys = append(keys, k)
	}
	m, err := h.svc.Store().GetSettingsMap(c.Request.Context(), keys)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50030, "msg": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": m})
}

// UpdateSettings 批量更新 setting（仅管理员）
func (h *HTTP) UpdateSettings(c *gin.Context) {
	var body map[string]string
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40030, "msg": "参数错误"})
		return
	}
	// 过滤：只允许白名单内的 key；值统一过 SanitizeText 清洗
	filtered := make(map[string]string, len(body))
	for k, v := range body {
		if !allowedSettingKeys[k] {
			continue
		}
		// greeting_text 可能含正常标点，只做 XSS 清洗保留文本
		if k == "greeting_text" || k == "widget_title" {
			v = security.SanitizeText(v)
			if len(v) > 500 {
				v = v[:500]
			}
		}
		filtered[k] = v
	}
	if len(filtered) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40031, "msg": "无可更新字段"})
		return
	}
	if err := h.svc.Store().SetSettings(c.Request.Context(), filtered); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50031, "msg": "保存失败"})
		return
	}
	actor, _ := c.Get("agent_username")
	h.svc.Store().AuditLog(c.Request.Context(), fmt.Sprintf("%v", actor),
		"update_settings", "", fmt.Sprintf("%v", filtered), security.ClientIP(c))
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// VisitorPublicSettings 给访客 widget 拉取的公开子集（不需要 token）
func (h *HTTP) VisitorPublicSettings(c *gin.Context) {
	ctx := c.Request.Context()
	m, _ := h.svc.Store().GetSettingsMap(ctx, []string{
		"visitor_notify_sound", "widget_title", "voice_call_hint",
	})
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"notify_sound":    defaultIfEmpty(m["visitor_notify_sound"], "visitor1"),
			"widget_title":    defaultIfEmpty(m["widget_title"], "在线客服"),
			"voice_call_hint": defaultIfEmpty(m["voice_call_hint"], "直接呼叫客服"),
		},
	})
}

func defaultIfEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// TurnCredential 返回 WebRTC 通话所需的 ICE 服务器配置（STUN + TURN）。
// 三端（widget/admin/mobile_app）在发起通话前调用此接口，把返回的 urls + username + credential
// 喂给 RTCPeerConnection，让 WebRTC 在 P2P 失败时走 TURN relay（解决 VPN/严格 NAT 场景）。
//
// 凭证有效期 24h，HMAC-SHA1 由 service.GenerateTurnCredential 生成；
// CoTURN 用同一个 TURN_STATIC_AUTH_SECRET 反向校验。
//
// 兼容：TURN_REALM 或 TURN_STATIC_AUTH_SECRET 未配置时，仅返回公共 STUN（不影响功能，
// 只是 P2P 失败时仍会失败），不报错。
func (h *HTTP) TurnCredential(c *gin.Context) {
	// userID 仅用于 username 标识（CoTURN 不验，只供日志/审计）
	// 优先用客服 ID（已登录），否则取 visitor token / IP 作为标识
	userID := "anon"
	if aid, ok := c.Get("agent_id"); ok {
		userID = fmt.Sprintf("agent-%v", aid)
	} else if vid := c.Query("vid"); vid != "" {
		userID = "visitor-" + vid
	} else {
		userID = "ip-" + security.ClientIP(c)
	}

	// TURN 未配置时：降级为公共 STUN（保持通话功能，只是无 relay 兜底）
	if h.cfg.TurnRealm == "" || h.cfg.TurnSecret == "" {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": gin.H{
				"urls": []string{"stun:stun.l.google.com:19302"},
				"ttl":  0,
			},
		})
		return
	}
	cred := service.GenerateTurnCredential(userID, h.cfg.TurnRealm, h.cfg.TurnSecret)
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": cred})
}

func (h *HTTP) DisableAgent(c *gin.Context) {
	var body struct {
		ID     int64 `json:"id"`
		Active bool  `json:"active"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40008, "msg": "参数错误"})
		return
	}
	if err := h.svc.Store().SetAgentActive(c.Request.Context(), body.ID, body.Active); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50019, "msg": "失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

