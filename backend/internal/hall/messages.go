package hall

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/security"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

const minute = time.Minute

var (
	// uuidV4Pattern hall_chat 的消息 ID 必须是 UUID v4（小写/大写均可）。
	uuidV4Pattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	// uuidPattern 历史分页游标（任意版本 UUID）。
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// hallFileURLPattern 本系统上传接口签发的文件 URL：/files/YYYY/MM/DD/<uuid>.<ext>?access=<令牌>
	hallFileURLPattern = regexp.MustCompile(`^/files/([0-9]{4}/[0-9]{2}/[0-9]{2}/[0-9a-f-]{36}\.[a-z0-9]{1,10})\?access=([A-Za-z0-9_-]{20,128})$`)
)

var beijing = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}()

// formatTime 带 +08:00 的 ISO 8601（毫秒）。
func formatTime(t time.Time) string { return t.In(beijing).Format("2006-01-02T15:04:05.000-07:00") }

// MessageDTO 契约第 6 节 HallMessage（sender_ref 永不外泄）。
type MessageDTO struct {
	ID        string `json:"id"`
	Sender    string `json:"sender"`
	Identity  string `json:"identity"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	MediaURL  string `json:"media_url"`
	MediaKind string `json:"media_kind"`
	MediaName string `json:"media_name"`
	MediaSize int64  `json:"media_size"`
	CreatedAt string `json:"created_at"`
}

func toDTO(m store.HallMessage) MessageDTO {
	return MessageDTO{
		ID: m.ID, Sender: m.Sender, Identity: m.Identity, Name: m.Name, Role: m.Sender, Content: m.Content,
		MediaURL: m.MediaURL.String, MediaKind: m.MediaKind.String, MediaName: m.MediaName.String, MediaSize: m.MediaSize.Int64,
		CreatedAt: formatTime(m.CreatedAt),
	}
}

func viewerOf(a Actor) store.HallViewer {
	if a.Visitor {
		return store.HallViewer{Sender: "visitor", SenderRef: a.VisitorID}
	}
	return store.HallViewer{Sender: "agent", SenderRef: itoa64(a.AgentID)}
}

func (s *Service) history(ctx context.Context, a Actor, before string, limit int) ([]MessageDTO, error) {
	rows, err := s.store.ListHallMessages(ctx, s.room, before, limit, viewerOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]MessageDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, toDTO(m))
	}
	return out, nil
}

// Messages 历史分页（正序）。访客需大厅开启且未被移出。
func (s *Service) Messages(ctx context.Context, a Actor, before string, limit int) ([]MessageDTO, error) {
	if e := s.allow(ctx, "messages", 30, minute, actorDimension(a)); e != nil {
		return nil, e
	}
	if before != "" && !uuidPattern.MatchString(before) {
		return nil, withMsg(errBadParam, "before 不合法")
	}
	if limit <= 0 || limit > maxHistory {
		limit = maxHistory
	}
	if a.Visitor {
		if !s.Settings(ctx).Enabled {
			return nil, errHallClosed
		}
		identity, _ := s.identityOf(a)
		if remain, err := s.state.banRemaining(ctx, identity); err != nil {
			return nil, s.internal("hall_messages_ban_check_failed", err)
		} else if remain > 0 {
			return nil, bannedError(remain)
		}
	}
	out, err := s.history(ctx, a, before, limit)
	if err != nil {
		return nil, s.internal("hall_messages_query_failed", err)
	}
	return out, nil
}

// DeleteMessage 客服软删除大厅消息并广播 hall_deleted（幂等：已删除不重复广播）。
func (s *Service) DeleteMessage(ctx context.Context, op Actor, id string) error {
	if op.Visitor || op.AgentID <= 0 {
		return errTargetForbidden
	}
	if e := s.allow(ctx, "delete", 60, minute, actorDimension(op)); e != nil {
		return e
	}
	if !uuidPattern.MatchString(id) {
		return withMsg(errBadParam, "消息标识不合法")
	}
	by := "agent:" + itoa64(op.AgentID)
	found, changed, err := s.store.SoftDeleteHallMessage(ctx, s.room, id, by, s.now())
	if err != nil {
		return s.internal("hall_message_delete_failed", err)
	}
	if !found {
		return errMsgNotFound
	}
	if changed {
		s.hub.Broadcast(ctx, &ws.Envelope{Type: ws.HallTypeDeleted, ID: id, TS: ws.NowMS()})
	}
	s.biz.Info("hall_message_deleted", zap.String("event", "hall_message_deleted"), zap.String("msg_id", id),
		zap.Int64("operator_aid", op.AgentID), zap.Bool("changed", changed))
	return nil
}

// AuthorizeUpload 大厅附件上传前置校验（scope=hall）：大厅开启；访客不在禁入期（被拉黑允许，静默）。
func (s *Service) AuthorizeUpload(ctx context.Context, a Actor) error {
	if !s.Settings(ctx).Enabled {
		return errHallClosed
	}
	identity, _ := s.identityOf(a)
	remain, err := s.state.banRemaining(ctx, identity)
	if err != nil {
		return s.internal("hall_upload_ban_check_failed", err)
	}
	if remain > 0 {
		return bannedError(remain)
	}
	return nil
}

// resolveMedia 校验大厅消息附件：必须是本系统以 scope=hall 签发、上传者就是发送者本人、访问令牌匹配的文件。
// 返回规范化 URL 与服务端权威的 kind/name/size（客户端传的值一律忽略）。
func (s *Service) resolveMedia(ctx context.Context, raw string, uploadBy, uploaderRef string) (url, kind, name string, size int64, ok bool) {
	u := security.SafeMediaURL(raw)
	m := hallFileURLPattern.FindStringSubmatch(u)
	if m == nil {
		return "", "", "", 0, false
	}
	rec, err := s.store.GetFileByStoreKey(ctx, m[1])
	if err != nil || rec == nil {
		return "", "", "", 0, false
	}
	if rec.ConvID != s.room || rec.UploadBy != uploadBy || rec.UploaderRef != uploaderRef {
		return "", "", "", 0, false
	}
	if subtle.ConstantTimeCompare([]byte(security.HashOpaqueAccessToken(m[2])), []byte(rec.AccessTokenHash)) != 1 {
		return "", "", "", 0, false
	}
	kind = "file"
	if strings.HasPrefix(rec.MIME, "image/") && rec.MIME != "image/svg+xml" {
		kind = "image"
	}
	name = security.SanitizeText(rec.Filename)
	if len(name) > 255 {
		name = truncateUTF8(name, 255)
	}
	return u, kind, name, rec.Size, true
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	b := s[:maxBytes]
	for len(b) > 0 && !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}

// ============ WS 帧（hall_subscribe / hall_unsubscribe / hall_chat） ============

// HandleHallFrame 实现 ws.HallHandler：在 Hub 主循环里被调用，只做拷贝与入队，处理放到有界工作池。
func (s *Service) HandleHallFrame(c *ws.Client, e *ws.Envelope) {
	s.handleFrame(c, e)
}

func (s *Service) handleFrame(p Peer, e *ws.Envelope) {
	snap := *e
	select {
	case s.frameSlots <- struct{}{}:
	default:
		s.sec.Warn("hall_frame_queue_full", zap.String("conn", p.ConnectionID()), zap.String("type", snap.Type))
		p.Send(&ws.Envelope{Type: "error", ID: safeID(snap.ID), Content: "服务繁忙，请稍后重试", TS: ws.NowMS()})
		return
	}
	go func() {
		defer func() {
			<-s.frameSlots
			if r := recover(); r != nil {
				s.biz.Error("hall_frame_panic", zap.Any("err", r), zap.String("type", snap.Type), zap.Stack("stack"))
				p.Send(&ws.Envelope{Type: "error", ID: safeID(snap.ID), Content: "消息处理异常，请重试", TS: ws.NowMS()})
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), frameTimeout)
		defer cancel()
		s.processFrame(ctx, p, &snap)
	}()
}

func safeID(id string) string {
	if uuidPattern.MatchString(id) {
		return id
	}
	return ""
}

func frameError(p Peer, id, msg string) {
	p.Send(&ws.Envelope{Type: "error", ID: safeID(id), Content: msg, TS: ws.NowMS()})
}

func actorOfPeer(p Peer) (Actor, bool) {
	if p.IsVisitor() {
		return Actor{Visitor: true, VisitorID: p.ActorID()}, p.ActorID() != ""
	}
	id, err := parseAgentID(p.ActorID())
	if err != nil {
		return Actor{}, false
	}
	return Actor{AgentID: id}, true
}

func (s *Service) processFrame(ctx context.Context, p Peer, e *ws.Envelope) {
	a, ok := actorOfPeer(p)
	if !ok {
		frameError(p, e.ID, "身份无效")
		return
	}
	switch e.Type {
	case "hall_subscribe":
		if !s.Settings(ctx).Enabled {
			frameError(p, e.ID, "大厅已关闭")
			return
		}
		identity, _ := s.identityOf(a)
		remain, err := s.state.banRemaining(ctx, identity)
		if err != nil {
			s.biz.Error("hall_subscribe_ban_check_failed", zap.Error(err))
			frameError(p, e.ID, "服务繁忙，请稍后重试")
			return
		}
		if remain > 0 {
			frameError(p, e.ID, "你已被移出大厅，请稍后再试")
			return
		}
		s.hub.Subscribe(p)
		p.Send(&ws.Envelope{Type: "hall_subscribed", ID: safeID(e.ID), TS: ws.NowMS()})
		s.biz.Info("hall_subscribe", zap.String("event", "hall_subscribe"), zap.String("identity", identity), zap.String("conn", p.ConnectionID()))
	case "hall_unsubscribe":
		s.hub.Unsubscribe(p)
		p.Send(&ws.Envelope{Type: "hall_unsubscribed", ID: safeID(e.ID), TS: ws.NowMS()})
	case "hall_chat":
		s.processChat(ctx, p, a, e)
	}
}

func parseAgentID(s string) (int64, error) {
	if s == "" || len(s) > 19 {
		return 0, errors.New("bad agent id")
	}
	var n int64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("bad agent id")
		}
		n = n*10 + int64(s[i]-'0')
	}
	if n <= 0 {
		return 0, errors.New("bad agent id")
	}
	return n, nil
}

func (s *Service) processChat(ctx context.Context, p Peer, a Actor, e *ws.Envelope) {
	start := s.now()
	if !uuidV4Pattern.MatchString(e.ID) {
		s.sec.Warn("hall_chat_id_rejected", zap.String("conn", p.ConnectionID()), zap.Int("id_length", len(e.ID)))
		frameError(p, "", "消息标识无效，请重试")
		return
	}
	if !s.Settings(ctx).Enabled {
		frameError(p, e.ID, "大厅已关闭")
		return
	}
	if !s.hub.Subscribed(p) {
		frameError(p, e.ID, "请先进入大厅")
		return
	}
	identity, code := s.identityOf(a)
	if remain, err := s.state.banRemaining(ctx, identity); err != nil {
		s.biz.Error("hall_chat_ban_check_failed", zap.Error(err))
		frameError(p, e.ID, "服务繁忙，请稍后重试")
		return
	} else if remain > 0 {
		frameError(p, e.ID, "你已被移出大厅，请稍后再试")
		return
	}
	// 只按身份限流：访客 5 条/10 秒，客服 20 条/10 秒
	limit, action := 5, "chat_visitor"
	if !a.Visitor {
		limit, action = 20, "chat_agent"
	}
	if lim := s.allow(ctx, action, limit, 10*time.Second, actorDimension(a)); lim != nil {
		frameError(p, e.ID, "发送过于频繁，请稍后再试")
		return
	}
	content := e.Content
	if content != "" {
		if suspicious, pattern := security.DetectSQLInjection(content); suspicious && s.limiter != nil {
			s.limiter.RecordViolation(ctx, actorDimension(a), "hall_sqli_suspect", pattern)
		}
		content = security.SanitizeText(content)
	}
	if utf8.RuneCountInString(content) > maxContentRunes {
		frameError(p, e.ID, "消息内容不能超过 1000 字")
		return
	}
	msg := &store.HallMessage{ID: e.ID, Room: s.room, Identity: identity, Content: content, CreatedAt: time.UnixMilli(e.TS)}
	if e.TS <= 0 {
		msg.CreatedAt = s.now()
	}
	silenced := false
	uploadBy := "visitor"
	if a.Visitor {
		msg.Sender, msg.SenderRef, msg.Name = "visitor", a.VisitorID, "访客 "+code
		blocked, err := s.store.IsVisitorBlocked(ctx, a.VisitorID)
		if err != nil {
			s.biz.Error("hall_chat_block_check_failed", zap.Error(err))
			frameError(p, e.ID, "服务繁忙，请稍后重试")
			return
		}
		silenced = blocked
	} else {
		uploadBy = "agent"
		agent, err := s.store.GetAgentByID(ctx, a.AgentID)
		if err != nil || agent == nil || !agent.Active {
			frameError(p, e.ID, "账号不可用，请重新登录")
			return
		}
		msg.Sender, msg.SenderRef, msg.Name = "agent", itoa64(a.AgentID), agentDisplayName(agent)
	}
	if strings.TrimSpace(e.MediaURL) != "" {
		u, kind, name, size, ok := s.resolveMedia(ctx, e.MediaURL, uploadBy, msg.SenderRef)
		if !ok {
			s.sec.Warn("hall_chat_media_rejected", zap.String("identity", identity), zap.String("conn", p.ConnectionID()))
			frameError(p, e.ID, "附件无效：请通过大厅上传后再发送")
			return
		}
		msg.MediaURL = sql.NullString{String: u, Valid: true}
		msg.MediaKind = sql.NullString{String: kind, Valid: true}
		msg.MediaName = sql.NullString{String: name, Valid: true}
		msg.MediaSize = sql.NullInt64{Int64: size, Valid: true}
	} else if content == "" {
		frameError(p, e.ID, "消息内容不能为空")
		return
	}
	if silenced {
		msg.DeletedAt = sql.NullTime{Time: msg.CreatedAt, Valid: true}
		msg.DeletedBy = store.HallSilencedBy
	}
	inserted, err := s.store.InsertHallMessage(ctx, msg)
	if err != nil {
		if errors.Is(err, store.ErrHallMessageConflict) {
			s.sec.Warn("hall_chat_id_conflict", zap.String("msg_id", msg.ID), zap.String("identity", identity))
			frameError(p, e.ID, "消息标识冲突，已拒绝发送")
			return
		}
		s.biz.Error("hall_chat_persist_failed", zap.Error(err), zap.String("msg_id", msg.ID), zap.Stack("stack"))
		frameError(p, e.ID, "消息保存失败，请重试")
		return
	}
	p.Send(&ws.Envelope{Type: "hall_ack", ID: e.ID, TS: msg.CreatedAt.UnixMilli()})
	if !inserted {
		s.biz.Info("hall_chat_idempotent_retry", zap.String("msg_id", msg.ID), zap.String("identity", identity))
		return
	}
	out := &ws.Envelope{
		Type: ws.HallTypeChat, ID: e.ID, Content: msg.Content, TS: msg.CreatedAt.UnixMilli(),
		MediaURL: msg.MediaURL.String, MediaKind: msg.MediaKind.String, MediaName: msg.MediaName.String, MediaSize: msg.MediaSize.Int64,
		Extra: map[string]any{"sender": msg.Sender, "identity": identity, "name": msg.Name, "role": msg.Sender},
	}
	if silenced {
		// 拉黑静默：只回显给发送连接，不广播、不提醒
		p.Send(out)
	} else {
		s.hub.Broadcast(ctx, out)
	}
	s.biz.Info("hall_chat_persisted", zap.String("event", "hall_chat"), zap.String("msg_id", msg.ID), zap.String("identity", identity),
		zap.String("sender", msg.Sender), zap.Bool("has_media", msg.MediaURL.Valid), zap.Bool("silenced", silenced),
		zap.Int("content_runes", utf8.RuneCountInString(msg.Content)), zap.Int64("latency_ms", s.now().Sub(start).Milliseconds()))
}
