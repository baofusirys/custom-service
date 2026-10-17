package hall

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// 身份与隐私（契约第 2 节）：访客 identity 不能泄露 vid（vid 可恢复访客会话，等同凭证）。
//   子密钥 = HKDF-SHA256(DATA_AES_KEY, salt=空, info=identityInfo, 32 字节)，与其它用途的密钥隔离；
//   mac    = HMAC-SHA256(子密钥, vid)；identity = "v_" + hex(mac)[:20]；code = 大端 uint32(mac[16:20]) % 10000。

const identityInfo = "custom-service/hall/identity/v1"

var (
	visitorIdentityPattern = regexp.MustCompile(`^v_[0-9a-f]{20}$`)
	agentIdentityPattern   = regexp.MustCompile(`^a_[0-9]{1,19}$`)
)

type identityDeriver struct {
	key []byte
}

func newIdentityDeriver(dataKey []byte) (*identityDeriver, error) {
	if len(dataKey) < 32 {
		return nil, errors.New("hall: DATA_AES_KEY 长度不足，无法派生身份子密钥")
	}
	key, err := hkdf.Key(sha256.New, dataKey, nil, identityInfo, 32)
	if err != nil {
		return nil, err
	}
	return &identityDeriver{key: key}, nil
}

// visitor 返回访客的 identity 与 4 位显示码（同一 vid 永远相同）。
func (d *identityDeriver) visitor(vid string) (identity, code string) {
	mac := hmac.New(sha256.New, d.key)
	mac.Write([]byte(vid))
	sum := mac.Sum(nil)
	identity = "v_" + hex.EncodeToString(sum)[:20]
	code = fmt.Sprintf("%04d", binary.BigEndian.Uint32(sum[16:20])%10000)
	return identity, code
}

// agentIdentity 客服 identity。
func agentIdentity(agentID int64) string { return "a_" + strconv.FormatInt(agentID, 10) }

// ValidIdentity 是否本系统签发的合法 identity。
func ValidIdentity(s string) bool {
	return visitorIdentityPattern.MatchString(s) || agentIdentityPattern.MatchString(s)
}

func isVisitorIdentity(s string) bool { return visitorIdentityPattern.MatchString(s) }

func isAgentIdentity(s string) bool { return agentIdentityPattern.MatchString(s) }

// agentIDFromIdentity 从客服 identity 取账号 ID。
func agentIDFromIdentity(s string) (int64, bool) {
	if !isAgentIdentity(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s[2:], 10, 64)
	return n, err == nil && n > 0
}
