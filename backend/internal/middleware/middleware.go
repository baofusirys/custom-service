package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/security"
)

// AccessLog 记录每条 HTTP 请求到 business 日志（爷爷铁律：细致、原始）。
func AccessLog(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery
		c.Next()
		log.Info("http_access",
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("query", raw),
			zap.String("ip", security.ClientIP(c)),
			zap.String("ua", c.Request.UserAgent()),
			zap.Int("status", c.Writer.Status()),
			zap.Int("size", c.Writer.Size()),
			zap.Duration("latency", time.Since(start)))
	}
}

// Recovery 兜底 panic（生产严禁让 panic 把进程拉崩）。
func Recovery(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic", zap.Any("err", r), zap.String("path", c.Request.URL.Path))
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"code": 50000, "msg": "服务器内部异常",
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
func AgentAuth(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if tok == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40101, "msg": "未登录"})
			return
		}
		claims, err := security.ParseAgentToken(secret, tok)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40102, "msg": "登录已过期"})
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
