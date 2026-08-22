package middleware

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/security"
)

// AccessLog 记录每条 HTTP 请求到 business 日志（爷爷铁律：细致、原始）。
func AccessLog(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		requestID := c.GetHeader("X-Request-ID")
		if !security.ValidTraceID(requestID) {
			requestID = uuid.NewString()
		}
		traceID := c.GetHeader("X-Trace-ID")
		if !security.ValidTraceID(traceID) {
			traceID = requestID
		}
		c.Set("request_id", requestID)
		c.Set("trace_id", traceID)
		c.Header("X-Request-ID", requestID)
		c.Header("X-Trace-ID", traceID)
		c.Next()
		log.Info("HTTP request completed",
			zap.String("trace_id", traceID),
			zap.String("request_id", requestID),
			zap.String("event", "http_access"),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("query", security.RedactRawQuery(c.Request.URL.RawQuery)),
			zap.String("ip", security.ClientIP(c)),
			zap.String("ua", c.Request.UserAgent()),
			zap.Int("status", c.Writer.Status()),
			zap.Int64("request_bytes", c.Request.ContentLength),
			zap.Int("response_bytes", c.Writer.Size()),
			zap.Int64("latency_ms", time.Since(start).Milliseconds()))
	}
}

// Recovery 兜底 panic（生产严禁让 panic 把进程拉崩）。
func Recovery(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				traceID, _ := c.Get("trace_id")
				log.Error("HTTP panic recovered", zap.String("event", "http_panic"), zap.Any("err", r), zap.String("path", c.Request.URL.Path),
					zap.Any("trace_id", traceID), zap.Stack("stack"))
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"code": 50000, "msg": "服务器内部异常", "trace_id": traceID,
				})
			}
		}()
		c.Next()
	}
}

// SecurityHeaders 给所有响应加安全头。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "interest-cohort=()")
		// HSTS（前提是 ENABLE_HTTPS=true，由 Nginx 决定；后端也带上更稳）
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Next()
	}
}

// AgentAuth 校验客服 / 管理员 JWT。
// [064] 区分错误码让客户端能精准走「过期 → refresh」还是「无效 → 重登」分支：
//
//	code=40101 未登录（缺 Authorization header）→ 客户端走登录页
//	code=40102 登录已过期（token expired）       → 客户端可调 /agent/login/refresh 续 token
//	code=40103 token 无效（签名错 / 篡改）       → 客户端走登录页
func AgentAuth(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if tok == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40101, "msg": "未登录"})
			return
		}
		claims, err := security.ParseAgentToken(secret, tok)
		if err != nil {
			if errors.Is(err, security.ErrTokenExpired) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40102, "msg": "登录已过期"})
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40103, "msg": "token 无效"})
			return
		}
		c.Set("agent_id", claims.AgentID)
		c.Set("agent_username", claims.Username)
		c.Set("agent_role", claims.Role)
		c.Next()
	}
}

// AdminOnly 仅 admin 角色。
func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("agent_role")
		if role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 40301, "msg": "权限不足"})
			return
		}
		c.Next()
	}
}
