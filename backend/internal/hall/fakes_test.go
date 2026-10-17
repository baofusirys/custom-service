package hall

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/livekit"
	"github.com/custom-service/backend/internal/security"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

const (
	tKey    = "APIhallUnitKey"
	tSecret = "unit-0123456789abcdefghijklmnopqrstuvwxyz"
)

// ============ 假数据层 ============

type fakeStore struct {
	mu       sync.Mutex
	msgs     map[string]*store.HallMessage
	files    map[string]*store.FileRecord
	blocked  map[string]bool
	settings map[string]string
	agents   map[int64]*store.Agent
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		msgs: map[string]*store.HallMessage{}, files: map[string]*store.FileRecord{}, blocked: map[string]bool{},
		settings: map[string]string{
			KeyEnabled: "true", KeyVideoEnabled: "false", KeyMaxSpeakers: "6", KeyMaxCameras: "2", KeyMaxParticipants: "100",
		},
		agents: map[int64]*store.Agent{
			1: {ID: 1, Username: "admin1", Role: "admin", Nickname: "管理员", Active: true},
			2: {ID: 2, Username: "agent2", Role: "agent", Nickname: "小王", Active: true},
			3: {ID: 3, Username: "agent3", Role: "agent", Nickname: "", Active: true},
		},
	}
}

func (f *fakeStore) set(k, v string) {
	f.mu.Lock()
	f.settings[k] = v
	f.mu.Unlock()
}

func (f *fakeStore) InsertHallMessage(_ context.Context, m *store.HallMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if old, ok := f.msgs[strings.ToLower(m.ID)]; ok {
		if old.Sender == m.Sender && old.SenderRef == m.SenderRef && old.Content == m.Content && old.MediaURL.String == m.MediaURL.String {
			return false, nil
		}
		return false, store.ErrHallMessageConflict
	}
	cp := *m
	f.msgs[strings.ToLower(m.ID)] = &cp
	return true, nil
}

func (f *fakeStore) ListHallMessages(_ context.Context, room, beforeID string, limit int, viewer store.HallViewer) ([]store.HallMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []store.HallMessage
	for _, m := range f.msgs {
		if m.Room != room {
			continue
		}
		visible := !m.DeletedAt.Valid || (m.DeletedBy == store.HallSilencedBy && m.Sender == viewer.Sender && m.SenderRef == viewer.SenderRef)
		if visible {
			all = append(all, *m)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID < all[j].ID
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	if beforeID != "" {
		b, ok := f.msgs[strings.ToLower(beforeID)]
		if !ok {
			return []store.HallMessage{}, nil
		}
		var cut []store.HallMessage
		for _, m := range all {
			if m.CreatedAt.Before(b.CreatedAt) || (m.CreatedAt.Equal(b.CreatedAt) && m.ID < b.ID) {
				cut = append(cut, m)
			}
		}
		all = cut
	}
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

func (f *fakeStore) SoftDeleteHallMessage(_ context.Context, room, id, by string, at time.Time) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.msgs[strings.ToLower(id)]
	if !ok || m.Room != room {
		return false, false, nil
	}
	if m.DeletedAt.Valid {
		return true, false, nil
	}
	m.DeletedAt.Time, m.DeletedAt.Valid, m.DeletedBy = at, true, by
	return true, true, nil
}

func (f *fakeStore) GetFileByStoreKey(_ context.Context, key string) (*store.FileRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[key], nil
}

func (f *fakeStore) IsVisitorBlocked(_ context.Context, vid string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocked[vid], nil
}

func (f *fakeStore) BlockedVisitorIDs(_ context.Context, vids []string) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for _, v := range vids {
		if f.blocked[v] {
			out[v] = true
		}
	}
	return out, nil
}

func (f *fakeStore) GetSettingsMap(_ context.Context, keys []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := f.settings[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (f *fakeStore) GetAgentByID(_ context.Context, id int64) (*store.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.agents[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, nil
}

// addHallFile 模拟 /api/upload 签发的文件，返回带 access 的 URL。
func (f *fakeStore) addHallFile(t *testing.T, conv, uploadBy, ref, mime, name string) string {
	t.Helper()
	plain, hash, err := security.NewOpaqueAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("0", 8) + "-0000-4000-8000-" + strings.Repeat("0", 11) + string(rune('a'+len(f.files)%6))
	key := "2026/10/17/" + id + ".png"
	f.mu.Lock()
	f.files[key] = &store.FileRecord{ID: id, ConvID: conv, UploadBy: uploadBy, UploaderRef: ref, Filename: name, StoreKey: key, Size: 1234, MIME: mime, AccessTokenHash: hash}
	f.mu.Unlock()
	return "/files/" + key + "?access=" + plain
}

// ============ 假 LiveKit ============

type fakeMedia struct {
	mu          sync.Mutex
	enabled     bool
	parts       map[string]*livekit.ParticipantInfo
	perms       map[string]livekit.Permission
	updateErr   error
	listErr     error
	removeErr   error
	removed     []string
	updateCalls int
}

func newFakeMedia() *fakeMedia {
	return &fakeMedia{enabled: true, parts: map[string]*livekit.ParticipantInfo{}, perms: map[string]livekit.Permission{}}
}

func (m *fakeMedia) Enabled() bool { return m.enabled }

func (m *fakeMedia) IssueToken(p livekit.ParticipantToken) (string, time.Time, error) {
	return livekit.IssueParticipantToken(tKey, tSecret, p, time.Now())
}

func (m *fakeMedia) VerifyWebhook(auth string, body []byte) (*livekit.WebhookEvent, error) {
	return livekit.VerifyWebhook(tKey, tSecret, auth, body, time.Now())
}

func (m *fakeMedia) join(identity string, perm *livekit.Permission, tracks ...livekit.TrackInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parts[identity] = &livekit.ParticipantInfo{Identity: identity, Tracks: tracks, Permission: perm, JoinedAt: time.Now().Unix()}
}

func (m *fakeMedia) ListParticipants(context.Context, string) ([]livekit.ParticipantInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	ids := make([]string, 0, len(m.parts))
	for id := range m.parts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]livekit.ParticipantInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, *m.parts[id])
	}
	return out, nil
}

func (m *fakeMedia) UpdatePermission(_ context.Context, _ string, identity string, perm livekit.Permission) (*livekit.ParticipantInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateCalls++
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	p, ok := m.parts[identity]
	if !ok {
		return nil, livekit.ErrNotFound
	}
	m.perms[identity] = perm
	cp := perm
	p.Permission = &cp
	// 模拟 LiveKit：移除不再允许来源的轨道
	kept := p.Tracks[:0]
	for _, t := range p.Tracks {
		if p.CanPublishSource(t.Source) {
			kept = append(kept, t)
		}
	}
	p.Tracks = kept
	out := *p
	return &out, nil
}

func (m *fakeMedia) MutePublishedTrack(context.Context, string, string, string, bool) error {
	return nil
}

func (m *fakeMedia) RemoveParticipant(_ context.Context, _ string, identity string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.removeErr != nil {
		return m.removeErr
	}
	m.removed = append(m.removed, identity)
	if _, ok := m.parts[identity]; !ok {
		return livekit.ErrNotFound
	}
	delete(m.parts, identity)
	return nil
}

func (m *fakeMedia) perm(identity string) (livekit.Permission, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.perms[identity]
	return p, ok
}

func hasSource(p livekit.Permission, src string) bool {
	if !p.CanPublish {
		return false
	}
	for _, s := range p.CanPublishSources {
		if s == src {
			return true
		}
	}
	return false
}

// ============ 假 Hub 与连接 ============

type fakeHub struct {
	mu         sync.Mutex
	subs       map[string]bool
	broadcasts []*ws.Envelope
}

func newFakeHub() *fakeHub { return &fakeHub{subs: map[string]bool{}} }

func (h *fakeHub) Subscribe(p Peer) {
	h.mu.Lock()
	h.subs[p.ConnectionID()] = true
	h.mu.Unlock()
}

func (h *fakeHub) Unsubscribe(p Peer) {
	h.mu.Lock()
	delete(h.subs, p.ConnectionID())
	h.mu.Unlock()
}

func (h *fakeHub) Subscribed(p Peer) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.subs[p.ConnectionID()]
}

func (h *fakeHub) Broadcast(_ context.Context, e *ws.Envelope) {
	h.mu.Lock()
	cp := *e
	h.broadcasts = append(h.broadcasts, &cp)
	h.mu.Unlock()
}

func (h *fakeHub) find(pred func(*ws.Envelope) bool) *ws.Envelope {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.broadcasts {
		if pred(e) {
			return e
		}
	}
	return nil
}

type fakePeer struct {
	visitor bool
	id      string
	conn    string
	mu      sync.Mutex
	out     []*ws.Envelope
}

func (p *fakePeer) IsVisitor() bool      { return p.visitor }
func (p *fakePeer) ActorID() string      { return p.id }
func (p *fakePeer) ConnectionID() string { return p.conn }
func (p *fakePeer) Send(e *ws.Envelope) bool {
	p.mu.Lock()
	cp := *e
	p.out = append(p.out, &cp)
	p.mu.Unlock()
	return true
}

func (p *fakePeer) wait(t *testing.T, pred func(*ws.Envelope) bool) *ws.Envelope {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		for _, e := range p.out {
			if pred(e) {
				p.mu.Unlock()
				return e
			}
		}
		p.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待下行帧超时，已收到 %d 帧", len(p.out))
	return nil
}

// ============ 组装 ============

type harness struct {
	svc   *Service
	store *fakeStore
	media *fakeMedia
	hub   *fakeHub
	mr    *miniredis.Miniredis
	rdb   *redis.Client
}

var testDataKey = []byte("0123456789abcdef0123456789abcdef")

func newHarness(t *testing.T) *harness {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	st, media, hub := newFakeStore(), newFakeMedia(), newFakeHub()
	svc, err := New(Deps{
		Room: store.HallDefaultRoom, Store: st, Redis: rdb, Media: media, Hub: hub,
		Limiter: security.NewRateLimiter(rdb, zap.NewNop()), DataKey: testDataKey, PublicURL: "wss://cs.example.com/lk",
		ICE: func(label string) []ICEServer {
			return []ICEServer{{URLs: []string{"turn:turn.example.com:3478?transport=udp"}, Username: "1800000000:hall-" + label, Credential: "x"}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{svc: svc, store: st, media: media, hub: hub, mr: mr, rdb: rdb}
}

func (h *harness) setSetting(k, v string) {
	h.store.set(k, v)
	h.svc.InvalidateSettings()
}

func visitorA(vid string) Actor { return Actor{Visitor: true, VisitorID: vid, SiteID: "default"} }

func agentA(id int64, admin bool) Actor { return Actor{AgentID: id, Admin: admin} }

func asErr(t *testing.T, err error, code int) *Error {
	t.Helper()
	he, ok := err.(*Error)
	if !ok || he.Code != code {
		t.Fatalf("期望业务码 %d，实际 %v", code, err)
	}
	return he
}

func jwtPayload(t *testing.T, tok string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
