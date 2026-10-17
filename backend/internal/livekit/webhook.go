package livekit

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 回调验签对照 livekit/protocol webhook/verifier.go + auth/verifier.go：
//   - Authorization 头是原始 JWT（不带 Bearer 前缀；这里兼容带前缀）；
//   - 只接受 HS256，iss 必须是本服务 API Key，exp 必填，允许 60 秒时钟偏差；
//   - claim sha256 必须等于 base64(sha256(请求体))，常量时间比较。

// WebhookLeeway 与 livekit/protocol tokenLeeway 一致。
const WebhookLeeway = time.Minute

var (
	ErrWebhookNoAuth   = errors.New("livekit webhook: missing authorization")
	ErrWebhookToken    = errors.New("livekit webhook: invalid token")
	ErrWebhookChecksum = errors.New("livekit webhook: checksum mismatch")
	ErrWebhookPayload  = errors.New("livekit webhook: invalid payload")
)

// WebhookEvent 对照 livekit_webhook.proto WebhookEvent（只取用到的字段）。
type WebhookEvent struct {
	Event       string
	ID          string
	CreatedAt   int64
	RoomName    string
	Participant *ParticipantInfo
	Track       *TrackInfo
}

// VerifyWebhook 校验签名与请求体摘要并解析事件。返回的错误不含任何密钥或令牌内容。
func VerifyWebhook(apiKey, apiSecret, authHeader string, body []byte, now time.Time) (*WebhookEvent, error) {
	if apiKey == "" || apiSecret == "" {
		return nil, ErrKeysMissing
	}
	raw := strings.TrimSpace(authHeader)
	if strings.HasPrefix(raw, "Bearer ") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
	}
	if raw == "" {
		return nil, ErrWebhookNoAuth
	}
	claims := &Claims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(apiKey),
		jwt.WithLeeway(WebhookLeeway),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	tok, err := parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		return []byte(apiSecret), nil
	})
	if err != nil || tok == nil || !tok.Valid {
		return nil, ErrWebhookToken
	}
	sum := sha256.Sum256(body)
	want := base64.StdEncoding.EncodeToString(sum[:])
	if claims.Sha256 == "" || subtle.ConstantTimeCompare([]byte(claims.Sha256), []byte(want)) != 1 {
		return nil, ErrWebhookChecksum
	}
	return ParseWebhookEvent(body)
}

// ParseWebhookEvent 解析（已验签的）事件体，camelCase / snake_case 均可。
func ParseWebhookEvent(body []byte) (*WebhookEvent, error) {
	var o rawObject
	if err := json.Unmarshal(body, &o); err != nil {
		return nil, ErrWebhookPayload
	}
	ev := &WebhookEvent{
		Event:     o.str("event"),
		ID:        o.str("id"),
		CreatedAt: o.int64("createdAt", "created_at"),
	}
	if ev.Event == "" {
		return nil, ErrWebhookPayload
	}
	if room, ok := o.object("room"); ok {
		ev.RoomName = room.str("name")
	}
	if p, ok := o.object("participant"); ok {
		info := decodeParticipant(p)
		ev.Participant = &info
	}
	if t, ok := o.object("track"); ok {
		info := decodeTrack(t)
		ev.Track = &info
	}
	return ev, nil
}

// SignWebhook 按 LiveKit 的方式为请求体签名（供测试与本地联调构造合法回调；生产中由 cs-livekit 签名）。
func SignWebhook(apiKey, apiSecret string, body []byte, now time.Time, ttl time.Duration) (string, error) {
	if apiKey == "" || apiSecret == "" {
		return "", ErrKeysMissing
	}
	sum := sha256.Sum256(body)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    apiKey,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		Sha256: base64.StdEncoding.EncodeToString(sum[:]),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(apiSecret))
}
