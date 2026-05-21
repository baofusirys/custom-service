package security

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWT 仅用于「客服 / 管理员」会话（访客用一次性 token，下面 IssueVisitorToken）。

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

func ParseAgentToken(secret []byte, tokenStr string) (*AgentClaims, error) {
	c := &AgentClaims{}
	t, err := jwt.ParseWithClaims(tokenStr, c, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "HS256" {
			return nil, errors.New("unexpected jwt alg")
		}
		return secret, nil
	})
	if err != nil || !t.Valid {
		return nil, errors.New("invalid agent token")
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
