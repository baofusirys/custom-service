package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/config"
	"github.com/custom-service/backend/internal/hall"
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
	hall    *hall.Service // [119] 多人大厅（main 注入；为 nil 时大厅接口返回已关闭）
}

// SetHall 注入大厅服务。
func (h *HTTP) SetHall(hs *hall.Service) { h.hall = hs }

func publicURLOrEmpty(raw string) string {
	v, err := security.ValidatePublicURL(raw)
	if err != nil {
		return ""
	}
	return v
}

func (h *HTTP) allowedWSOrigin(c *gin.Context) bool {
	origin := strings.TrimRight(strings.TrimSpace(c.GetHeader("Origin")), "/")
	if origin == "" || len(h.cfg.CORSAllowedOrigins) == 0 {
		return origin == "" || origin == "https://"+h.cfg.PublicDomain || origin == "http://"+h.cfg.PublicDomain
	}
	for _, allowed := range h.cfg.CORSAllowedOrigins {
		if origin == strings.TrimRight(allowed, "/") {
			return true
		}
	}
	return false
}

func websocketToken(c *gin.Context) string {
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	// 浏览器 WebSocket API 不能自定义 Authorization，客户端以第二个 subprotocol 传递 JWT。
	for _, p := range strings.Split(c.GetHeader("Sec-WebSocket-Protocol"), ",") {
		p = strings.TrimSpace(p)
		if p == "cs-auth" || strings.HasPrefix(p, "cs-auth.") {
			if strings.HasPrefix(p, "cs-auth.") {
				return strings.TrimPrefix(p, "cs-auth.")
			}
			continue
		}
		if strings.Count(p, ".") == 2 {
			return p
		}
	}
	if cookie, err := c.Cookie("cs_session"); err == nil {
		return cookie
	}
	return ""
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
//
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
	if ok, err := h.svc.Limiter().AllowAction(c.Request.Context(), "visitor_session", 60, time.Minute, "ip:"+security.ClientIP(c)); err != nil || !ok {
		h.svc.Limiter().LogSecurityWarn(security.ClientIP(c), "visitor_session_rate_limited", "")
		c.JSON(http.StatusTooManyRequests, gin.H{"code": 42904, "msg": "请求过于频繁，请稍后重试"})
		return
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
		Referer:    publicURLOrEmpty(r.Referer),
		LastPage:   publicURLOrEmpty(r.LastPage),
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
	// [113] 来自已拉黑 IP 的访客（含清空本地存储换身份的）自动继承拉黑，静默处理。
	if inherited, err := h.svc.Store().InheritIPBlock(c.Request.Context(), v.ID, ipHash); err != nil {
		h.svc.BizLog().Warn("inherit_ip_block_failed", zap.Error(err), zap.String("vid", v.ID))
	} else if inherited {
		h.svc.SecurityLog().Info("visitor_ip_block_inherited", zap.String("vid", v.ID), zap.String("ip", ip))
	}
	// 30 分钟无活动则关闭旧会话 + 开新会话，让访客重新触发问候 + 提示音 + APNs 推送。
	// 真新访客 (first_seen=now) 完全不受影响——本来就没旧会话，直接 isNew=true。
	// 老访客回访：30 分钟阈值更敏感（之前 60 分钟），老客户半小时内反复打开 widget 不会
	// 骚扰；超过 30 分钟才算"重新进入"触发新会话 + 推送「老客户回访」。
	// 旧会话只是 status=closed，消息历史保留在 messages 表，客服「历史记录」页可查。
	// [085] 期望③：30 分钟阈值改 settings 可配（session_fresh_minutes），默认 30。
	//   clamp 到 [1,1440] 分钟：0/负/超界一律回退默认，防误配导致"每次进入都重建"或"永不重建"。
	freshMin := h.svc.SettingInt(c.Request.Context(), "session_fresh_minutes", 30)
	if freshMin < 1 || freshMin > 1440 {
		freshMin = 30
	}
	conv, isNew, err := h.svc.Store().EnsureFreshConversation(c.Request.Context(), v.SiteID, v.ID, freshMin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50002, "msg": "创建会话失败"})
		return
	}
	tok, err := security.IssueVisitorToken(h.cfg.JWTSecret, v.ID, v.SiteID, 2*time.Hour)
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
		// [113] 访客端据此清空被客服删除的本地聊天缓存、改写离线期间被撤回的消息
		"history_reset_at": int64(0),
		"recalled_ids":     []string{},
	}
	if t, err := h.svc.Store().HistoryResetAt(c.Request.Context(), v.ID); err == nil && !t.IsZero() {
		resp["history_reset_at"] = t.UnixMilli()
	}
	if ids, err := h.svc.Store().RecentRecalledIDs(c.Request.Context(), v.ID, time.Now().Add(-7*24*time.Hour), 200); err == nil {
		resp["recalled_ids"] = ids
	}
	// 新会话时：异步通知客服 + 落库问候 + 等访客 WSS 上线后通过 WSS 推送问候
	// 不再在 HTTP 响应里塞 greeting —— 让 greeting 走完整 WSS 通道，
	// 访客端能走正常的 playNotify / unread+1 / 已读机制
	if isNew {
		h.svc.OnVisitorEnter(v, conv, h.hub)
	}
	c.JSON(http.StatusOK, resp)
}

// VisitorWS 访客的 WSS 入口。JWT 只能放 Authorization 或 Sec-WebSocket-Protocol，禁止 query。
func (h *HTTP) VisitorWS(c *gin.Context) {
	ctx := c.Request.Context()
	if ok, err := h.svc.Limiter().AllowAction(ctx, "visitor_ws", 30, time.Minute, "ip:"+security.ClientIP(c)); err != nil || !ok {
		h.svc.Limiter().LogSecurityWarn(security.ClientIP(c), "visitor_ws_rate_limited", "")
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"code": 42905, "msg": "连接过于频繁，请稍后重试"})
		return
	}
	if c.Query("token") != "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"code": 40041, "msg": "WebSocket 禁止在 URL 中携带 token"})
		return
	}
	if !h.allowedWSOrigin(c) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 40304, "msg": "来源不允许"})
		return
	}
	tok := websocketToken(c)
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
	blocked, err := h.svc.Store().IsVisitorBlocked(ctx, claims.VisitorID)
	if err != nil {
		h.svc.BizLog().Warn("visitor_ws_block_check_failed", zap.Error(err), zap.String("vid", claims.VisitorID))
	}
	_, _ = ws.UpgradeVisitor(h.hub, c.Writer, c.Request, claims.VisitorID, claims.SiteID, conv.ID, blocked)
}

// =========== 客服侧 ===========

type AgentLoginReq struct {
	Username    string `json:"username" binding:"required"`
	Password    string `json:"password" binding:"required"`
	CaptchaID   string `json:"captcha_id"`
	CaptchaCode string `json:"captcha_code"`
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
	rateUsername := strings.ToLower(r.Username)
	captchaSubject := ip + "|" + rateUsername
	if ok, err := h.svc.Limiter().AllowAction(ctx, "login", 10, time.Minute, "ip:"+ip, "user:"+rateUsername); err != nil || !ok {
		h.svc.Limiter().LogSecurityWarn(ip, "agent_login_rate_limited", r.Username)
		c.JSON(http.StatusTooManyRequests, gin.H{"code": 42901, "msg": "请求过于频繁，请稍后再试"})
		return
	}
	locked, lockErr := h.svc.Limiter().LoginLocked(ctx, rateUsername, ip)
	if lockErr != nil {
		h.svc.SecurityLog().Error("agent_login_lock_check_failed", zap.String("ip", ip), zap.Error(lockErr))
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": 50301, "msg": "登录服务暂不可用"})
		return
	}
	if locked {
		h.svc.Limiter().LogSecurityWarn(ip, "agent_login_locked", r.Username)
		c.JSON(http.StatusTooManyRequests, gin.H{"code": 42902, "msg": "账号或来源已临时锁定"})
		return
	}
	if h.svc.Limiter().LoginCaptchaRequired(ctx, r.Username, ip) &&
		!h.svc.Limiter().VerifyCaptcha(ctx, captchaSubject, r.CaptchaID, r.CaptchaCode) {
		captchaID, captchaSVG, _ := h.svc.Limiter().IssueCaptcha(ctx, captchaSubject)
		h.svc.Limiter().LogSecurityWarn(ip, "agent_login_captcha_required", r.Username)
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40106, "msg": "请完成图形验证码", "captcha_id": captchaID, "captcha_svg": captchaSVG})
		return
	}

	a, err := h.svc.Store().GetAgentByUsername(ctx, r.Username)
	if err != nil || a == nil {
		// 用户输错账号是常见情况，不计 violation（防爆破靠 Nginx login_rps 2 r/s + bcrypt 慢）
		_, captcha, failureErr := h.svc.Limiter().RecordLoginFailure(ctx, rateUsername, ip)
		if failureErr != nil {
			h.svc.SecurityLog().Error("agent_login_failure_record_failed", zap.String("ip", ip), zap.Error(failureErr))
			c.JSON(http.StatusServiceUnavailable, gin.H{"code": 50301, "msg": "登录服务暂不可用"})
			return
		}
		h.svc.Limiter().LogSecurityWarn(ip, "agent_login_fail_nouser", r.Username)
		if captcha {
			c.Header("X-Captcha-Required", "1")
		}
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40105, "msg": "账号或密码错误"})
		return
	}
	if !a.Active {
		c.JSON(http.StatusForbidden, gin.H{"code": 40302, "msg": "账号已禁用"})
		return
	}
	if !security.CheckPassword(a.PassHash, r.Password) {
		_, captcha, failureErr := h.svc.Limiter().RecordLoginFailure(ctx, rateUsername, ip)
		if failureErr != nil {
			h.svc.SecurityLog().Error("agent_login_failure_record_failed", zap.String("ip", ip), zap.Error(failureErr))
			c.JSON(http.StatusServiceUnavailable, gin.H{"code": 50301, "msg": "登录服务暂不可用"})
			return
		}
		h.svc.Limiter().LogSecurityWarn(ip, "agent_login_fail_password", r.Username)
		if captcha {
			c.Header("X-Captcha-Required", "1")
		}
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40105, "msg": "账号或密码错误"})
		return
	}
	h.svc.Limiter().ClearLoginFailures(ctx, rateUsername, ip)
	tok, err := security.IssueAgentTokenWithVersion(h.cfg.JWTSecret, a.ID, a.Username, a.Role, a.TokenVersion, 30*time.Minute)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50005, "msg": "签发 token 失败"})
		return
	}
	_ = h.svc.Store().UpdateAgentLastLogin(ctx, a.ID)
	h.svc.AuditLog().Info("agent_login",
		zap.String("username", a.Username), zap.String("ip", ip))
	h.svc.Store().AuditLog(ctx, a.Username, "login", "", "", ip)
	http.SetCookie(c.Writer, &http.Cookie{
		Name: "cs_session", Value: tok, Path: "/", MaxAge: 30 * 60,
		HttpOnly: true, Secure: h.cfg.EnableHTTPS, SameSite: http.SameSiteStrictMode,
	})
	c.JSON(http.StatusOK, gin.H{
		"code":  0,
		"token": tok,
		"agent": gin.H{
			"id": a.ID, "username": a.Username, "role": a.Role, "nickname": a.Nickname,
		},
	})
}

// AgentLogout 主动吊销该账号当前及历史会话。
func (h *HTTP) AgentLogout(c *gin.Context) {
	aid, _ := c.Get("agent_id")
	id, ok := aid.(int64)
	if !ok || h.svc.Store().BumpAgentTokenVersion(c.Request.Context(), id) != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50020, "msg": "退出失败"})
		return
	}
	h.svc.Store().AuditLog(c.Request.Context(), fmt.Sprintf("%v", c.MustGet("agent_username")), "logout", fmt.Sprintf("agent:%d", id), "session_revoked", security.ClientIP(c))
	h.hub.CloseAgent(fmt.Sprintf("%d", id))
	http.SetCookie(c.Writer, &http.Cookie{Name: "cs_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: h.cfg.EnableHTTPS, SameSite: http.SameSiteStrictMode})
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// RefreshAgentToken [064] 客户端用即将过期 / 已过期不久的 token 换新 token。
//
// 设计要点（解决 [068] iOS App 12h 后 401 死循环问题）：
//  1. 可用 Authorization: Bearer <oldToken> 或 HttpOnly cs_session Cookie 带旧 token
//  2. 旧 token 用 ParseAgentTokenAllowExpired 解析（允许 exp 已过），但签名/sub 必须 valid
//  3. grace period 24h：过期超过 24h 的拒绝（防止失效太久的 token 被无限续命）
//  4. 重新查 DB agent 仍 active（防止已禁用的 agent 续 token）
//  5. 签发同样 30m TTL 的新 token + 写 audit log
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
	oldToken := ""
	if strings.HasPrefix(auth, "Bearer ") {
		oldToken = strings.TrimPrefix(auth, "Bearer ")
	} else if cookie, cookieErr := c.Cookie("cs_session"); cookieErr == nil {
		oldToken = cookie
	}
	if oldToken == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40101, "msg": "缺少 token"})
		return
	}
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
	if err != nil || agent == nil || !agent.Active || agent.TokenVersion != claims.Version {
		h.svc.Limiter().LogSecurityWarn(security.ClientIP(c), "refresh_token_agent_inactive",
			fmt.Sprintf("aid=%d", claims.AgentID))
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40105, "msg": "账号已禁用"})
		return
	}
	// 签发新 token（同样 30m TTL）
	const newTTL = 30 * time.Minute
	newTok, err := security.IssueAgentTokenWithVersion(h.cfg.JWTSecret, agent.ID, agent.Username, agent.Role, agent.TokenVersion, newTTL)
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
	http.SetCookie(c.Writer, &http.Cookie{
		Name: "cs_session", Value: newTok, Path: "/", MaxAge: int(newTTL.Seconds()),
		HttpOnly: true, Secure: h.cfg.EnableHTTPS, SameSite: http.SameSiteStrictMode,
	})
	c.JSON(http.StatusOK, gin.H{
		"code":       0,
		"token":      newTok,
		"expires_in": int(newTTL.Seconds()),
	})
}

// AgentWS 客服的 WSS 入口。JWT 只能放 Authorization 或 Sec-WebSocket-Protocol，禁止 query。
// 防御层：agent JWT token 校验 + WSS origin 白名单 + 会话版本吊销 / SSL
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
	if ok, err := h.svc.Limiter().AllowAction(c.Request.Context(), "agent_ws", 30, time.Minute, "ip:"+security.ClientIP(c)); err != nil || !ok {
		h.svc.Limiter().LogSecurityWarn(security.ClientIP(c), "agent_ws_rate_limited", "")
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"code": 42905, "msg": "连接过于频繁，请稍后重试"})
		return
	}
	if c.Query("token") != "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"code": 40041, "msg": "WebSocket 禁止在 URL 中携带 token"})
		return
	}
	if !h.allowedWSOrigin(c) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 40304, "msg": "来源不允许"})
		return
	}
	tok := websocketToken(c)
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
	agent, dbErr := h.svc.Store().GetAgentByID(c.Request.Context(), claims.AgentID)
	if dbErr != nil || agent == nil || !agent.Active || agent.TokenVersion != claims.Version {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40108, "msg": "会话已失效，请重新登录"})
		return
	}
	_, _ = ws.UpgradeAgent(h.hub, c.Writer, c.Request, fmt.Sprintf("%d", claims.AgentID), "")
}

// ListConversations 客服后台拉取会话列表。
// [085] 支持分页 + 两种口径：
//
//	mode=all（默认）：进行中(open)会话，按最近活动倒序，offset/limit 分页
//	mode=contacted：客服回复过的客户(按访客去重，跨 open/closed)，分页 —— 「已联系」滚动加载
//
// 返回 data 为数组；前端按「返回条数 < limit」判断是否到底（没有下一页）。
func (h *HTTP) ListConversations(c *gin.Context) {
	mode := c.Query("mode")
	limit := 50
	if v := c.Query("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := 0
	if v := c.Query("offset"); v != "" {
		fmt.Sscanf(v, "%d", &offset)
	}
	if offset < 0 {
		offset = 0
	}
	// [113] 按标签筛选（可选）
	tag, ok := NormalizeTagFilter(c.Query("tag"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40053, "msg": "标签不合法"})
		return
	}
	// [115] 列表与总数互不依赖：总数在独立 goroutine 中并行查询（各用一个连接），接口耗时 ≈ max(列表, 计数)。
	// goroutine 只使用请求 ctx 与局部变量，不触碰 gin.Context；自带 recover，避免计数异常拖垮进程。
	ctx := c.Request.Context()
	st := h.svc.Store()
	var (
		rows     []map[string]any
		err      error
		total    int
		countErr error
		wg       sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if p := recover(); p != nil {
				countErr = fmt.Errorf("count panic: %v", p)
			}
		}()
		if mode == "contacted" {
			total, countErr = st.CountContactedVisitors(ctx, tag)
		} else {
			total, countErr = st.CountAllVisitors(ctx, tag)
		}
	}()
	if mode == "contacted" {
		rows, err = st.ListContactedConversations(ctx, tag, limit, offset)
	} else {
		rows, err = st.ListAllVisitorConversations(ctx, tag, limit, offset)
	}
	wg.Wait()
	if err != nil {
		reqID, _ := c.Get("request_id")
		h.svc.BizLog().Error("conversation_list_failed", zap.Error(err), zap.String("mode", mode),
			zap.Int("limit", limit), zap.Int("offset", offset), zap.Bool("tag_filter", tag != ""),
			zap.String("request_id", fmt.Sprintf("%v", reqID)))
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
	// [085] 附带总数：前端显示「已联系 (N)」/「全部 (N)」；滚动是否到底用「返回条数<limit」判断
	if countErr != nil {
		// 计数失败不影响列表本身（与旧行为一致返回 total=0），但必须留痕便于排障。
		reqID, _ := c.Get("request_id")
		h.svc.BizLog().Warn("conversation_count_failed", zap.Error(countErr), zap.String("mode", mode),
			zap.String("request_id", fmt.Sprintf("%v", reqID)))
		total = 0
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": rows, "total": total})
}

// ListMessages 拉历史消息
func (h *HTTP) ListMessages(c *gin.Context) {
	convID := c.Param("id")
	if !h.agentCanAccessConversation(c, convID) {
		c.JSON(http.StatusForbidden, gin.H{"code": 40302, "msg": "会话不存在或无权访问"})
		return
	}
	before := c.Query("before")
	after := c.Query("after") // [070] 增量同步：只拉比 afterID 更新的消息（进会话先显本地缓存，再后台补这段）
	limit := 50
	if v := c.Query("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	msgs, err := h.svc.Store().ListMessages(c.Request.Context(), convID, before, after, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50007, "msg": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": msgs})
}

// ListMessagesByVisitor [090] 按客户(访客)拉「全部会话段」消息，合并成完整历史对话流。
//
//	path: /agent/visitor/:vid/messages?before=&after=&limit=
//	配合 [088] 列表按客户聚合：点开客户看其所有历史会话(含已结束)的真实对话，而非只看最新一段。
func (h *HTTP) ListMessagesByVisitor(c *gin.Context) {
	vid := c.Param("vid")
	if !h.agentCanAccessVisitor(c, vid) {
		c.JSON(http.StatusForbidden, gin.H{"code": 40302, "msg": "访客不存在或无权访问"})
		return
	}
	before := c.Query("before")
	after := c.Query("after")
	limit := 50
	if v := c.Query("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	msgs, err := h.svc.Store().ListMessagesByVisitor(c.Request.Context(), vid, before, after, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50007, "msg": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": msgs})
}

// AssignSelf 客服接管会话。
// [117] 会话由其他客服接待时的接管规则：管理员可直接接管；原接待客服已停用或当前不在线时，任何客服可接管；
// 原接待客服在线时普通客服不可接管，返回 40303 与接待人名称（客户端据此显示「只能查看」并禁用发送）。
// 接管用条件更新保证原子性（并发接管只有一个成功），并写审计；原客服绑定在该会话上的连接随即解除。
func (h *HTTP) AssignSelf(c *gin.Context) {
	convID := c.Param("id")
	a := currentAgent(c)
	ctx := c.Request.Context()
	if !ValidConversationID(convID) {
		c.JSON(http.StatusForbidden, gin.H{"code": 40302, "msg": "会话不存在或已删除"})
		return
	}
	owns, err := h.svc.Store().AgentOwnsConversation(ctx, convID, a.IDStr)
	if err != nil {
		h.serverError(c, "assign_owner_check_failed", err)
		return
	}
	if !owns {
		owner, err := h.svc.Store().ConversationOwner(ctx, convID)
		if err != nil {
			h.serverError(c, "assign_owner_lookup_failed", err)
			return
		}
		if !owner.Exists || owner.OwnerID == 0 {
			c.JSON(http.StatusForbidden, gin.H{"code": 40302, "msg": "会话不存在或已删除"})
			return
		}
		ownerOnline := h.hub.AgentOnline(fmt.Sprintf("%d", owner.OwnerID))
		reason := ""
		switch {
		case a.Admin:
			reason = "admin"
		case !owner.OwnerActive:
			reason = "owner_disabled"
		case !ownerOnline:
			reason = "owner_offline"
		}
		if reason == "" {
			reqID, _ := c.Get("request_id")
			h.svc.BizLog().Info("conversation_takeover_denied",
				zap.Int64("aid", a.ID), zap.String("conv", convID), zap.Int64("owner", owner.OwnerID),
				zap.String("request_id", fmt.Sprintf("%v", reqID)))
			c.JSON(http.StatusForbidden, gin.H{"code": 40303, "msg": "该客户正由「" + owner.OwnerName + "」接待，对方在线，你只能查看聊天记录",
				"owner": owner.OwnerName, "owner_online": true})
			return
		}
		ok, err := h.svc.Store().TakeOverConversation(ctx, convID, owner.OwnerID, a.ID)
		if err != nil {
			h.serverError(c, "conversation_takeover_failed", err)
			return
		}
		if !ok {
			c.JSON(http.StatusConflict, gin.H{"code": 40903, "msg": "会话状态已变化，请返回列表刷新后重试"})
			return
		}
		h.hub.DetachAgentFromConv(fmt.Sprintf("%d", owner.OwnerID), convID)
		h.auditAction(c, "conversation_takeover", convID, map[string]any{
			"from_agent": owner.OwnerID, "to_agent": a.ID, "reason": reason, "owner_online": ownerOnline})
	} else if err := h.svc.Store().AssignAgent(ctx, convID, a.ID); err != nil {
		h.serverError(c, "assign_failed", err)
		return
	}
	// [117] 客户端带上自己的实时连接 ID 时只绑定该设备；旧客户端未带时绑定该账号全部连接（原行为）
	if !h.hub.AttachAgentConnToConv(a.IDStr, strings.TrimSpace(c.GetHeader("X-CS-Conn")), convID) {
		h.hub.AttachAgentToConv(a.IDStr, convID)
	}
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

func (h *HTTP) agentCanAccessConversation(c *gin.Context, convID string) bool {
	aid, ok := c.Get("agent_id")
	if !ok {
		return false
	}
	okay, err := h.svc.Store().AgentOwnsConversation(c.Request.Context(), convID, fmt.Sprintf("%v", aid))
	return err == nil && okay
}

func (h *HTTP) agentCanAccessVisitor(c *gin.Context, visitorID string) bool {
	aid, ok := c.Get("agent_id")
	if !ok || visitorID == "" {
		return false
	}
	okay, err := h.svc.Store().AgentCanAccessVisitor(c.Request.Context(), visitorID, fmt.Sprintf("%v", aid))
	return err == nil && okay
}

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
	if !h.agentCanAccessVisitor(c, vid) {
		c.JSON(http.StatusForbidden, gin.H{"code": 40302, "msg": "访客不存在或无权访问"})
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

// MarkRead 标记已读（HTTP 兜底；正常实时走 WSS type=read）。同步推进 last_read_agent_at 并按游标重算 unread_agent。
// [115] 权限 =「能查看该会话聊天记录」（store.AgentCanReadConversation）：管理员，或会话/该访客任一未删除会话
// 分配给本人或未分配。解决「他人接管过、自己仍能看记录，但未读永远清不掉」的问题。
// 发送消息、接管、上传、关闭等写操作的归属规则不变（AgentOwnsConversation）。
// 会话不存在、已删除、ID 非法、无权查看一律 403（同一错误码，不区分存在性，防枚举），并写安全日志。
func (h *HTTP) MarkRead(c *gin.Context) {
	convID := c.Param("id")
	a := currentAgent(c)
	agentID := a.IDStr
	allowed := false
	if ValidConversationID(convID) {
		ok, err := h.svc.Store().AgentCanReadConversation(c.Request.Context(), convID, a.ID, a.Admin)
		if err != nil {
			requestID, _ := c.Get("request_id")
			h.svc.BizLog().Error("http_read_receipt_check_failed", zap.Error(err),
				zap.String("agent", agentID), zap.String("conv", convID),
				zap.String("request_id", fmt.Sprintf("%v", requestID)))
			c.JSON(http.StatusInternalServerError, gin.H{"code": 50009, "msg": "会话校验失败"})
			return
		}
		allowed = ok
	}
	if !allowed {
		requestID, _ := c.Get("request_id")
		h.svc.SecurityLog().Warn("http_read_receipt_forbidden",
			zap.String("agent", agentID), zap.String("agent_username", a.Username), zap.Bool("admin", a.Admin),
			zap.String("conv", logSafeParam(convID)), zap.Bool("conv_id_valid", ValidConversationID(convID)),
			zap.String("ip", security.ClientIP(c)), zap.String("path", c.FullPath()),
			zap.String("request_id", fmt.Sprintf("%v", requestID)))
		c.JSON(http.StatusForbidden, gin.H{"code": 40302, "msg": "会话不存在或无权操作"})
		return
	}
	if err := h.svc.Store().UpdateLastRead(c.Request.Context(), convID, "agent", time.Now()); err != nil {
		requestID, _ := c.Get("request_id")
		h.svc.BizLog().Error("http_read_receipt_update_failed", zap.Error(err),
			zap.String("agent", agentID), zap.String("conv", convID),
			zap.String("request_id", fmt.Sprintf("%v", requestID)))
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50009, "msg": "失败"})
		return
	}
	// 通过 WSS 把已读事件推给对端访客（让 UI 实时更新「已读」角标）
	h.hub.FanoutToConv(c.Request.Context(), &ws.Envelope{
		Type:   "read",
		From:   "agent:" + agentID,
		ConvID: convID,
		TS:     ws.NowMS(),
	})
	// [113] 打开会话即清除「标为未读」
	if vid, changed, err := h.svc.Store().ClearMarkedUnreadByConversation(c.Request.Context(), convID); err != nil {
		h.svc.BizLog().Warn("clear_marked_unread_failed", zap.Error(err), zap.String("conv", convID))
	} else if changed {
		h.svc.NotifyProfileUpdated(vid)
	}
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// CloseConv 关闭会话
func (h *HTTP) CloseConv(c *gin.Context) {
	convID := c.Param("id")
	if !h.agentCanAccessConversation(c, convID) {
		c.JSON(http.StatusForbidden, gin.H{"code": 40302, "msg": "会话不存在或无权操作"})
		return
	}
	if err := h.svc.Store().CloseConversation(c.Request.Context(), convID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50010, "msg": "失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// =========== 文件上传（访客或客服都用） ===========

// Upload 表单字段：file (二进制) + conv_id (可选) + uploader (visitor|agent) + ref (id)
// 必须带有效 token：访客拿 visitor_token、客服拿 agent_token，从 Authorization Bearer 取。
func (h *HTTP) Upload(c *gin.Context) {
	ctx := c.Request.Context()
	ip := security.ClientIP(c)
	maxBytes := int64(h.cfg.MaxUploadMB)*1024*1024 + 1024
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)

	uploadBy := c.PostForm("uploader")
	if uploadBy != "visitor" && uploadBy != "agent" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40003, "msg": "uploader 错误"})
		return
	}
	// [119] scope=hall：大厅附件（不需要 conv_id；文件归属记为大厅房间，hall_chat 只接受这类文件）
	scope := c.PostForm("scope")
	if scope != "" && scope != "hall" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40061, "msg": "scope 错误"})
		return
	}
	ref, convID, err := h.authorizeUpload(c, uploadBy, scope == "hall")
	if err != nil {
		var ue *uploadAuthError
		if errors.As(err, &ue) {
			body := gin.H{"code": ue.code, "msg": ue.msg}
			if ue.data != nil {
				body["data"] = ue.data
			}
			c.JSON(ue.status, body)
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40108, "msg": err.Error()})
		return
	}
	authToken := uploadAuthToken(c, uploadBy)
	if ok, err := h.svc.Limiter().AllowAction(ctx, "upload", 30, time.Minute, "ip:"+ip, "actor:"+ref, "token:"+authToken); err != nil || !ok {
		h.svc.Limiter().LogSecurityWarn(ip, "upload_rate_limited", ref)
		c.JSON(http.StatusTooManyRequests, gin.H{"code": 42903, "msg": "上传过于频繁，请稍后再试"})
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
	// 同时校验扩展名、服务端嗅探 MIME 与 magic bytes，不信任 multipart Content-Type。
	f, err := mh.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50011, "msg": "打开文件失败"})
		return
	}
	defer f.Close()
	sniff := make([]byte, 512)
	n, _ := f.Read(sniff)
	mimeType := http.DetectContentType(sniff[:n])
	valid, validateErr := security.ValidateUpload(mh.Filename, mimeType, sniff[:n])
	if validateErr != nil {
		h.svc.Limiter().LogSecurityWarn(ip, "upload_type_blocked", filepath.Ext(mh.Filename)+"|"+mimeType)
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"code": 41501, "msg": "文件扩展名、类型或文件头不匹配"})
		return
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
	storeKey := strings.ReplaceAll(filepath.Join(tz.Format("2006/01/02"), id+valid.Extension), "\\", "/")
	full := filepath.Join(subDir, id+valid.Extension)
	out, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50014, "msg": "写文件失败"})
		return
	}
	written, err := io.Copy(out, f)
	if err != nil || written > int64(h.cfg.MaxUploadMB)*1024*1024 {
		out.Close()
		os.Remove(full)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 50015, "msg": "写入失败"})
		} else {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"code": 41301, "msg": "文件过大"})
		}
		return
	}
	out.Close()

	accessToken, accessHash, err := security.NewOpaqueAccessToken()
	if err != nil {
		_ = os.Remove(full)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50016, "msg": "生成文件凭证失败"})
		return
	}
	rec := &store.FileRecord{
		ID: id, ConvID: convID, UploadBy: uploadBy, UploaderRef: ref,
		Filename: filepath.Base(mh.Filename), StoreKey: storeKey, Size: written, MIME: valid.MIME,
		AccessTokenHash: accessHash,
		CreatedAt:       time.Now(),
	}
	if err := h.svc.Store().InsertFile(ctx, rec); err != nil {
		os.Remove(full)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50016, "msg": "落库失败"})
		return
	}
	url := "/files/" + storeKey
	kind := valid.Kind
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"id":   id, "url": url + "?access=" + accessToken, "kind": kind, "size": written, "name": filepath.Base(mh.Filename), "mime": valid.MIME,
	})
}

// uploadAuthError 上传鉴权的结构化错误（状态码 + 业务码），让客户端能区分「过期可续期」与「无效需重登」。
type uploadAuthError struct {
	status int
	code   int
	msg    string
	data   map[string]any
}

func (e *uploadAuthError) Error() string { return e.msg }

// uploadAuthToken 取上传令牌：Authorization: Bearer 优先；客服回退 HttpOnly cs_session Cookie（与 agentAuth 中间件一致）。
// 访客令牌只认 Authorization 头（cs_session 是客服会话 Cookie）。
func uploadAuthToken(c *gin.Context, uploadBy string) string {
	tok := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	if tok == "" && uploadBy == "agent" {
		if cookie, err := c.Cookie("cs_session"); err == nil {
			tok = strings.TrimSpace(cookie)
		}
	}
	return tok
}

func (h *HTTP) authorizeUpload(c *gin.Context, uploadBy string, hallScope bool) (ref, convID string, err error) {
	tok := uploadAuthToken(c, uploadBy)
	if tok == "" {
		return "", "", errors.New("缺少 token")
	}
	if hallScope && h.hall == nil {
		return "", "", &uploadAuthError{status: http.StatusForbidden, code: 40371, msg: "大厅已关闭"}
	}
	switch uploadBy {
	case "visitor":
		vc, e := security.ParseVisitorToken(h.cfg.JWTSecret, tok)
		if e != nil {
			return "", "", errors.New("visitor token 无效")
		}
		if hallScope {
			if herr := h.hall.AuthorizeUpload(c.Request.Context(), hall.Actor{Visitor: true, VisitorID: vc.VisitorID, SiteID: vc.SiteID}); herr != nil {
				return "", "", hallUploadError(herr)
			}
			return vc.VisitorID, h.hall.Room(), nil
		}
		conv, convErr := h.svc.Store().OpenOrGetConversation(c.Request.Context(), vc.SiteID, vc.VisitorID)
		if convErr != nil || conv == nil {
			h.svc.BizLog().Error("upload_open_conversation_failed", zap.Error(convErr), zap.String("vid", vc.VisitorID))
			return "", "", &uploadAuthError{status: http.StatusInternalServerError, code: 50017, msg: "会话不可用，请稍后重试"}
		}
		return vc.VisitorID, conv.ID, nil
	case "agent":
		ac, e := security.ParseAgentToken(h.cfg.JWTSecret, tok)
		if e != nil {
			if errors.Is(e, security.ErrTokenExpired) {
				return "", "", &uploadAuthError{status: http.StatusUnauthorized, code: 40102, msg: "登录已过期"}
			}
			return "", "", errors.New("agent token 无效")
		}
		// 与 agentAuth 中间件一致：账号必须存在、启用且令牌版本未被吊销
		agent, dbErr := h.svc.Store().GetAgentByID(c.Request.Context(), ac.AgentID)
		if dbErr != nil || agent == nil || !agent.Active || agent.TokenVersion != ac.Version {
			return "", "", errors.New("会话已失效，请重新登录")
		}
		if hallScope {
			if herr := h.hall.AuthorizeUpload(c.Request.Context(), hall.Actor{AgentID: ac.AgentID, AgentUsername: ac.Username, Admin: agent.Role == "admin"}); herr != nil {
				return "", "", hallUploadError(herr)
			}
			return fmt.Sprintf("%d", ac.AgentID), h.hall.Room(), nil
		}
		convID = c.PostForm("conv_id")
		if convID == "" {
			return "", "", errors.New("缺少 conv_id")
		}
		owned, e := h.svc.Store().AgentOwnsConversation(c.Request.Context(), convID, fmt.Sprintf("%d", ac.AgentID))
		if e != nil || !owned {
			return "", "", errors.New("无权访问该会话")
		}
		return fmt.Sprintf("%d", ac.AgentID), convID, nil
	}
	return "", "", errors.New("未知 uploader")
}

// hallUploadError 把大厅业务错误转为上传响应。
func hallUploadError(err error) error {
	var he *hall.Error
	if errors.As(err, &he) {
		return &uploadAuthError{status: he.Status, code: he.Code, msg: he.Msg, data: he.Data}
	}
	return &uploadAuthError{status: http.StatusInternalServerError, code: 50050, msg: "服务器错误，请稍后重试"}
}

// normalizeFileKey 把路由 /files/*key 的通配参数规范为存储键。
// gin 的 *key 参数自带前导 "/"（如 "/2026/10/14/x.jpg"），必须先去掉这一个前导斜杠再做路径安全校验；
// 否则所有合法请求都会被当作绝对路径拒绝（v0.7.4 回归：图片/文件一律 400）。
// 仍拒绝：空键、路径穿越（..）、去掉前导斜杠后仍为绝对路径（如 "//etc/passwd"）、反斜杠与 NUL。
func normalizeFileKey(raw string) (string, bool) {
	key := strings.TrimPrefix(raw, "/")
	if key == "" || strings.Contains(key, "..") || strings.HasPrefix(key, "/") || filepath.IsAbs(key) || strings.ContainsAny(key, "\\\x00") {
		return "", false
	}
	cleanKey := filepath.ToSlash(filepath.Clean(filepath.FromSlash(key)))
	if cleanKey == "." || cleanKey == "" || strings.HasPrefix(cleanKey, "../") || strings.HasPrefix(cleanKey, "/") {
		return "", false
	}
	return cleanKey, true
}

// ServeFile 静态文件下载（受 token 保护：只能访问自己会话内的文件）
func (h *HTTP) ServeFile(c *gin.Context) {
	cleanKey, ok := normalizeFileKey(c.Param("key"))
	if !ok {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	rec, err := h.svc.Store().GetFileByStoreKey(c.Request.Context(), cleanKey)
	if err != nil || rec == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	access := c.Query("access")
	authorized := access != "" && security.HashOpaqueAccessToken(access) == rec.AccessTokenHash
	if !authorized {
		tok := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if tok == "" {
			if cookie, e := c.Cookie("cs_session"); e == nil {
				tok = cookie
			}
		}
		if tok != "" {
			if ac, e := security.ParseAgentToken(h.cfg.JWTSecret, tok); e == nil {
				agent, dbErr := h.svc.Store().GetAgentByID(c.Request.Context(), ac.AgentID)
				authorized = dbErr == nil && agent != nil && agent.Active && agent.TokenVersion == ac.Version &&
					(agent.Role == "admin" || func() bool {
						ok, _ := h.svc.Store().AgentOwnsConversation(c.Request.Context(), rec.ConvID, fmt.Sprintf("%d", ac.AgentID))
						return ok
					}())
			} else if vc, e := security.ParseVisitorToken(h.cfg.JWTSecret, tok); e == nil {
				authorized, _ = h.svc.Store().ConversationBelongsToVisitor(c.Request.Context(), rec.ConvID, vc.SiteID, vc.VisitorID)
			}
		}
	}
	if !authorized {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40109, "msg": "文件访问未授权"})
		return
	}
	full := filepath.Join(h.uploads, filepath.FromSlash(cleanKey))
	if _, err := os.Stat(full); err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	filename := strings.NewReplacer(`"`, "'", "\\", "_").Replace(filepath.Base(rec.Filename))
	if strings.HasPrefix(rec.MIME, "image/") && rec.MIME != "image/svg+xml" {
		c.Header("Content-Disposition", `inline; filename="`+filename+`"`)
	} else {
		c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	}
	c.Header("Content-Type", rec.MIME)
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
	minPasswordLen = 12 // 管理后台强制复杂密码
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
		c.JSON(http.StatusBadRequest, gin.H{"code": 40006, "msg": "密码至少 12 位"})
		return
	}
	if !security.StrongPassword(r.Password, r.Username) {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40014, "msg": "密码必须同时包含大小写字母、数字和符号，且不能与用户名相同"})
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
	"agent_notify_sound":    true,
	"visitor_notify_sound":  true,
	"notify_visitor_enter":  true,
	"session_fresh_minutes": true, // [085] 会话超时重建阈值(分钟)，默认 30
	"greeting_enabled":      true,
	"greeting_text":         true,
	"widget_title":          true,
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
	// [113] 自动清理：最近活动早于 N 天的会话自动移入回收站（"0" 关闭，7-3650）
	"auto_clean_days": true,
	// [119] 多人大厅（取值严格校验见 hall.ValidateSettingValue；迁移 015 写入默认值）
	hall.KeyEnabled:         true,
	hall.KeyVideoEnabled:    true,
	hall.KeyMaxSpeakers:     true,
	hall.KeyMaxCameras:      true,
	hall.KeyMaxParticipants: true,
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
	hallTouched := false
	for k, v := range body {
		if !allowedSettingKeys[k] {
			continue
		}
		// [119] 大厅设置：任一不合法整次拒绝，不做部分保存
		if hall.IsSettingKey(k) {
			norm, err := hall.ValidateSettingValue(k, v)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 40059, "msg": "大厅设置不合法：" + err.Error()})
				return
			}
			filtered[k] = norm
			hallTouched = true
			continue
		}
		if k == "auto_clean_days" {
			days, err := ParseAutoCleanDays(v)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 40058, "msg": "自动清理天数不合法（0 关闭，或 7-3650）"})
				return
			}
			v = days
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
	var hallBefore hall.Settings
	if hallTouched && h.hall != nil {
		hallBefore = h.hall.LoadSettingsFresh(c.Request.Context())
	}
	if err := h.svc.Store().SetSettings(c.Request.Context(), filtered); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50031, "msg": "保存失败"})
		return
	}
	actor, _ := c.Get("agent_username")
	detail, _ := json.Marshal(filtered)
	h.svc.Store().AuditLog(c.Request.Context(), fmt.Sprintf("%v", actor),
		"update_settings", "", string(security.RedactJSONPayload(detail)), security.ClientIP(c))
	// [119] 大厅设置变更：广播 hall_settings，关闭大厅/视频时后台强制执行（对账兜底）
	if hallTouched && h.hall != nil {
		after := h.hall.OnSettingsChanged(c.Request.Context(), hallBefore)
		h.auditAction(c, "hall_settings_update", "hall:"+h.hall.Room(), map[string]any{
			"enabled_before": hallBefore.Enabled, "enabled": after.Enabled,
			"video_before": hallBefore.VideoEnabled, "video": after.VideoEnabled,
			"max_speakers": after.MaxSpeakers, "max_cameras": after.MaxCameras, "max_participants": after.MaxParticipants,
		})
	}
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// VisitorPublicSettings 给访客 widget 拉取的公开子集（不需要 token）
func (h *HTTP) VisitorPublicSettings(c *gin.Context) {
	ctx := c.Request.Context()
	m, _ := h.svc.Store().GetSettingsMap(ctx, []string{
		"visitor_notify_sound", "widget_title", "voice_call_hint",
	})
	// [119] 大厅公开开关（布尔；未配置 LiveKit 时恒为 false）
	hallEnabled, hallVideo := false, false
	if h.hall != nil {
		hallEnabled, hallVideo = h.hall.PublicFlags(ctx)
	}
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"notify_sound":       defaultIfEmpty(m["visitor_notify_sound"], "visitor1"),
			"widget_title":       defaultIfEmpty(m["widget_title"], "在线客服"),
			"voice_call_hint":    defaultIfEmpty(m["voice_call_hint"], "直接呼叫客服"),
			"hall_enabled":       hallEnabled,
			"hall_video_enabled": hallVideo,
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
	if !body.Active {
		h.hub.CloseAgent(fmt.Sprintf("%d", body.ID))
	}
	c.JSON(http.StatusOK, gin.H{"code": 0})
}
