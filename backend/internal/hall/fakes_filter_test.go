package hall

import (
	"context"
	"time"

	"github.com/custom-service/backend/internal/store"
)

// [122] fakeStore 实现 FilterStore（内存版，语义与 store/hall_filter.go 一致）。

func (f *fakeStore) SetSettings(_ context.Context, kv map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range kv {
		f.settings[k] = v
	}
	return nil
}

func (f *fakeStore) InsertHallFilterEvent(_ context.Context, e *store.HallFilterEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.fevents {
		if x.MessageID == e.MessageID {
			return nil
		}
	}
	f.fnext++
	c := *e
	c.ID = f.fnext
	f.fevents = append(f.fevents, c)
	return nil
}

// fakeNextAt 测试用：记录每条记录的下次重试时间（真实实现存在 mail_next_at 列）。
var fakeNextAt = map[int64]time.Time{}

func (f *fakeStore) pending(now time.Time) []int {
	var idx []int
	for i, e := range f.fevents {
		if e.MailStatus == store.HallMailPending && (fakeNextAt[e.ID].IsZero() || !fakeNextAt[e.ID].After(now)) {
			idx = append(idx, i)
		}
	}
	return idx
}

func (f *fakeStore) CountPendingHallFilterMail(_ context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending(now)), nil
}

func (f *fakeStore) ListPendingHallFilterMail(_ context.Context, now time.Time, limit int) ([]store.HallFilterEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.HallFilterEvent
	for _, i := range f.pending(now) {
		if len(out) >= limit {
			break
		}
		out = append(out, f.fevents[i])
	}
	return out, nil
}

func (f *fakeStore) MarkHallFilterMailSent(_ context.Context, ids []int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.fevents {
		for _, id := range ids {
			if f.fevents[i].ID == id && f.fevents[i].MailStatus == store.HallMailPending {
				f.fevents[i].MailStatus = store.HallMailSent
				f.fevents[i].MailAttempts++
				f.fevents[i].MailError = ""
			}
		}
	}
	return nil
}

func (f *fakeStore) MarkHallFilterMailRetry(_ context.Context, ids []int64, maxAttempts int, nextAt time.Time, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.fevents {
		for _, id := range ids {
			if f.fevents[i].ID == id && f.fevents[i].MailStatus == store.HallMailPending {
				f.fevents[i].MailAttempts++
				if f.fevents[i].MailAttempts >= maxAttempts {
					f.fevents[i].MailStatus = store.HallMailFailed
				}
				f.fevents[i].MailError = reason
				fakeNextAt[f.fevents[i].ID] = nextAt
			}
		}
	}
	return nil
}

func (f *fakeStore) ListHallFilterEvents(_ context.Context, beforeID int64, limit int) ([]store.HallFilterEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.HallFilterEvent
	for i := len(f.fevents) - 1; i >= 0 && len(out) < limit; i-- {
		if beforeID > 0 && f.fevents[i].ID >= beforeID {
			continue
		}
		out = append(out, f.fevents[i])
	}
	return out, nil
}
