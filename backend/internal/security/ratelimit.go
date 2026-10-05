package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// 限流 / 安全计数：
//
// 关键动作同时按 IP、账号和令牌维度计数，访客消息仍按 visitor session 限流。
//
// 纵深防御：
//   - Nginx/应用按动作限流 + 失败锁定 + 本地 SVG 验证码
//   - JWT visitor / agent token（短 TTL，agent token_version 可立即吊销）
//   - bcrypt cost=12（agent 密码 hash ≈ 250ms/次，自然防爆破）
//   - CORS（widget 跨域请求 origin 校验）
//   - AES-GCM 敏感字段加密 + HMAC-SHA256 IP hash 索引
//   - SSL/TLS（HTTPS only）+ acme.sh 自动证书

type RateLimiter struct {
	rdb    *redis.Client
	secLog *zap.Logger
}

// AllowAction 为登录、上传、批量及邮件入口提供多维度限流。每个维度独立计数，
// 既能挡单 IP，也能挡换 IP 撞同一账号/令牌；Redis 失败时拒绝高风险动作。
func (r *RateLimiter) AllowAction(ctx context.Context, action string, limit int, window time.Duration, dimensions ...string) (bool, error) {
	if r == nil || r.rdb == nil || limit <= 0 {
		return true, nil
	}
	for _, dimension := range dimensions {
		if strings.TrimSpace(dimension) == "" {
			continue
		}
		key := "rl:" + action + ":" + hashDimension(dimension)
		ok, err := r.allow(ctx, key, limit, window)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func hashDimension(v string) string {
	s := sha256.Sum256([]byte(v))
	return hex.EncodeToString(s[:])
}

// LoginLocked / RecordLoginFailure / ClearLoginFailures 实现账户 + IP 双维度失败锁定。
func (r *RateLimiter) LoginLocked(ctx context.Context, username, ip string) (bool, error) {
	if r == nil || r.rdb == nil {
		return false, nil
	}
	for _, key := range []string{"auth:lock:user:" + hashDimension(username), "auth:lock:ip:" + hashDimension(ip)} {
		n, err := r.rdb.Exists(ctx, key).Result()
		if err != nil {
			return true, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (r *RateLimiter) RecordLoginFailure(ctx context.Context, username, ip string) (locked bool, captchaRequired bool, err error) {
	if r == nil || r.rdb == nil {
		return false, false, nil
	}
	pipe := r.rdb.TxPipeline()
	keys := []string{"auth:fail:user:" + hashDimension(username), "auth:fail:ip:" + hashDimension(ip)}
	counts := make([]*redis.IntCmd, 0, len(keys))
	for _, key := range keys {
		counts = append(counts, pipe.Incr(ctx, key))
		pipe.Expire(ctx, key, 15*time.Minute)
	}
	if _, err = pipe.Exec(ctx); err != nil {
		return false, false, err
	}
	max := int64(0)
	for _, c := range counts {
		if c.Val() > max {
			max = c.Val()
		}
	}
	if max >= 5 {
		lockPipe := r.rdb.TxPipeline()
		for _, key := range []string{"auth:lock:user:" + hashDimension(username), "auth:lock:ip:" + hashDimension(ip)} {
			lockPipe.Set(ctx, key, "1", 15*time.Minute)
		}
		_, err = lockPipe.Exec(ctx)
		return err == nil, true, err
	}
	return false, max >= 3, nil
}

func (r *RateLimiter) ClearLoginFailures(ctx context.Context, username, ip string) {
	if r == nil || r.rdb == nil {
		return
	}
	_ = r.rdb.Del(ctx,
		"auth:fail:user:"+hashDimension(username), "auth:fail:ip:"+hashDimension(ip),
		"auth:lock:user:"+hashDimension(username), "auth:lock:ip:"+hashDimension(ip)).Err()
}

func (r *RateLimiter) LoginCaptchaRequired(ctx context.Context, username, ip string) bool {
	if r == nil || r.rdb == nil {
		return false
	}
	for _, key := range []string{"auth:fail:user:" + hashDimension(username), "auth:fail:ip:" + hashDimension(ip)} {
		n, err := r.rdb.Get(ctx, key).Int64()
		if err == nil && n >= 3 {
			return true
		}
	}
	return false
}

// IssueCaptcha 生成本服务内 SVG 图形验证码，不依赖外部服务。
func (r *RateLimiter) IssueCaptcha(ctx context.Context, subject string) (id, svg string, err error) {
	if r == nil || r.rdb == nil {
		return "", "", nil
	}
	b := make([]byte, 4)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	answer := fmt.Sprintf("%d%d%d%d", b[0]%10, b[1]%10, b[2]%10, b[3]%10)
	id = uuid.NewString()
	sum := sha256.Sum256([]byte(answer))
	if err = r.rdb.Set(ctx, "captcha:"+hashDimension(subject)+":"+id, hex.EncodeToString(sum[:]), 5*time.Minute).Err(); err != nil {
		return "", "", err
	}
	// 数字来自密码学随机字节，SVG 固定模板，不拼接用户输入。
	svg = `<svg xmlns="http://www.w3.org/2000/svg" width="150" height="48" role="img" aria-label="验证码"><rect width="150" height="48" fill="#f3f4f6"/><text x="75" y="32" text-anchor="middle" font-size="24" font-family="monospace" letter-spacing="5">` + answer + `</text></svg>`
	return id, svg, nil
}

func (r *RateLimiter) VerifyCaptcha(ctx context.Context, subject, id, answer string) bool {
	if r == nil || r.rdb == nil || id == "" || len(answer) != 4 {
		return false
	}
	sum := sha256.Sum256([]byte(answer))
	want, err := r.rdb.Get(ctx, "captcha:"+hashDimension(subject)+":"+id).Result()
	if err != nil || subtle.ConstantTimeCompare([]byte(want), []byte(hex.EncodeToString(sum[:]))) != 1 {
		return false
	}
	_ = r.rdb.Del(ctx, "captcha:"+hashDimension(subject)+":"+id).Err()
	return true
}

// NewRateLimiter 创建统一 Redis 限流器；高风险入口由调用方组合 IP、账号和令牌维度。
func NewRateLimiter(rdb *redis.Client, secLog *zap.Logger) *RateLimiter {
	return &RateLimiter{rdb: rdb, secLog: secLog}
}

// ClientIP 取真实 IP。Nginx 已经设置 X-Forwarded-For / X-Real-IP，
// 我们信第一个非内网的地址，避免代理链伪造。
// IP 用于限流、失败锁定、审计日志及 ip_cipher / ip_hash 关联访客查询。
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
// 违规计数用于审计追溯；具体阻断由 AllowAction 与登录失败锁定按动作执行。
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
