package handler

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/config"
	"github.com/custom-service/backend/internal/security"
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
		"tz":       "Asia/Shanghai",
		"now":      tz,
		"visitors": h.hub.OnlineVisitorCount(),
		"agents":   h.hub.OnlineAgentCount(),
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

	now := time.Now()
	v := &store.Visitor{
		ID:         r.VisitorID,
		SiteID:     r.SiteID,
		IPCipher:   ipCipher,
		UA:         r.UA,
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
	conv, err := h.svc.Store().OpenOrGetConversation(c.Request.Context(), v.SiteID, v.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50002, "msg": "创建会话失败"})
		return
	}
	tok, err := security.IssueVisitorToken(h.cfg.JWTSecret, v.ID, v.SiteID, 12*time.Hour)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50003, "msg": "签发 token 失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":          0,
		"visitor_id":    v.ID,
		"conversation":  conv.ID,
		"visitor_token": tok,
		"server_now":    time.Now().In(h.cfg.Timezone).Format("2006-01-02 15:04:05"),
	})
}

// VisitorWS 访客的 WSS 入口。query: token=<visitor_token>
func (h *HTTP) VisitorWS(c *gin.Context) {
	ip := security.ClientIP(c)
	ctx := c.Request.Context()
	if ok, _ := h.svc.Limiter().AllowWSHandshake(ctx, ip, h.cfg.IPWSHandshakePM); !ok {
		h.svc.Limiter().RecordViolation(ctx, ip, "ws_handshake_flood", "/ws/visitor")
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"code": 42903, "msg": "握手太频繁"})
		return
	}
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
		h.svc.Limiter().RecordViolation(ctx, ip, "agent_login_fail_nouser", r.Username)
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40105, "msg": "账号或密码错误"})
		return
	}
	if !a.Active {
		c.JSON(http.StatusForbidden, gin.H{"code": 40302, "msg": "账号已禁用"})
		return
	}
	if !security.CheckPassword(a.PassHash, r.Password) {
		h.svc.Limiter().RecordViolation(ctx, ip, "agent_login_fail_password", r.Username)
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

// AgentWS 客服的 WSS 入口。query: token=<agent_token>
func (h *HTTP) AgentWS(c *gin.Context) {
	ip := security.ClientIP(c)
	ctx := c.Request.Context()
	if ok, _ := h.svc.Limiter().AllowWSHandshake(ctx, ip, h.cfg.IPWSHandshakePM); !ok {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"code": 42904, "msg": "握手太频繁"})
		return
	}
	tok := c.Query("token")
	if tok == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40106, "msg": "缺少 token"})
		return
	}
	claims, err := security.ParseAgentToken(h.cfg.JWTSecret, tok)
	if err != nil {
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

// MarkRead 标记已读
func (h *HTTP) MarkRead(c *gin.Context) {
	convID := c.Param("id")
	role, _ := c.Get("agent_role")
	_ = role
	if err := h.svc.Store().MarkRead(c.Request.Context(), convID, "agent"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50009, "msg": "失败"})
		return
	}
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
			h.svc.Limiter().RecordViolation(ctx, ip, "upload_mime_blocked", mimeType+"|"+ext)
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

func (h *HTTP) CreateAgent(c *gin.Context) {
	var r CreateAgentReq
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40005, "msg": "参数错误"})
		return
	}
	if len(r.Password) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40006, "msg": "密码至少 8 位"})
		return
	}
	if r.Role != "admin" {
		r.Role = "agent"
	}
	hash, err := security.HashPassword(r.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50017, "msg": "失败"})
		return
	}
	id, err := h.svc.Store().CreateAgent(c.Request.Context(), &store.Agent{
		Username: r.Username, PassHash: hash, Role: r.Role, Nickname: r.Nickname,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40007, "msg": "用户名已存在或失败"})
		return
	}
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

