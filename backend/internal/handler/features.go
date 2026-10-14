package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/export"
	"github.com/custom-service/backend/internal/security"
	"github.com/custom-service/backend/internal/store"
)

// [113] 0.8.0 新功能接口：删除/回收站、快捷回复、客户资料（备注/标签/置顶/未读/拉黑）、导出、全局搜索、撤回。
// 契约见 _internal/reports/api-0.8.0-features.md。所有写操作：权限校验 → 多维限流 → 参数化 SQL → 审计日志。

const (
	maxDeleteIDs       = 200
	maxExportVisitors  = 50
	maxExportRows      = 50000
	quickPersonalLimit = 100
	quickTeamLimit     = 200
	confirmDeleteAll   = "删除全部"
	confirmPurgeAll    = "清空回收站"
)

type agentContext struct {
	ID       int64
	IDStr    string
	Username string
	Admin    bool
}

func currentAgent(c *gin.Context) agentContext {
	var a agentContext
	if v, ok := c.Get("agent_id"); ok {
		if id, ok := v.(int64); ok {
			a.ID = id
		}
	}
	a.IDStr = fmt.Sprintf("%d", a.ID)
	if v, ok := c.Get("agent_username"); ok {
		a.Username = fmt.Sprintf("%v", v)
	}
	if v, ok := c.Get("agent_role"); ok {
		a.Admin = fmt.Sprintf("%v", v) == "admin"
	}
	return a
}

func fail(c *gin.Context, status, code int, msg string) {
	c.JSON(status, gin.H{"code": code, "msg": msg})
}

// allowAgentAction 按「客服账号 + IP」双维度限流；Redis 不可用时拒绝（防止限流失效被滥用）。
func (h *HTTP) allowAgentAction(c *gin.Context, action string, limit int, window time.Duration) bool {
	a := currentAgent(c)
	ok, err := h.svc.Limiter().AllowAction(c.Request.Context(), action, limit, window, "agent:"+a.IDStr, "ip:"+security.ClientIP(c))
	if err != nil || !ok {
		h.svc.Limiter().LogSecurityWarn(security.ClientIP(c), action+"_rate_limited", a.Username)
		fail(c, http.StatusTooManyRequests, 42910, "操作过于频繁，请稍后再试")
		return false
	}
	return true
}

// auditAction 审计：写入审计表（结构化可检索）+ 审计日志文件（长效留存）。detail 不含消息正文与凭证。
func (h *HTTP) auditAction(c *gin.Context, action, target string, detail map[string]any) {
	a := currentAgent(c)
	raw, _ := json.Marshal(detail)
	h.svc.Store().AuditLog(c.Request.Context(), a.Username, action, target, string(raw), security.ClientIP(c))
	reqID, _ := c.Get("request_id")
	h.svc.AuditLog().Info(action,
		zap.String("actor", a.Username), zap.Int64("aid", a.ID), zap.String("target", target),
		zap.String("ip", security.ClientIP(c)), zap.String("request_id", fmt.Sprintf("%v", reqID)), zap.ByteString("detail", raw))
}

func (h *HTTP) serverError(c *gin.Context, event string, err error) {
	reqID, _ := c.Get("request_id")
	h.svc.BizLog().Error(event, zap.Error(err), zap.String("request_id", fmt.Sprintf("%v", reqID)), zap.Stack("stack"))
	fail(c, http.StatusInternalServerError, 50050, "服务器错误，请稍后重试")
}

// canReadVisitor 读取/删除/导出聊天记录：客户有未删除会话，且（管理员 或 会话分配给本人/未分配）。
func (h *HTTP) canReadVisitor(ctx context.Context, a agentContext, vid string) bool {
	if a.Admin {
		ok, err := h.svc.Store().VisitorHasActiveHistory(ctx, vid)
		return err == nil && ok
	}
	ok, err := h.svc.Store().AgentCanAccessVisitor(ctx, vid, a.IDStr)
	return err == nil && ok
}

// canManageVisitor 管理客户资料（备注/标签/置顶/未读/拉黑）：管理员 或 有分配给本人/未分配的会话（含回收站中）。
func (h *HTTP) canManageVisitor(ctx context.Context, a agentContext, vid string) bool {
	if a.Admin {
		ok, err := h.svc.Store().VisitorExists(ctx, vid)
		return err == nil && ok
	}
	ok, err := h.svc.Store().AgentCanManageVisitor(ctx, vid, a.IDStr)
	return err == nil && ok
}

func (h *HTTP) visitorParam(c *gin.Context, manage bool) (string, bool) {
	vid := c.Param("vid")
	if !ValidVisitorID(vid) {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return "", false
	}
	a := currentAgent(c)
	allowed := false
	if manage {
		allowed = h.canManageVisitor(c.Request.Context(), a, vid)
	} else {
		allowed = h.canReadVisitor(c.Request.Context(), a, vid)
	}
	if !allowed {
		h.svc.SecurityLog().Warn("visitor_access_forbidden", zap.String("agent", a.Username), zap.String("vid", vid), zap.String("path", c.FullPath()))
		fail(c, http.StatusForbidden, 40311, "无权操作该客户")
		return "", false
	}
	return vid, true
}

func (h *HTTP) decryptIP(cipher string) string {
	if cipher == "" {
		return ""
	}
	ip, err := h.svc.Cipher().Decrypt(cipher)
	if err != nil {
		return ""
	}
	return ip
}

// ============ 删除与回收站 ============

type visitorIDsReq struct {
	VisitorIDs []string `json:"visitor_ids"`
}

// DeleteVisitors 多选删除（移入回收站，7 天内可恢复）。无权访问的客户跳过并计入 skipped。
func (h *HTTP) DeleteVisitors(c *gin.Context) {
	var r visitorIDsReq
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	ids, err := ParseIDList(r.VisitorIDs, maxDeleteIDs)
	if errors.Is(err, errTooManyIDs) {
		fail(c, http.StatusBadRequest, 40051, "一次最多删除 200 位客户")
		return
	}
	if err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	if !h.allowAgentAction(c, "delete_visitors", 30, time.Minute) {
		return
	}
	a := currentAgent(c)
	ctx := c.Request.Context()
	allowed := make([]string, 0, len(ids))
	for _, vid := range ids {
		if h.canReadVisitor(ctx, a, vid) {
			allowed = append(allowed, vid)
		}
	}
	res := store.DeleteResult{}
	if len(allowed) > 0 {
		res, err = h.svc.DeleteVisitors(ctx, allowed, a.Username)
		if err != nil {
			h.serverError(c, "delete_visitors_failed", err)
			return
		}
	}
	res.Skipped = len(ids) - len(allowed)
	h.auditAction(c, "delete_visitors", fmt.Sprintf("visitors=%d", len(allowed)), map[string]any{
		"visitor_ids": allowed, "conversations": res.Conversations, "messages": res.Messages, "skipped": res.Skipped})
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": res})
}

// DeleteAllVisitors 一键删除全部聊天记录（仅管理员，需输入确认文字）。
func (h *HTTP) DeleteAllVisitors(c *gin.Context) {
	var r struct {
		Confirm string `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&r); err != nil || strings.TrimSpace(r.Confirm) != confirmDeleteAll {
		fail(c, http.StatusBadRequest, 40052, "请输入「删除全部」确认")
		return
	}
	if !h.allowAgentAction(c, "delete_all_visitors", 3, 10*time.Minute) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	start := time.Now()
	res, err := h.svc.DeleteAllVisitors(ctx, currentAgent(c).Username)
	if err != nil {
		h.serverError(c, "delete_all_visitors_failed", err)
		return
	}
	h.auditAction(c, "delete_all_visitors", "all", map[string]any{
		"visitors": res.Visitors, "conversations": res.Conversations, "messages": res.Messages, "duration_ms": time.Since(start).Milliseconds()})
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": res})
}

// ListRecycleBin 回收站列表（仅管理员）。
func (h *HTTP) ListRecycleBin(c *gin.Context) {
	offset, limit := pageParams(c.Query("offset"), c.Query("limit"))
	items, total, err := h.svc.Store().ListRecycleBin(c.Request.Context(), limit, offset)
	if err != nil {
		h.serverError(c, "list_recycle_bin_failed", err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, it := range items {
		out = append(out, gin.H{"visitor_id": it.VisitorID, "identifier": it.Identifier, "ip": h.decryptIP(it.IPCipher),
			"conversations": it.Conversations, "messages": it.Messages, "deleted_at": it.DeletedAt,
			"deleted_by": it.DeletedBy, "purge_at": it.PurgeAt})
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": out, "total": total})
}

// RestoreRecycleBin 从回收站恢复（仅管理员）。
func (h *HTTP) RestoreRecycleBin(c *gin.Context) {
	var r visitorIDsReq
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	ids, err := ParseIDList(r.VisitorIDs, maxDeleteIDs)
	if err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	if !h.allowAgentAction(c, "recycle_restore", 30, time.Minute) {
		return
	}
	v, n, err := h.svc.RestoreVisitors(c.Request.Context(), ids)
	if err != nil {
		h.serverError(c, "recycle_restore_failed", err)
		return
	}
	h.auditAction(c, "recycle_restore", fmt.Sprintf("visitors=%d", v), map[string]any{"visitor_ids": ids, "conversations": n})
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"visitors": v, "conversations": n}})
}

// PurgeRecycleBin 彻底删除（仅管理员）：指定客户，或输入确认文字后清空回收站。
func (h *HTTP) PurgeRecycleBin(c *gin.Context) {
	var r struct {
		VisitorIDs []string `json:"visitor_ids"`
		All        bool     `json:"all"`
		Confirm    string   `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	var ids []string
	if r.All {
		if strings.TrimSpace(r.Confirm) != confirmPurgeAll {
			fail(c, http.StatusBadRequest, 40052, "请输入「清空回收站」确认")
			return
		}
	} else {
		var err error
		if ids, err = ParseIDList(r.VisitorIDs, maxDeleteIDs); err != nil {
			fail(c, http.StatusBadRequest, 40050, "参数错误")
			return
		}
	}
	if !h.allowAgentAction(c, "recycle_purge", 10, time.Minute) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := h.svc.PurgeVisitors(ctx, ids, r.All)
	if err != nil {
		h.serverError(c, "recycle_purge_failed", err)
		return
	}
	target := fmt.Sprintf("visitors=%d", len(ids))
	if r.All {
		target = "all"
	}
	h.auditAction(c, "recycle_purge", target, map[string]any{"visitor_ids": ids, "conversations": res.Conversations, "messages": res.Messages, "files": res.Files})
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": res})
}

// ============ 快捷回复 ============

func (h *HTTP) ListQuickReplies(c *gin.Context) {
	list, err := h.svc.Store().ListQuickReplies(c.Request.Context(), currentAgent(c).ID)
	if err != nil {
		h.serverError(c, "list_quick_replies_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": list})
}

type quickReplyReq struct {
	Scope     string `json:"scope"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	SortOrder int    `json:"sort_order"`
}

func clampSort(n int) int {
	if n < -100000 {
		return -100000
	}
	if n > 100000 {
		return 100000
	}
	return n
}

func (h *HTTP) CreateQuickReply(c *gin.Context) {
	var r quickReplyReq
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	title, content, err := NormalizeQuickReply(r.Title, r.Content)
	if err != nil {
		fail(c, http.StatusBadRequest, 40055, "标题最多 32 字，内容需 1-1000 字")
		return
	}
	a := currentAgent(c)
	var owner *int64
	limit := quickTeamLimit
	switch r.Scope {
	case "team":
		if !a.Admin {
			fail(c, http.StatusForbidden, 40312, "团队快捷回复仅管理员可管理")
			return
		}
	case "personal", "":
		id := a.ID
		owner = &id
		limit = quickPersonalLimit
	default:
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	if !h.allowAgentAction(c, "quick_reply_write", 60, time.Minute) {
		return
	}
	ctx := c.Request.Context()
	n, err := h.svc.Store().CountQuickReplies(ctx, owner)
	if err != nil {
		h.serverError(c, "count_quick_replies_failed", err)
		return
	}
	if n >= limit {
		fail(c, http.StatusConflict, 40912, fmt.Sprintf("快捷回复最多 %d 条", limit))
		return
	}
	id, err := h.svc.Store().CreateQuickReply(ctx, owner, title, content, clampSort(r.SortOrder), a.Username)
	if err != nil {
		h.serverError(c, "create_quick_reply_failed", err)
		return
	}
	h.auditAction(c, "quick_reply_create", fmt.Sprintf("quick_reply:%d", id), map[string]any{"scope": scopeName(owner), "title": title, "content_len": len([]rune(content))})
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"id": id}})
}

func scopeName(owner *int64) string {
	if owner == nil {
		return "team"
	}
	return "personal"
}

// loadOwnQuickReply 读取并校验可编辑：团队话术需管理员；个人话术只能本人（他人的返回不存在，避免枚举）。
func (h *HTTP) loadOwnQuickReply(c *gin.Context) (*store.QuickReply, bool) {
	id, ok := ParseNumericID(c.Param("id"))
	if !ok {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return nil, false
	}
	q, err := h.svc.Store().GetQuickReply(c.Request.Context(), id)
	if errors.Is(err, store.ErrQuickReplyNotFound) {
		fail(c, http.StatusNotFound, 40411, "快捷回复不存在")
		return nil, false
	}
	if err != nil {
		h.serverError(c, "get_quick_reply_failed", err)
		return nil, false
	}
	a := currentAgent(c)
	if q.AgentID.Valid {
		if q.AgentID.Int64 != a.ID {
			fail(c, http.StatusNotFound, 40411, "快捷回复不存在")
			return nil, false
		}
	} else if !a.Admin {
		fail(c, http.StatusForbidden, 40312, "团队快捷回复仅管理员可管理")
		return nil, false
	}
	return q, true
}

func (h *HTTP) UpdateQuickReply(c *gin.Context) {
	q, ok := h.loadOwnQuickReply(c)
	if !ok {
		return
	}
	var r quickReplyReq
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	title, content, err := NormalizeQuickReply(r.Title, r.Content)
	if err != nil {
		fail(c, http.StatusBadRequest, 40055, "标题最多 32 字，内容需 1-1000 字")
		return
	}
	if !h.allowAgentAction(c, "quick_reply_write", 60, time.Minute) {
		return
	}
	if err := h.svc.Store().UpdateQuickReply(c.Request.Context(), q.ID, title, content, clampSort(r.SortOrder)); err != nil {
		h.serverError(c, "update_quick_reply_failed", err)
		return
	}
	h.auditAction(c, "quick_reply_update", fmt.Sprintf("quick_reply:%d", q.ID), map[string]any{"scope": q.Scope, "title_before": q.Title, "title_after": title})
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

func (h *HTTP) DeleteQuickReply(c *gin.Context) {
	q, ok := h.loadOwnQuickReply(c)
	if !ok {
		return
	}
	if !h.allowAgentAction(c, "quick_reply_write", 60, time.Minute) {
		return
	}
	if err := h.svc.Store().DeleteQuickReply(c.Request.Context(), q.ID); err != nil {
		h.serverError(c, "delete_quick_reply_failed", err)
		return
	}
	h.auditAction(c, "quick_reply_delete", fmt.Sprintf("quick_reply:%d", q.ID), map[string]any{"scope": q.Scope, "title": q.Title})
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// ============ 客户资料：备注 / 标签 / 置顶 / 未读 / 拉黑 ============

func (h *HTTP) GetVisitorProfile(c *gin.Context) {
	vid, ok := h.visitorParam(c, true)
	if !ok {
		return
	}
	p, err := h.svc.Store().GetVisitorProfile(c.Request.Context(), vid)
	if err != nil {
		h.serverError(c, "get_visitor_profile_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": p})
}

func (h *HTTP) SaveVisitorProfile(c *gin.Context) {
	vid, ok := h.visitorParam(c, true)
	if !ok {
		return
	}
	var r struct {
		Note string   `json:"note"`
		Tags []string `json:"tags"`
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	note, err := NormalizeNote(r.Note)
	if err != nil {
		fail(c, http.StatusBadRequest, 40054, "备注最多 500 字")
		return
	}
	tags, err := NormalizeTags(r.Tags)
	if err != nil {
		fail(c, http.StatusBadRequest, 40053, "标签每个 1-16 字，最多 10 个")
		return
	}
	if !h.allowAgentAction(c, "visitor_profile_write", 120, time.Minute) {
		return
	}
	ctx := c.Request.Context()
	before, _ := h.svc.Store().GetVisitorProfile(ctx, vid)
	if err := h.svc.Store().SaveVisitorProfile(ctx, vid, note, tags, currentAgent(c).Username); err != nil {
		h.serverError(c, "save_visitor_profile_failed", err)
		return
	}
	detail := map[string]any{"tags_after": tags, "note_len_after": len([]rune(note))}
	if before != nil {
		detail["tags_before"] = before.Tags
		detail["note_len_before"] = len([]rune(before.Note))
	}
	h.auditAction(c, "update_visitor_profile", "visitor:"+vid, detail)
	h.svc.NotifyProfileUpdated(vid)
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

func (h *HTTP) ListTags(c *gin.Context) {
	tags, err := h.svc.Store().ListTags(c.Request.Context(), 100)
	if err != nil {
		h.serverError(c, "list_tags_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": tags})
}

func (h *HTTP) SetVisitorPinned(c *gin.Context) {
	vid, ok := h.visitorParam(c, true)
	if !ok {
		return
	}
	var r struct {
		Pinned bool `json:"pinned"`
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	if !h.allowAgentAction(c, "visitor_profile_write", 120, time.Minute) {
		return
	}
	if err := h.svc.Store().SetVisitorPinned(c.Request.Context(), vid, r.Pinned, currentAgent(c).Username); err != nil {
		h.serverError(c, "set_visitor_pinned_failed", err)
		return
	}
	h.auditAction(c, "update_visitor_profile", "visitor:"+vid, map[string]any{"pinned": r.Pinned})
	h.svc.NotifyProfileUpdated(vid)
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

func (h *HTTP) SetVisitorUnread(c *gin.Context) {
	vid, ok := h.visitorParam(c, true)
	if !ok {
		return
	}
	var r struct {
		Unread bool `json:"unread"`
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	if !h.allowAgentAction(c, "visitor_profile_write", 120, time.Minute) {
		return
	}
	if err := h.svc.Store().SetVisitorMarkedUnread(c.Request.Context(), vid, r.Unread, currentAgent(c).Username); err != nil {
		h.serverError(c, "set_visitor_unread_failed", err)
		return
	}
	h.auditAction(c, "update_visitor_profile", "visitor:"+vid, map[string]any{"marked_unread": r.Unread})
	h.svc.NotifyProfileUpdated(vid)
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

func (h *HTTP) SetVisitorBlocked(c *gin.Context) {
	vid, ok := h.visitorParam(c, true)
	if !ok {
		return
	}
	var r struct {
		Blocked bool   `json:"blocked"`
		Reason  string `json:"reason"`
		BlockIP bool   `json:"block_ip"`
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	if !h.allowAgentAction(c, "visitor_block", 30, time.Minute) {
		return
	}
	reason := NormalizeReason(r.Reason)
	if err := h.svc.SetVisitorBlocked(c.Request.Context(), vid, r.Blocked, reason, currentAgent(c).Username, r.BlockIP); err != nil {
		h.serverError(c, "set_visitor_blocked_failed", err)
		return
	}
	action := "unblock_visitor"
	if r.Blocked {
		action = "block_visitor"
	}
	h.auditAction(c, action, "visitor:"+vid, map[string]any{"reason": reason, "block_ip": r.BlockIP && r.Blocked})
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

func (h *HTTP) ListBlockedVisitors(c *gin.Context) {
	offset, limit := pageParams(c.Query("offset"), c.Query("limit"))
	items, total, err := h.svc.Store().ListBlockedVisitors(c.Request.Context(), limit, offset)
	if err != nil {
		h.serverError(c, "list_blocked_failed", err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, b := range items {
		out = append(out, gin.H{"visitor_id": b.VisitorID, "identifier": b.Identifier, "ip": h.decryptIP(b.IPCipher),
			"reason": b.Reason, "blocked_by": b.BlockedBy, "blocked_at": b.BlockedAt, "ip_blocked": b.IPBlocked})
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": out, "total": total})
}

// ============ 导出 ============

func (h *HTTP) ExportVisitor(c *gin.Context) {
	vid, ok := h.visitorParam(c, false)
	if !ok {
		return
	}
	h.exportVisitors(c, []string{vid}, c.Query("format"))
}

func (h *HTTP) ExportVisitors(c *gin.Context) {
	var r struct {
		VisitorIDs []string `json:"visitor_ids"`
		Format     string   `json:"format"`
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	ids, err := ParseIDList(r.VisitorIDs, maxExportVisitors)
	if errors.Is(err, errTooManyIDs) {
		fail(c, http.StatusBadRequest, 40051, "一次最多导出 50 位客户")
		return
	}
	if err != nil {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	a := currentAgent(c)
	allowed := make([]string, 0, len(ids))
	for _, vid := range ids {
		if h.canReadVisitor(c.Request.Context(), a, vid) {
			allowed = append(allowed, vid)
		}
	}
	if len(allowed) == 0 {
		fail(c, http.StatusForbidden, 40311, "无权操作该客户")
		return
	}
	h.exportVisitors(c, allowed, r.Format)
}

func senderLabel(sender string) string {
	switch sender {
	case "visitor":
		return "访客"
	case "agent":
		return "客服"
	default:
		return "系统"
	}
}

func customerName(identifier, vid string) string {
	if strings.TrimSpace(identifier) != "" {
		return strings.TrimSpace(identifier)
	}
	if len(vid) > 6 {
		vid = vid[:6]
	}
	return "访客 " + vid
}

// exportContent 导出时的消息内容：已撤回 → [已撤回]；附件 → [图片]/[文件] 文件名。
func exportContent(r store.ExportRow) string {
	if r.Recalled {
		return "[已撤回]"
	}
	if r.MediaKind != "" {
		label := "[文件]"
		if r.MediaKind == "image" {
			label = "[图片]"
		}
		parts := []string{label}
		if r.MediaName != "" {
			parts = append(parts, r.MediaName)
		}
		if r.Content != "" {
			parts = append(parts, r.Content)
		}
		return strings.Join(parts, " ")
	}
	return r.Content
}

func (h *HTTP) exportVisitors(c *gin.Context, vids []string, format string) {
	if format == "" {
		format = "xlsx"
	}
	if format != "xlsx" && format != "txt" {
		fail(c, http.StatusBadRequest, 40057, "导出格式不支持")
		return
	}
	if !h.allowAgentAction(c, "export_chats", 10, time.Minute) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	rows, err := h.svc.Store().ExportMessages(ctx, vids, maxExportRows)
	if err != nil {
		h.serverError(c, "export_query_failed", err)
		return
	}
	now := time.Now().In(h.cfg.Timezone)
	stamp := now.Format("20060102-150405")
	name := fmt.Sprintf("聊天记录-%d位客户-%s", len(vids), stamp)
	if len(vids) == 1 {
		ident := vids[0]
		if len(rows) > 0 {
			ident = customerName(rows[0].Identifier, rows[0].VisitorID)
		}
		name = fmt.Sprintf("聊天记录-%s-%s", ident, stamp)
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	h.auditAction(c, "export_chats", fmt.Sprintf("visitors=%d", len(vids)), map[string]any{"visitor_ids": vids, "format": format, "messages": len(rows)})
	if format == "txt" {
		c.Header("Content-Disposition", export.ContentDisposition(name+".txt", "chat-export-"+stamp+".txt"))
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.Status(http.StatusOK)
		w := c.Writer
		fmt.Fprintf(w, "聊天记录导出\n导出时间：%s（北京时间）\n客户数：%d  消息数：%d\n", now.Format("2006-01-02 15:04:05"), len(vids), len(rows))
		last := ""
		for _, r := range rows {
			if r.VisitorID != last {
				fmt.Fprintf(w, "\n==== 客户：%s ====\n", customerName(r.Identifier, r.VisitorID))
				last = r.VisitorID
			}
			fmt.Fprintf(w, "[%s] %s：%s\n", r.CreatedAt.In(h.cfg.Timezone).Format("2006-01-02 15:04:05"), senderLabel(r.Sender), exportContent(r))
		}
		return
	}
	c.Header("Content-Disposition", export.ContentDisposition(name+".xlsx", "chat-export-"+stamp+".xlsx"))
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Status(http.StatusOK)
	err = export.WriteXLSX(c.Writer, "聊天记录", []string{"时间（北京时间）", "客户", "发送方", "内容"}, []float64{21, 18, 8, 80},
		func(yield func([]string) error) error {
			for _, r := range rows {
				if err := yield([]string{r.CreatedAt.In(h.cfg.Timezone).Format("2006-01-02 15:04:05"),
					customerName(r.Identifier, r.VisitorID), senderLabel(r.Sender), exportContent(r)}); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		h.svc.BizLog().Error("export_write_failed", zap.Error(err))
	}
}

// ============ 全局搜索 ============

func (h *HTTP) SearchMessages(c *gin.Context) {
	q, err := NormalizeSearchQuery(c.Query("q"))
	if err != nil {
		fail(c, http.StatusBadRequest, 40056, "搜索关键词需 2-64 个字符")
		return
	}
	if !h.allowAgentAction(c, "search_messages", 30, time.Minute) {
		return
	}
	_, limit := pageParams("0", c.Query("limit"))
	if limit > 50 {
		limit = 50
	}
	a := currentAgent(c)
	scope := a.IDStr
	if a.Admin {
		scope = ""
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	hits, err := h.svc.Store().SearchMessages(ctx, q, scope, limit)
	if err != nil {
		h.serverError(c, "search_messages_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": hits})
}

// ============ 撤回 ============

func (h *HTTP) RecallMessage(c *gin.Context) {
	id := c.Param("id")
	if !ValidVisitorID(id) {
		fail(c, http.StatusBadRequest, 40050, "参数错误")
		return
	}
	if !h.allowAgentAction(c, "recall_message", 30, time.Minute) {
		return
	}
	a := currentAgent(c)
	convID, err := h.svc.RecallMessage(c.Request.Context(), id, a.IDStr, a.Username)
	switch {
	case errors.Is(err, store.ErrRecallNotFound):
		fail(c, http.StatusNotFound, 40410, "消息不存在")
		return
	case errors.Is(err, store.ErrRecallForbidden):
		h.svc.SecurityLog().Warn("recall_forbidden", zap.String("agent", a.Username), zap.String("message_id", id))
		fail(c, http.StatusForbidden, 40310, "只能撤回自己发送的消息")
		return
	case errors.Is(err, store.ErrRecallExpired):
		fail(c, http.StatusConflict, 40910, "超过 2 分钟，无法撤回")
		return
	case errors.Is(err, store.ErrRecallAlready):
		fail(c, http.StatusConflict, 40911, "消息已撤回")
		return
	case err != nil:
		h.serverError(c, "recall_message_failed", err)
		return
	}
	h.auditAction(c, "recall_message", "message:"+id, map[string]any{"conv": convID})
	c.JSON(http.StatusOK, gin.H{"code": 0})
}
