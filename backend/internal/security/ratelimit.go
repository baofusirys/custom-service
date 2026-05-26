package security

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// 限流 / 安全计数（爷爷铁律 + [062] 调整后剩余防御层）：
//
// [062] 大改：移除按 IP 的所有限流机制（集成方 NAT 后多设备同 IP 误封代价过高）。
// 当前保留的「非 IP 维度」防御：
//   1. 按访客 session 的消息/分钟 — 超出临时静音（不影响其他访客）
//   2. SQL 注入启发式检测（service.go 调 RecordViolation，仅写安全日志）
//   3. 各类登录失败 / MIME 黑名单 / 异常输入（LogSecurityWarn 写日志）
//
// 移除的功能（CHANGELOG [062] 详记）：
//   - HTTPMiddleware（按 IP 的 HTTP RPM 限流）→ 删
//   - AllowWSHandshake（按 IP 的 WSS 握手 PM 限流）→ 删
//   - 自动拉黑（24h 内 viol 累计达阈值 bl:<ip>=1）→ 删
//   - 按 IP 的 violation 计数 → 改为按业务实体（visitor: 前缀）只为审计
//
// 剩余仍然有效的纵深防御（不在本文件）：
//   - Nginx：[062] 移除 limit_req / limit_conn 后仅靠 set_real_ip_from + WAF/SSL
//   - JWT visitor / agent token（有 TTL，过期失效）
//   - bcrypt cost=12（agent 密码 hash ≈ 250ms/次，自然防爆破）
//   - CORS（widget 跨域请求 origin 校验）
//   - AES-GCM 敏感字段加密 + HMAC-SHA256 IP hash 索引
//   - SSL/TLS（HTTPS only）+ acme.sh 自动证书

type RateLimiter struct {
	rdb    *redis.Client
	secLog *zap.Logger
}

// NewRateLimiter [062] 删除 blacklistThreshold 参数（不再自动拉黑）。
func NewRateLimiter(rdb *redis.Client, secLog *zap.Logger) *RateLimiter {
	return &RateLimiter{rdb: rdb, secLog: secLog}
}

// ClientIP 取真实 IP。Nginx 已经设置 X-Forwarded-For / X-Real-IP，
// 我们信第一个非内网的地址，避免代理链伪造。
// [062] 注意：IP 不再用于限流/拉黑，但仍用于审计日志、ip_cipher / ip_hash 落库、
// 「关联访客」面板查同 IP 历史 vid，所以 ClientIP 函数保留并继续被广泛调用。
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

// AllowVisitorMessage 检查单访客消息频率（per-visitor，不是 per-IP）。
// 约定：pm <= 0 表示「不限制」（用户可在 .env 把 SECURITY_VISITOR_MSG_PM 设为 0 关闭此限流）。
// [062] 保留此函数 —— 单访客限流不影响其他访客，跟 IP 维度无关。
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

// RecordViolation 上报真实攻击行为（SQL 注入嫌疑、访客刷消息等）。
// [062] 重大变更：不再触发自动拉黑（24h bl:<ip>=1），改为只写安全日志 + 累计计数。
// 24h 计数 viol:<key> 仍然保留，方便事后审计追溯，但**不再有任何阻断行为**。
// 调用方传入的 key 通常是 "visitor:<vid>" 等业务实体，不再按 IP 拉黑。
func (r *RateLimiter) RecordViolation(ctx context.Context, key, kind, detail string) {
	violKey := "viol:" + key
	pipe := r.rdb.TxPipeline()
	v := pipe.Incr(ctx, violKey)
	pipe.Expire(ctx, violKey, 24*time.Hour)
	_, _ = pipe.Exec(ctx)

	r.secLog.Warn("security violation",
		zap.String("key", key),
		zap.String("kind", kind),
		zap.String("detail", detail),
		zap.Int64("count_24h", v.Val()))
}

// LogSecurityWarn 只写安全日志，不计入 violation 累计（不会拉黑）。
// 适用于"用户失误而非攻击"的场景：密码输错、上传不支持的文件类型等。
// 防爆破靠 bcrypt cost=12（每次 ~250ms），不靠拉黑机制。
func (r *RateLimiter) LogSecurityWarn(ip, kind, detail string) {
	r.secLog.Warn("security warn",
		zap.String("ip", ip),
		zap.String("kind", kind),
		zap.String("detail", detail))
}
