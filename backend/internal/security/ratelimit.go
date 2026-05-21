package security

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// 防 DDoS / 防同 IP 暴力请求 / 防访客刷消息（爷爷铁律）：
//
//   1. 按 IP 的 HTTP 请求/分钟 — 超出 429
//   2. 按 IP 的 WSS 握手/分钟  — 超出拒绝 upgrade
//   3. 按访客 session 的消息/分钟 — 超出临时静音
//   4. 单 IP 24h 内被拦截累计达到阈值 → 拉黑 24h（自动解封）
//
// 所有计数走 Redis（带 TTL），多副本部署可天然共享。

type RateLimiter struct {
	rdb       *redis.Client
	secLog    *zap.Logger
	blacklist int // 24h 内被拦次数阈值
}

func NewRateLimiter(rdb *redis.Client, secLog *zap.Logger, blacklistThreshold int) *RateLimiter {
	return &RateLimiter{rdb: rdb, secLog: secLog, blacklist: blacklistThreshold}
}

// ClientIP 取真实 IP。Nginx 已经设置 X-Forwarded-For / X-Real-IP，
// 我们信第一个非内网的地址，避免代理链伪造。
func ClientIP(c *gin.Context) string {
	// 1) X-Real-IP
	if ip := strings.TrimSpace(c.GetHeader("X-Real-IP")); ip != "" {
		return ip
	}
	// 2) X-Forwarded-For 取最左边非空
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		for _, p := range strings.Split(xff, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				return p
			}
		}
	}
	// 3) RemoteAddr
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}

// HTTPMiddleware：按 IP 限流；超出/被拉黑直接 429。
func (r *RateLimiter) HTTPMiddleware(rpm int) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := ClientIP(c)
		ctx := c.Request.Context()
		if r.isBlacklisted(ctx, ip) {
			r.secLog.Warn("blacklisted ip blocked",
				zap.String("ip", ip), zap.String("path", c.Request.URL.Path))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code": 42901, "msg": "您的 IP 已被临时限制访问，请稍后再试",
			})
			return
		}
		allowed, err := r.allow(ctx, "rl:http:"+ip, rpm, time.Minute)
		if err != nil {
			// Redis 异常时 fail-open，但记到安全日志
			r.secLog.Error("ratelimit redis err", zap.Error(err), zap.String("ip", ip))
			c.Next()
			return
		}
		if !allowed {
			r.recordViolation(ctx, ip, "http_rpm_exceeded", c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code": 42902, "msg": "请求过于频繁，请稍后再试",
			})
			return
		}
		c.Next()
	}
}

// AllowWSHandshake 检查单 IP 的 WSS 握手频率。
func (r *RateLimiter) AllowWSHandshake(ctx context.Context, ip string, pm int) (bool, error) {
	if r.isBlacklisted(ctx, ip) {
		return false, nil
	}
	return r.allow(ctx, "rl:wsh:"+ip, pm, time.Minute)
}

// AllowVisitorMessage 检查单访客消息频率。
// 约定：pm <= 0 表示「不限制」（用户可在 .env 把 SECURITY_VISITOR_MSG_PM 设为 0 关闭此限流）。
func (r *RateLimiter) AllowVisitorMessage(ctx context.Context, visitorID string, pm int) (bool, error) {
	if pm <= 0 {
		return true, nil
	}
	return r.allow(ctx, "rl:vmsg:"+visitorID, pm, time.Minute)
}

// 通用计数：Redis INCR + EXPIRE
func (r *RateLimiter) allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	pipe := r.rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return incr.Val() <= int64(limit), nil
}

// 拉黑
func (r *RateLimiter) isBlacklisted(ctx context.Context, ip string) bool {
	v, err := r.rdb.Get(ctx, "bl:"+ip).Result()
	if err != nil {
		return false
	}
	return v == "1"
}

func (r *RateLimiter) recordViolation(ctx context.Context, ip, kind, path string) {
	key := "viol:" + ip
	pipe := r.rdb.TxPipeline()
	v := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 24*time.Hour)
	_, _ = pipe.Exec(ctx)

	r.secLog.Warn("security violation",
		zap.String("ip", ip),
		zap.String("kind", kind),
		zap.String("path", path),
		zap.Int64("count_24h", v.Val()))

	if v.Val() >= int64(r.blacklist) {
		if err := r.rdb.Set(ctx, "bl:"+ip, "1", 24*time.Hour).Err(); err == nil {
			r.secLog.Error("ip auto-blacklisted",
				zap.String("ip", ip),
				zap.Int64("violations", v.Val()),
				zap.String("ttl", "24h"))
		}
	}
}

// RecordViolation 暴露给外部主动上报（如 SQL 注入嫌疑、XSS 嫌疑）。
func (r *RateLimiter) RecordViolation(ctx context.Context, ip, kind, detail string) {
	r.recordViolation(ctx, ip, kind, detail)
}

