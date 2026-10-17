package hall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis 中的大厅运行态（多副本共享，全部带 TTL）：
//   hall:<room>:slot:spk   ZSET 访客开麦名额，score=租约到期毫秒
//   hall:<room>:slot:cam   ZSET 摄像头名额（含客服），score=租约到期毫秒
//   hall:<room>:online     SET  当前在 LiveKit 房间里的 identity（webhook + 对账维护）
//   hall:<room>:ban:<id>   STRING 移出禁入，TTL=禁入时长
//   hall:<room>:amute:<id> STRING 管理员静音的客服（重新加入大厅时清除）
//   hall:<room>:p:<id>     HASH 参与者服务端档案（vid / agent_id / role / name），TTL 7 小时
//   hall:<room>:lock:<id>  STRING 同一身份开麦/摄像头操作串行锁
//   hall:wh:<event_id>     STRING webhook 事件去重（10 分钟）

type slotKind string

const (
	slotSpeaker slotKind = "spk"
	slotCamera  slotKind = "cam"
)

const (
	participantRecordTTL = 7 * time.Hour
	onlineSetTTL         = 10 * time.Minute
	identityLockTTL      = 8 * time.Second
	webhookDedupTTL      = 10 * time.Minute
)

// acquireScript 原子占用名额：先清理过期租约；已持有则续租；未满则占用；满则拒绝。
// 返回 {结果, 当前占用数}：结果 2=已持有并续租，1=新占用，0=已满。
var acquireScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local lease = tonumber(ARGV[2])
local max = tonumber(ARGV[3])
local keyttl = tonumber(ARGV[5])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
if redis.call('ZSCORE', KEYS[1], ARGV[4]) then
  redis.call('ZADD', KEYS[1], now + lease, ARGV[4])
  redis.call('PEXPIRE', KEYS[1], keyttl)
  return {2, redis.call('ZCARD', KEYS[1])}
end
local n = redis.call('ZCARD', KEYS[1])
if n >= max then
  return {0, n}
end
redis.call('ZADD', KEYS[1], now + lease, ARGV[4])
redis.call('PEXPIRE', KEYS[1], keyttl)
return {1, n + 1}
`)

// membersScript 清理过期租约后返回仍有效的持有者。
var membersScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', tonumber(ARGV[1]))
return redis.call('ZRANGE', KEYS[1], 0, -1)
`)

// unlockScript 只删除自己持有的锁。
var unlockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

type redisState struct {
	rdb   *redis.Client
	room  string
	lease time.Duration
	now   func() time.Time
}

func (r *redisState) key(parts ...string) string {
	k := "hall:" + r.room
	for _, p := range parts {
		k += ":" + p
	}
	return k
}

func (r *redisState) slotKey(kind slotKind) string { return r.key("slot", string(kind)) }

// acquireSlot 占用名额；newly 表示本次新占用（用于失败回滚时不误删已有名额）。
func (r *redisState) acquireSlot(ctx context.Context, kind slotKind, identity string, max int) (ok, newly bool, used int, err error) {
	if max <= 0 {
		n, err := r.slotCount(ctx, kind)
		return false, false, n, err
	}
	now := r.now().UnixMilli()
	res, err := acquireScript.Run(ctx, r.rdb, []string{r.slotKey(kind)},
		now, r.lease.Milliseconds(), max, identity, (r.lease * 3).Milliseconds()).Int64Slice()
	if err != nil {
		return false, false, 0, err
	}
	if len(res) != 2 {
		return false, false, 0, errors.New("hall: unexpected slot script result")
	}
	return res[0] > 0, res[0] == 1, int(res[1]), nil
}

func (r *redisState) releaseSlot(ctx context.Context, kind slotKind, identity string) (bool, error) {
	n, err := r.rdb.ZRem(ctx, r.slotKey(kind), identity).Result()
	return n > 0, err
}

// renewSlot 仅对仍持有者续租（ZADD XX），不会凭空占用。
func (r *redisState) renewSlot(ctx context.Context, kind slotKind, identity string) error {
	until := float64(r.now().Add(r.lease).UnixMilli())
	pipe := r.rdb.TxPipeline()
	pipe.ZAddXX(ctx, r.slotKey(kind), redis.Z{Score: until, Member: identity})
	pipe.PExpire(ctx, r.slotKey(kind), r.lease*3)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *redisState) slotMembers(ctx context.Context, kind slotKind) ([]string, error) {
	return membersScript.Run(ctx, r.rdb, []string{r.slotKey(kind)}, r.now().UnixMilli()).StringSlice()
}

func (r *redisState) slotCount(ctx context.Context, kind slotKind) (int, error) {
	m, err := r.slotMembers(ctx, kind)
	return len(m), err
}

func (r *redisState) holdsSlot(ctx context.Context, kind slotKind, identity string) (bool, error) {
	score, err := r.rdb.ZScore(ctx, r.slotKey(kind), identity).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return int64(score) > r.now().UnixMilli(), nil
}

func (r *redisState) clearSlots(ctx context.Context, kind slotKind) error {
	return r.rdb.Del(ctx, r.slotKey(kind)).Err()
}

// ban 写入移出禁入（覆盖旧值，以最后一次操作为准）。
func (r *redisState) ban(ctx context.Context, identity string, d time.Duration) error {
	return r.rdb.Set(ctx, r.key("ban", identity), "1", d).Err()
}

// banRemaining 剩余禁入时长（0 表示未禁入）。
func (r *redisState) banRemaining(ctx context.Context, identity string) (time.Duration, error) {
	d, err := r.rdb.PTTL(ctx, r.key("ban", identity)).Result()
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, nil
	}
	return d, nil
}

// bannedAmong 批量判断哪些 identity 处于禁入期。
func (r *redisState) bannedAmong(ctx context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	pipe := r.rdb.Pipeline()
	cmds := make([]*redis.IntCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.Exists(ctx, r.key("ban", id))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for i, c := range cmds {
		if c.Val() > 0 {
			out[ids[i]] = true
		}
	}
	return out, nil
}

func (r *redisState) setAgentMuted(ctx context.Context, identity string, muted bool) error {
	if muted {
		return r.rdb.Set(ctx, r.key("amute", identity), "1", participantRecordTTL).Err()
	}
	return r.rdb.Del(ctx, r.key("amute", identity)).Err()
}

func (r *redisState) agentMuted(ctx context.Context, identity string) (bool, error) {
	n, err := r.rdb.Exists(ctx, r.key("amute", identity)).Result()
	return n > 0, err
}

func (r *redisState) addOnline(ctx context.Context, identity string) error {
	pipe := r.rdb.TxPipeline()
	pipe.SAdd(ctx, r.key("online"), identity)
	pipe.Expire(ctx, r.key("online"), onlineSetTTL)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *redisState) removeOnline(ctx context.Context, identity string) error {
	return r.rdb.SRem(ctx, r.key("online"), identity).Err()
}

func (r *redisState) isOnline(ctx context.Context, identity string) (bool, error) {
	return r.rdb.SIsMember(ctx, r.key("online"), identity).Result()
}

func (r *redisState) onlineCount(ctx context.Context) (int, error) {
	n, err := r.rdb.SCard(ctx, r.key("online")).Result()
	return int(n), err
}

func (r *redisState) onlineMembers(ctx context.Context) ([]string, error) {
	return r.rdb.SMembers(ctx, r.key("online")).Result()
}

// replaceOnline 用 LiveKit 的权威列表整体替换在线集合（对账用）。
func (r *redisState) replaceOnline(ctx context.Context, ids []string) error {
	pipe := r.rdb.TxPipeline()
	pipe.Del(ctx, r.key("online"))
	if len(ids) > 0 {
		members := make([]any, len(ids))
		for i, id := range ids {
			members[i] = id
		}
		pipe.SAdd(ctx, r.key("online"), members...)
		pipe.Expire(ctx, r.key("online"), onlineSetTTL)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (r *redisState) clearOnline(ctx context.Context) error {
	return r.rdb.Del(ctx, r.key("online")).Err()
}

// participantRecord 参与者服务端档案（仅服务端使用：客服接口据此返回 vid 映射）。
type participantRecord struct {
	Role    string
	VID     string
	AgentID string
	Name    string
}

func (r *redisState) putParticipant(ctx context.Context, identity string, rec participantRecord) error {
	pipe := r.rdb.TxPipeline()
	k := r.key("p", identity)
	pipe.HSet(ctx, k, "role", rec.Role, "vid", rec.VID, "agent_id", rec.AgentID, "name", rec.Name)
	pipe.Expire(ctx, k, participantRecordTTL)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *redisState) participants(ctx context.Context, ids []string) (map[string]participantRecord, error) {
	out := map[string]participantRecord{}
	if len(ids) == 0 {
		return out, nil
	}
	pipe := r.rdb.Pipeline()
	cmds := make([]*redis.SliceCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.HMGet(ctx, r.key("p", id), "role", "vid", "agent_id", "name")
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for i, c := range cmds {
		vals := c.Val()
		if len(vals) != 4 || vals[0] == nil {
			continue
		}
		str := func(v any) string {
			if s, ok := v.(string); ok {
				return s
			}
			return ""
		}
		out[ids[i]] = participantRecord{Role: str(vals[0]), VID: str(vals[1]), AgentID: str(vals[2]), Name: str(vals[3])}
	}
	return out, nil
}

func (r *redisState) touchParticipants(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	pipe := r.rdb.Pipeline()
	for _, id := range ids {
		pipe.Expire(ctx, r.key("p", id), participantRecordTTL)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// lock 同一身份的媒体操作串行锁；拿不到返回 ok=false。
func (r *redisState) lock(ctx context.Context, identity string) (unlock func(), ok bool, err error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, false, err
	}
	token := hex.EncodeToString(b)
	k := r.key("lock", identity)
	ok, err = r.rdb.SetNX(ctx, k, token, identityLockTTL).Result()
	if err != nil || !ok {
		return nil, false, err
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = unlockScript.Run(ctx, r.rdb, []string{k}, token).Err()
	}, true, nil
}

// markWebhookEvent 记录事件 ID；同一 ID 第二次返回 false（防重放 / LiveKit 重试重复投递）。
func (r *redisState) markWebhookEvent(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return true, nil
	}
	return r.rdb.SetNX(ctx, "hall:wh:"+id, strconv.FormatInt(r.now().Unix(), 10), webhookDedupTTL).Result()
}
