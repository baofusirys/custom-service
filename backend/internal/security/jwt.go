package security

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWT 仅用于「客服 / 管理员」会话（访客用一次性 token，下面 IssueVisitorToken）。

// [064] 区分 token 失败的几种错误：
//   - ErrTokenExpired：签名有效但已过期（客户端可调 /agent/login/refresh 续）
//   - ErrTokenInvalid：签名错 / 篡改 / sub 字段不对（客户端必须重新登录）
//   - ErrTokenMalformed：完全不是合法 JWT（同上）
// handler 用 errors.Is 区分后给前端不同 code，让 App 走 refresh 而不是登录页。
var (
	ErrTokenExpired   = errors.New("token expired")
	ErrTokenInvalid   = errors.New("token invalid")
	ErrTokenMalformed = errors.New("token malformed")
)

type AgentClaims struct {
	AgentID  int64  `json:"aid"`
	Username string `json:"u"`
	Role     string `json:"r"` // admin | agent
	jwt.RegisteredClaims
}

type VisitorClaims struct {
	VisitorID string `json:"vid"`
	SiteID    string `json:"sid"`
	jwt.RegisteredClaims
}

func IssueAgentToken(secret []byte, agentID int64, username, role string, ttl time.Duration) (string, error) {
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, AgentClaims{
		AgentID:  agentID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Subject:   "agent",
		},
	})
	return tok.SignedString(secret)
}

// ParseAgentToken [064] 改为区分错误类型：
//   - 签名 OK 但已过期 → ErrTokenExpired（claims 仍返回，便于 refresh 校验 grace period）
//   - 签名错 / 算法错 / sub 不对 → ErrTokenInvalid
//   - 完全不是 JWT → ErrTokenMalformed
//
// 兼容老调用方：错误类型已变，但 nil/non-nil 行为不变。
func ParseAgentToken(secret []byte, tokenStr string) (*AgentClaims, error) {
	c := &AgentClaims{}
	t, err := jwt.ParseWithClaims(tokenStr, c, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "HS256" {
			return nil, errors.New("unexpected jwt alg")
		}
		return secret, nil
	})
	if err != nil {
		// jwt/v5 lib 暴露 jwt.ErrTokenExpired 等错误类型
		if errors.Is(err, jwt.ErrTokenExpired) {
			// 即使过期，claims 也已填好（lib 这种情况会返回 claims），返给 caller 用
			return c, ErrTokenExpired
		}
		if errors.Is(err, jwt.ErrTokenMalformed) {
			return nil, ErrTokenMalformed
		}
		return nil, ErrTokenInvalid
	}
	if !t.Valid {
		return nil, ErrTokenInvalid
	}
	if c.Subject != "agent" {
		return nil, ErrTokenInvalid
	}
	return c, nil
}

// ParseAgentTokenAllowExpired [064] 给 /agent/login/refresh 用：
// 允许 token 已过期，但签名 / 算法 / subject 必须 valid。
// 调用方拿到 claims 后自己判断「过期是否在 grace period（默认 24h）内」决定能否 refresh。
func ParseAgentTokenAllowExpired(secret []byte, tokenStr string) (*AgentClaims, error) {
	c := &AgentClaims{}
	_, err := jwt.ParseWithClaims(tokenStr, c, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "HS256" {
			return nil, errors.New("unexpected jwt alg")
		}
		return secret, nil
	}, jwt.WithoutClaimsValidation()) // ← 关键：跳过 exp 校验
	if err != nil {
		// 注：WithoutClaimsValidation 之后通常只会因签名错而失败
		if errors.Is(err, jwt.ErrTokenMalformed) {
			return nil, ErrTokenMalformed
		}
		return nil, ErrTokenInvalid
	}
	if c.Subject != "agent" {
		return nil, ErrTokenInvalid
	}
	return c, nil
}

func IssueVisitorToken(secret []byte, visitorID, siteID string, ttl time.Duration) (string, error) {
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, VisitorClaims{
		VisitorID: visitorID,
		SiteID:    siteID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Subject:   "visitor",
		},
	})
	return tok.SignedString(secret)
}

func ParseVisitorToken(secret []byte, tokenStr string) (*VisitorClaims, error) {
	c := &VisitorClaims{}
	t, err := jwt.ParseWithClaims(tokenStr, c, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "HS256" {
			return nil, errors.New("unexpected jwt alg")
		}
		return secret, nil
	})
	if err != nil || !t.Valid {
		return nil, errors.New("invalid visitor token")
	}
	return c, nil
}
