package livekit

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 本包是 LiveKit 的最小服务端客户端（不引入官方 Go SDK，避免其庞大的依赖树）：
//   - 令牌：HS256 JWT，claim 结构对照 livekit/protocol auth/grants.go（ClaimGrants / VideoGrant 的 JSON 字段名）；
//   - 服务端 API：Twirp JSON（POST {InternalURL}/twirp/livekit.RoomService/<Method>），见 client.go；
//   - 回调验签：对照 livekit/protocol webhook/verifier.go（Authorization 头为原始 JWT，claim sha256=base64(sha256(body)))。
//
// 安全约定：API Secret 只用于签名/验签，任何日志、错误信息都不得包含 Secret 与签发出的令牌。

// 轨道来源（Twirp JSON 里的 TrackSource 枚举名，对照 protobufs/livekit_models.proto）。
const (
	SourceCamera     = "CAMERA"
	SourceMicrophone = "MICROPHONE"
)

// 令牌中的 canPublishSources 使用小写字符串（对照 grants.go sourceToString = strings.ToLower(source.String())）。
const (
	GrantSourceCamera     = "camera"
	GrantSourceMicrophone = "microphone"
)

// ErrKeysMissing 未配置 API Key/Secret。
var ErrKeysMissing = errors.New("livekit: api key/secret missing")

// VideoGrant 对照 livekit/protocol auth/grants.go 的 VideoGrant（只保留本系统用到的字段）。
// 注意：LiveKit 规定「一个发布/订阅权限都没显式设置」时默认全部授予，所以这里的布尔权限一律显式写出。
type VideoGrant struct {
	RoomCreate           bool     `json:"roomCreate,omitempty"`
	RoomList             bool     `json:"roomList,omitempty"`
	RoomAdmin            bool     `json:"roomAdmin,omitempty"`
	RoomJoin             bool     `json:"roomJoin,omitempty"`
	Room                 string   `json:"room,omitempty"`
	CanPublish           *bool    `json:"canPublish,omitempty"`
	CanSubscribe         *bool    `json:"canSubscribe,omitempty"`
	CanPublishData       *bool    `json:"canPublishData,omitempty"`
	CanPublishSources    []string `json:"canPublishSources,omitempty"`
	CanUpdateOwnMetadata *bool    `json:"canUpdateOwnMetadata,omitempty"`
	Hidden               bool     `json:"hidden,omitempty"`
}

// Claims 对照 livekit/protocol auth/accesstoken.go 的 tokenClaims（RegisteredClaims + ClaimGrants）。
// identity 不单独写：LiveKit 验签时以 sub 作为 identity。
type Claims struct {
	jwt.RegisteredClaims
	Name     string      `json:"name,omitempty"`
	Video    *VideoGrant `json:"video,omitempty"`
	Metadata string      `json:"metadata,omitempty"`
	Sha256   string      `json:"sha256,omitempty"`
}

// ParticipantToken 签发参与者令牌所需参数。
type ParticipantToken struct {
	Room     string
	Identity string
	Name     string
	Metadata string
	TTL      time.Duration
	// PublishSources 允许发布的来源（GrantSource* 小写值）。为空表示不可发布：canPublish=false。
	// 不为空时 canPublish=true 且只允许列出的来源（canPublishSources 优先于 canPublish）。
	PublishSources []string
}

func boolPtr(v bool) *bool { return &v }

// IssueParticipantToken 签发客户端加入房间用的令牌：可订阅、不可发 data、不可改自身 metadata、无任何房间管理权限。
func IssueParticipantToken(apiKey, apiSecret string, p ParticipantToken, now time.Time) (string, time.Time, error) {
	if apiKey == "" || apiSecret == "" {
		return "", time.Time{}, ErrKeysMissing
	}
	if p.Room == "" || p.Identity == "" || p.TTL <= 0 {
		return "", time.Time{}, errors.New("livekit: room/identity/ttl required")
	}
	grant := &VideoGrant{
		RoomJoin:             true,
		Room:                 p.Room,
		CanSubscribe:         boolPtr(true),
		CanPublishData:       boolPtr(false),
		CanUpdateOwnMetadata: boolPtr(false),
		CanPublish:           boolPtr(len(p.PublishSources) > 0),
	}
	if len(p.PublishSources) > 0 {
		grant.CanPublishSources = append([]string(nil), p.PublishSources...)
	}
	exp := now.Add(p.TTL)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    apiKey,
			Subject:   p.Identity,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		Name:     p.Name,
		Metadata: p.Metadata,
		Video:    grant,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(apiSecret))
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// issueAdminToken 后端调用服务端 API 用的短期令牌：仅 roomAdmin + 指定房间（ListParticipants/Update/Mute/Remove 都只需要它）。
func issueAdminToken(apiKey, apiSecret, room string, now time.Time) (string, error) {
	if apiKey == "" || apiSecret == "" {
		return "", ErrKeysMissing
	}
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    apiKey,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(adminTokenTTL)),
		},
		Video: &VideoGrant{RoomAdmin: true, Room: room},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(apiSecret))
}

// adminTokenTTL 服务端 API 令牌有效期：只用于单次调用，越短越好。
const adminTokenTTL = 2 * time.Minute
