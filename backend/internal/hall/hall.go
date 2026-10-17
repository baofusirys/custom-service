package hall

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/livekit"
	"github.com/custom-service/backend/internal/mailer"
	"github.com/custom-service/backend/internal/security"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

// [119] 多人大厅业务模块。职责：签发 LiveKit 令牌、开麦/摄像头名额（Redis 原子操作 + 租约 + 30 秒对账）、
// 客服管理（静音/全员静音/关摄像头/移出禁入）、大厅消息（WS 收发 + 持久化 + 历史）、LiveKit 回调。
// 并发约束：WS 帧与回调都进入有界工作池，绝不阻塞 Hub 主循环；LiveKit 调用由 livekit.Client 限并发。
// 日志：业务事件 hall_* 写 business，安全事件写 security；不记录消息正文、令牌与密钥。

const (
	tokenTTL          = 6 * time.Hour
	slotLease         = 120 * time.Second
	reconcileInterval = 30 * time.Second
	settingsCacheTTL  = 3 * time.Second
	frameTimeout      = 8 * time.Second
	webhookTimeout    = 10 * time.Second
	maxContentRunes   = 1000
	maxKickMinutes    = 1440
	defaultKickMinute = 30
	maxReconcileCalls = 40
	maxHistory        = 50
)

// Store 大厅依赖的数据层（*store.Store 实现）。
type Store interface {
	InsertHallMessage(ctx context.Context, m *store.HallMessage) (bool, error)
	ListHallMessages(ctx context.Context, room, beforeID string, limit int, viewer store.HallViewer) ([]store.HallMessage, error)
	SoftDeleteHallMessage(ctx context.Context, room, id, by string, at time.Time) (bool, bool, error)
	GetFileByStoreKey(ctx context.Context, key string) (*store.FileRecord, error)
	IsVisitorBlocked(ctx context.Context, visitorID string) (bool, error)
	BlockedVisitorIDs(ctx context.Context, visitorIDs []string) (map[string]bool, error)
	GetSettingsMap(ctx context.Context, keys []string) (map[string]string, error)
	GetAgentByID(ctx context.Context, id int64) (*store.Agent, error)
}

// Media LiveKit 能力（*livekit.Client 实现）。
type Media interface {
	Enabled() bool
	IssueToken(p livekit.ParticipantToken) (string, time.Time, error)
	VerifyWebhook(authHeader string, body []byte) (*livekit.WebhookEvent, error)
	ListParticipants(ctx context.Context, room string) ([]livekit.ParticipantInfo, error)
	UpdatePermission(ctx context.Context, room, identity string, perm livekit.Permission) (*livekit.ParticipantInfo, error)
	MutePublishedTrack(ctx context.Context, room, identity, trackSid string, muted bool) error
	RemoveParticipant(ctx context.Context, room, identity string, revokeBefore time.Time) error
}

// Peer WS 连接的最小视图（*ws.Client 实现）。
type Peer interface {
	IsVisitor() bool
	ActorID() string
	ConnectionID() string
	Send(e *ws.Envelope) bool
}

// Broadcaster 大厅广播能力（NewHubBroadcaster 包装 *ws.Hub）。
type Broadcaster interface {
	Subscribe(p Peer)
	Unsubscribe(p Peer)
	Subscribed(p Peer) bool
	Broadcast(ctx context.Context, e *ws.Envelope)
}

// ICEServer 下发给客户端的 ICE 服务器（契约：{urls, username, credential}）。
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Deps 构造依赖。
type Deps struct {
	Room      string
	Store     Store
	Redis     *redis.Client
	Media     Media
	Hub       Broadcaster
	Limiter   *security.RateLimiter
	DataKey   []byte
	PublicURL string
	// ICE 生成 ICE 服务器（复用 coturn 短期凭证）；label 只用于 TURN 用户名标识（传 identity，不传 vid）。
	ICE      func(label string) []ICEServer
	BizLog   *zap.Logger
	SecLog   *zap.Logger
	AuditLog *zap.Logger
	Now      func() time.Time
	// [122] 敏感词屏蔽与告警邮件
	PublicDomain  string        // 推出默认放行的本站主域
	AlertDefaults AlertDefaults // 告警邮件环境变量默认值
	FilterStore   FilterStore   // 为空时若 Store 实现了 FilterStore 则复用
	SendMail      func(ctx context.Context, c mailer.Config, m mailer.Message) error
}

// Service 大厅服务。
type Service struct {
	room      string
	store     Store
	media     Media
	hub       Broadcaster
	limiter   *security.RateLimiter
	state     *redisState
	ids       *identityDeriver
	publicURL string
	ice       func(string) []ICEServer
	biz       *zap.Logger
	sec       *zap.Logger
	audit     *zap.Logger
	now       func() time.Time

	settings atomic.Pointer[cachedSettings]

	// [122] 敏感词屏蔽
	publicDomain  string
	alertDefaults AlertDefaults
	filterStore   FilterStore
	sendMail      func(ctx context.Context, c mailer.Config, m mailer.Message) error
	smtpCipher    *security.Cipher
	origCipher    *security.Cipher
	filterCache   atomic.Pointer[cachedFilter]
	alertCache    atomic.Pointer[cachedAlert]
	frameSlots    chan struct{} // WS 帧有界工作池
	bgSlots       chan struct{} // 回调与后台强制动作的有界工作池
	bgWG          sync.WaitGroup
}

type cachedSettings struct {
	s   Settings
	exp time.Time
}

// New 创建大厅服务。
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Redis == nil || d.Media == nil || d.Hub == nil {
		return nil, errors.New("hall: missing dependency")
	}
	ids, err := newIdentityDeriver(d.DataKey)
	if err != nil {
		return nil, err
	}
	if d.Room == "" {
		d.Room = store.HallDefaultRoom
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	nop := zap.NewNop()
	if d.BizLog == nil {
		d.BizLog = nop
	}
	if d.SecLog == nil {
		d.SecLog = nop
	}
	if d.AuditLog == nil {
		d.AuditLog = nop
	}
	if d.ICE == nil {
		d.ICE = func(string) []ICEServer { return []ICEServer{} }
	}
	if d.FilterStore == nil {
		if fs, ok := d.Store.(FilterStore); ok {
			d.FilterStore = fs
		}
	}
	if d.SendMail == nil {
		d.SendMail = mailer.Send
	}
	smtpCipher, err := deriveCipher(d.DataKey, smtpKeyInfo)
	if err != nil {
		return nil, err
	}
	origCipher, err := deriveCipher(d.DataKey, originalKeyInfo)
	if err != nil {
		return nil, err
	}
	return &Service{
		room: d.Room, store: d.Store, media: d.Media, hub: d.Hub, limiter: d.Limiter,
		state:      &redisState{rdb: d.Redis, room: d.Room, lease: slotLease, now: d.Now},
		ids:        ids,
		publicURL:  d.PublicURL,
		ice:        d.ICE,
		biz:        d.BizLog.With(zap.String("component", "hall")),
		sec:        d.SecLog.With(zap.String("component", "hall")),
		audit:      d.AuditLog.With(zap.String("component", "hall")),
		now:        d.Now,
		frameSlots: make(chan struct{}, 256),
		bgSlots:    make(chan struct{}, 32),

		publicDomain:  d.PublicDomain,
		alertDefaults: d.AlertDefaults,
		filterStore:   d.FilterStore,
		sendMail:      d.SendMail,
		smtpCipher:    smtpCipher,
		origCipher:    origCipher,
	}, nil
}

// Room 当前房间名。
func (s *Service) Room() string { return s.room }

// Error 业务错误：HTTP 状态 + 业务码 + 中文提示（+ 可选 data）。
type Error struct {
	Status int
	Code   int
	Msg    string
	Data   map[string]any
}

func (e *Error) Error() string { return e.Msg }

func newErr(status, code int, msg string) *Error { return &Error{Status: status, Code: code, Msg: msg} }

var (
	errHallClosed      = newErr(http.StatusForbidden, 40371, "大厅已关闭")
	errMediaForbidden  = newErr(http.StatusForbidden, 40374, "当前无法开麦或开摄像头")
	errNotInHall       = newErr(http.StatusForbidden, 40374, "当前无法开麦或开摄像头：请先进入大厅")
	errVideoDisabled   = newErr(http.StatusForbidden, 40375, "视频未开启")
	errSpeakersFull    = newErr(http.StatusConflict, 40973, "开麦人数已满，请稍后再试")
	errCamerasFull     = newErr(http.StatusConflict, 40976, "摄像头人数已满，请稍后再试")
	errHallFull        = newErr(http.StatusConflict, 40977, "大厅已满，可以先使用私聊客服")
	errTooFrequent     = newErr(http.StatusTooManyRequests, 42910, "操作过于频繁，请稍后再试")
	errBadParam        = newErr(http.StatusBadRequest, 40060, "参数错误")
	errTargetForbidden = newErr(http.StatusForbidden, 40376, "不能对该对象执行此操作")
	errMsgNotFound     = newErr(http.StatusNotFound, 40477, "大厅消息不存在")
	errMediaDown       = newErr(http.StatusServiceUnavailable, 50370, "语音服务暂时不可用，请稍后再试")
	errInternal        = newErr(http.StatusInternalServerError, 50050, "服务器错误，请稍后重试")
)

func withData(e *Error, data map[string]any) *Error {
	c := *e
	c.Data = data
	return &c
}

func withMsg(e *Error, msg string) *Error {
	c := *e
	c.Msg = msg
	return &c
}

// bannedError 已被移出（带剩余秒数）。
func bannedError(remaining time.Duration) *Error {
	secs := int64((remaining + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return withData(newErr(http.StatusForbidden, 40372, "你已被移出大厅，请稍后再试"), map[string]any{"retry_after": secs})
}

// Actor 发起请求的身份（由 handler 从令牌/中间件得出，绝不来自请求体）。
type Actor struct {
	Visitor       bool
	VisitorID     string
	SiteID        string
	AgentID       int64
	AgentUsername string
	Admin         bool
}

// identityOf 计算 identity 与访客显示码。
func (s *Service) identityOf(a Actor) (identity, code string) {
	if a.Visitor {
		return s.ids.visitor(a.VisitorID)
	}
	return agentIdentity(a.AgentID), ""
}

// VisitorIdentity 访客 identity（供 handler / 拉黑联动使用）。
func (s *Service) VisitorIdentity(vid string) string {
	id, _ := s.ids.visitor(vid)
	return id
}

// ============ 设置 ============

// Settings 生效设置（3 秒缓存；未配置 LiveKit 时大厅恒为关闭）。
func (s *Service) Settings(ctx context.Context) Settings {
	if c := s.settings.Load(); c != nil && s.now().Before(c.exp) {
		return c.s
	}
	return s.loadSettings(ctx)
}

func (s *Service) loadSettings(ctx context.Context) Settings {
	m, err := s.store.GetSettingsMap(ctx, SettingKeys)
	if err != nil {
		s.biz.Warn("hall_settings_load_failed", zap.Error(err))
		if c := s.settings.Load(); c != nil {
			return c.s
		}
		st := DefaultSettings
		st.Enabled = false // 读不到设置时保守关闭，避免绕过管理员的关闭决定
		return st
	}
	st := ParseSettings(m)
	if !s.media.Enabled() {
		st.Enabled = false
	}
	s.settings.Store(&cachedSettings{s: st, exp: s.now().Add(settingsCacheTTL)})
	return st
}

// InvalidateSettings 使本节点缓存失效（设置保存后调用）。
func (s *Service) InvalidateSettings() { s.settings.Store(nil) }

// PublicFlags 访客公开设置里的两项。
func (s *Service) PublicFlags(ctx context.Context) (enabled, video bool) {
	st := s.Settings(ctx)
	return st.Enabled, st.Enabled && st.VideoEnabled
}

// ============ 通用 ============

// allow 按身份维度限流（不做 IP 维度：爷爷 [066] 决定去掉全部按 IP 限流）。Redis 故障时拒绝（防限流失效被滥用）。
func (s *Service) allow(ctx context.Context, action string, limit int, window time.Duration, dimension string) *Error {
	if s.limiter == nil {
		return nil
	}
	ok, err := s.limiter.AllowAction(ctx, "hall_"+action, limit, window, dimension)
	if err != nil {
		s.biz.Warn("hall_rate_limiter_unavailable", zap.String("action", action), zap.Error(err))
		return errTooFrequent
	}
	if !ok {
		s.sec.Warn("hall_rate_limited", zap.String("event", "hall_rate_limited"), zap.String("action", action), zap.String("actor", dimension))
		return errTooFrequent
	}
	return nil
}

func actorDimension(a Actor) string {
	if a.Visitor {
		return "visitor:" + a.VisitorID
	}
	return "agent:" + agentIdentity(a.AgentID)
}

// runBackground 有界后台任务（回调处理、关闭大厅/视频时的强制动作）。满载时丢弃并记日志，由 30 秒对账兜底。
func (s *Service) runBackground(event string, timeout time.Duration, task func(ctx context.Context)) bool {
	select {
	case s.bgSlots <- struct{}{}:
	default:
		s.biz.Warn("hall_background_queue_full", zap.String("task", event))
		return false
	}
	s.bgWG.Add(1)
	go func() {
		defer func() {
			<-s.bgSlots
			s.bgWG.Done()
			if r := recover(); r != nil {
				s.biz.Error("hall_background_panic", zap.String("task", event), zap.Any("err", r), zap.Stack("stack"))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		task(ctx)
	}()
	return true
}

// mediaError 把 LiveKit 错误归类为前端可读错误。
func mediaError(err error) *Error {
	switch {
	case errors.Is(err, livekit.ErrNotFound):
		return errNotInHall
	default:
		return errMediaDown
	}
}

// ============ Hub 适配器 ============

type hubBroadcaster struct{ hub *ws.Hub }

// NewHubBroadcaster 把 *ws.Hub 包装成 Broadcaster。
func NewHubBroadcaster(h *ws.Hub) Broadcaster { return &hubBroadcaster{hub: h} }

func (b *hubBroadcaster) Subscribe(p Peer) {
	if c, ok := p.(*ws.Client); ok {
		b.hub.HallSubscribe(c)
	}
}

func (b *hubBroadcaster) Unsubscribe(p Peer) {
	if c, ok := p.(*ws.Client); ok {
		b.hub.HallUnsubscribe(c)
	}
}

func (b *hubBroadcaster) Subscribed(p Peer) bool {
	if c, ok := p.(*ws.Client); ok {
		return b.hub.HallSubscribed(c)
	}
	return false
}

func (b *hubBroadcaster) Broadcast(ctx context.Context, e *ws.Envelope) { b.hub.BroadcastHall(ctx, e) }
