package livekit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Twirp JSON 服务端 API：POST {InternalURL}/twirp/livekit.RoomService/{Method}
// 请求/响应字段对照 protobufs/livekit_room.proto：
//   ListParticipants(ListParticipantsRequest{room}) → ListParticipantsResponse{participants}
//   UpdateParticipant(UpdateParticipantRequest{room, identity, permission}) → ParticipantInfo
//   MutePublishedTrack(MuteRoomTrackRequest{room, identity, track_sid, muted}) → MuteRoomTrackResponse{track}
//   RemoveParticipant(RoomParticipantIdentity{room, identity, revoke_token_ts}) → RemoveParticipantResponse{}
// 鉴权：Authorization: Bearer <roomAdmin + room 的短期令牌>。

const (
	roomServicePrefix = "/twirp/livekit.RoomService/"
	// DefaultTimeout 单次调用超时（含排队等待并发槽）。
	DefaultTimeout = 5 * time.Second
	// DefaultConcurrency 全进程同时进行的服务端 API 调用上限（保护 LiveKit 与本机 CPU）。
	DefaultConcurrency = 8
	maxResponseBytes   = 4 << 20
)

// 错误分类：调用方用 errors.Is 判断，向前端返回明确中文错误而不是 500 堆栈。
var (
	ErrDisabled     = errors.New("livekit: not configured")
	ErrUnavailable  = errors.New("livekit: unavailable")
	ErrNotFound     = errors.New("livekit: not found")
	ErrUnauthorized = errors.New("livekit: unauthorized")
	ErrBadRequest   = errors.New("livekit: bad request")
)

// APIError 带上下文的调用错误（不含令牌、不含请求体）。
type APIError struct {
	Method string
	Status int
	Code   string
	Kind   error
	cause  error
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("livekit %s failed: status=%d code=%s kind=%v", e.Method, e.Status, e.Code, e.Kind)
	if e.cause != nil {
		msg += ": " + e.cause.Error()
	}
	return msg
}

func (e *APIError) Unwrap() error { return e.Kind }

// Config 客户端配置。
type Config struct {
	APIKey      string
	APISecret   string
	InternalURL string
	Timeout     time.Duration
	Concurrency int
}

// Client LiveKit 服务端 API 客户端：连接复用、单次超时、并发上限、错误分类与结构化日志。
type Client struct {
	cfg  Config
	http *http.Client
	sem  chan struct{}
	log  *zap.Logger
	now  func() time.Time
}

// NewClient 创建客户端；APIKey 为空时所有调用返回 ErrDisabled。
func NewClient(cfg Config, log *zap.Logger) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	cfg.InternalURL = strings.TrimRight(cfg.InternalURL, "/")
	if log == nil {
		log = zap.NewNop()
	}
	transport := &http.Transport{
		Proxy:                 nil, // 内网直连，不走任何代理环境变量
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   cfg.Concurrency,
		MaxConnsPerHost:       cfg.Concurrency * 2,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
		ExpectContinueTimeout: time.Second,
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Transport: transport, Timeout: cfg.Timeout},
		sem:  make(chan struct{}, cfg.Concurrency),
		log:  log,
		now:  time.Now,
	}
}

// Enabled 是否已配置。
func (c *Client) Enabled() bool { return c != nil && c.cfg.APIKey != "" && c.cfg.APISecret != "" }

// APIKey 返回 Key（非机密，供签发令牌与验签使用）。
func (c *Client) APIKey() string { return c.cfg.APIKey }

// IssueToken 签发参与者令牌。
func (c *Client) IssueToken(p ParticipantToken) (string, time.Time, error) {
	if !c.Enabled() {
		return "", time.Time{}, ErrDisabled
	}
	return IssueParticipantToken(c.cfg.APIKey, c.cfg.APISecret, p, c.now())
}

// VerifyWebhook 校验并解析回调。
func (c *Client) VerifyWebhook(authHeader string, body []byte) (*WebhookEvent, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	return VerifyWebhook(c.cfg.APIKey, c.cfg.APISecret, authHeader, body, c.now())
}

// ListParticipants 列出房间参与者；房间不存在视为空列表。
func (c *Client) ListParticipants(ctx context.Context, room string) ([]ParticipantInfo, error) {
	var resp struct {
		Participants []ParticipantInfo `json:"participants"`
	}
	err := c.call(ctx, "ListParticipants", room, map[string]any{"room": room}, &resp)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return resp.Participants, nil
}

// UpdatePermission 用完整权限覆盖参与者权限（LiveKit 会立即移除不再允许来源的已发布轨道）。
func (c *Client) UpdatePermission(ctx context.Context, room, identity string, perm Permission) (*ParticipantInfo, error) {
	var resp ParticipantInfo
	body := map[string]any{"room": room, "identity": identity, "permission": perm.request()}
	if err := c.call(ctx, "UpdateParticipant", room, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// MutePublishedTrack 静音/取消静音指定轨道。
func (c *Client) MutePublishedTrack(ctx context.Context, room, identity, trackSid string, muted bool) error {
	body := map[string]any{"room": room, "identity": identity, "track_sid": trackSid, "muted": muted}
	return c.call(ctx, "MutePublishedTrack", room, body, nil)
}

// RemoveParticipant 移出参与者，并让 revokeBefore 之前签发（nbf 更早）的该身份令牌失效。
func (c *Client) RemoveParticipant(ctx context.Context, room, identity string, revokeBefore time.Time) error {
	body := map[string]any{"room": room, "identity": identity}
	if !revokeBefore.IsZero() {
		// protojson 的 int64 用字符串表示
		body["revoke_token_ts"] = fmt.Sprintf("%d", revokeBefore.Unix())
	}
	return c.call(ctx, "RemoveParticipant", room, body, nil)
}

func (c *Client) call(ctx context.Context, method, room string, reqBody any, out any) (err error) {
	if !c.Enabled() {
		return ErrDisabled
	}
	start := c.now()
	status := 0
	code := ""
	defer func() {
		fields := []zap.Field{
			zap.String("event", "hall_livekit_api"), zap.String("method", method), zap.String("room", room),
			zap.Int("status", status), zap.String("twirp_code", code), zap.Int64("latency_ms", time.Since(start).Milliseconds()),
		}
		if err != nil {
			c.log.Warn("LiveKit server API call failed", append(fields, zap.Error(err))...)
		} else {
			c.log.Debug("LiveKit server API call completed", fields...)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return &APIError{Method: method, Kind: ErrUnavailable, cause: errors.New("concurrency slot wait timeout")}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return &APIError{Method: method, Kind: ErrBadRequest, cause: err}
	}
	token, err := issueAdminToken(c.cfg.APIKey, c.cfg.APISecret, room, c.now())
	if err != nil {
		return &APIError{Method: method, Kind: ErrUnauthorized, cause: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.InternalURL+roomServicePrefix+method, bytes.NewReader(payload))
	if err != nil {
		return &APIError{Method: method, Kind: ErrBadRequest, cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return &APIError{Method: method, Kind: ErrUnavailable, cause: sanitizeTransportErr(err)}
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if readErr != nil {
		return &APIError{Method: method, Status: status, Kind: ErrUnavailable, cause: readErr}
	}
	if status != http.StatusOK {
		var te struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &te)
		code = te.Code
		return &APIError{Method: method, Status: status, Code: code, Kind: classify(status, code)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &APIError{Method: method, Status: status, Kind: ErrUnavailable, cause: fmt.Errorf("decode response: %w", err)}
	}
	return nil
}

// classify 按 Twirp 错误码（优先）或 HTTP 状态归类。
func classify(status int, code string) error {
	switch code {
	case "not_found":
		return ErrNotFound
	case "unauthenticated", "permission_denied":
		return ErrUnauthorized
	case "invalid_argument", "malformed", "out_of_range", "failed_precondition", "already_exists":
		return ErrBadRequest
	case "unavailable", "deadline_exceeded", "internal", "unknown", "resource_exhausted", "canceled", "aborted":
		return ErrUnavailable
	}
	switch {
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrUnauthorized
	case status >= 400 && status < 500:
		return ErrBadRequest
	default:
		return ErrUnavailable
	}
}

// sanitizeTransportErr 网络错误只保留类别，不回显 URL 以外的请求内容（url.Error 自带的 URL 不含令牌）。
func sanitizeTransportErr(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return errors.New("timeout")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("timeout")
	}
	return err
}
