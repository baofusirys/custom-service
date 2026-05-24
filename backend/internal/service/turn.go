package service

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// TURN 短期凭证生成 —— 严格遵循 draft-uberti-behave-turn-rest-00：
//
//	timestamp = unix() + ttl
//	username  = "<timestamp>:<userid>"
//	password  = base64(HMAC-SHA1(static-auth-secret, username))
//
// CoTURN 收到 client 上报 <username, password> 后：
//  1. 解析 username 里的 timestamp，验证未过期
//  2. 用 static-auth-secret 反向 HMAC username，比对 password 是否一致
//
// 跟 turnserver.conf 里的 `use-auth-secret + static-auth-secret=$TURN_STATIC_AUTH_SECRET` 配合。
// 两侧 secret 必须完全一致（通过 docker-compose 的同一个 ${TURN_STATIC_AUTH_SECRET} 环境变量分发）。

// TurnCredential 给前端的完整 TURN 配置（直接喂 RTCPeerConnection iceServers）
type TurnCredential struct {
	Username   string   `json:"username"`
	Credential string   `json:"credential"` // 即 password
	URLs       []string `json:"urls"`       // turn/turns/stun 多协议
	TTL        int      `json:"ttl"`        // 秒
}

// GenerateTurnCredential 生成一对 24h 时效的 TURN 用户名密码。
//
//	userID  访客 ID 或 客服 ID，仅作日志/审计标识，不影响校验（CoTURN 不查 ID）；
//	        匿名场景传 "anon" 也行
//
// realm   TURN realm（CoTURN 服务的域名），通常等于 TURN_REALM env
// secret  与 CoTURN 共享的 HMAC 密钥（TURN_STATIC_AUTH_SECRET env）
//
// 返回的 TurnCredential 直接 json 给前端用。
func GenerateTurnCredential(userID, realm, secret string) *TurnCredential {
	const ttl = 24 * 60 * 60 // 24 小时

	// userID 清洗：可能含冒号（破坏 username 解析），统一替换
	safeID := strings.ReplaceAll(userID, ":", "_")
	if safeID == "" {
		safeID = "anon"
	}

	exp := time.Now().Unix() + ttl
	username := fmt.Sprintf("%d:%s", exp, safeID)

	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write([]byte(username))
	password := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// 三种 URL：客户端会按顺序尝试，UDP 最优 → TCP 兜底 → TLS 穿透严格防火墙
	urls := []string{
		fmt.Sprintf("turn:%s:3478?transport=udp", realm),
		fmt.Sprintf("turn:%s:3478?transport=tcp", realm),
		fmt.Sprintf("turns:%s:5349?transport=tcp", realm),
		fmt.Sprintf("stun:%s:3478", realm),
	}

	return &TurnCredential{
		Username:   username,
		Credential: password,
		URLs:       urls,
		TTL:        ttl,
	}
}
