package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/store"
)

// fakeHallCleanupStore 按预设批次返回，记录调用参数。
type fakeHallCleanupStore struct {
	msgBatches  []int
	msgErr      error
	fileBatches [][]store.HallFileRef
	deleted     []string
	gotRoom     string
	gotCutoff   time.Time
	msgCalls    int
	fileCalls   int
}

func (f *fakeHallCleanupStore) PurgeHallMessagesBefore(_ context.Context, before time.Time, limit int) (int, error) {
	f.gotCutoff = before
	if limit != hallPurgeMsgBatch {
		return 0, errors.New("unexpected batch size")
	}
	if f.msgErr != nil {
		return 0, f.msgErr
	}
	if f.msgCalls >= len(f.msgBatches) {
		f.msgCalls++
		return 0, nil
	}
	n := f.msgBatches[f.msgCalls]
	f.msgCalls++
	return n, nil
}

func (f *fakeHallCleanupStore) HallFilesBefore(_ context.Context, room string, _ time.Time, _ int) ([]store.HallFileRef, error) {
	f.gotRoom = room
	if f.fileCalls >= len(f.fileBatches) {
		f.fileCalls++
		return nil, nil
	}
	b := f.fileBatches[f.fileCalls]
	f.fileCalls++
	return b, nil
}

func (f *fakeHallCleanupStore) DeleteFileRecords(_ context.Context, ids []string) (int, error) {
	f.deleted = append(f.deleted, ids...)
	return len(ids), nil
}

func newCleanupTestService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	uploads := filepath.Join(root, "uploads")
	if err := os.MkdirAll(filepath.Join(uploads, "2026", "09"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Service{uploadsDir: uploads, bizLog: zap.NewNop(), secLog: zap.NewNop()}, root
}

func writeFile(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 正常：消息分批删到 0 为止；附件先删记录再删磁盘文件；房间与截止时间原样传入。
// 绕过：store_key 带 ../ 或绝对路径的记录只删数据库记录，绝不删除上传目录外的文件。
func TestPurgeHallBeforeDeletesMessagesAndFiles(t *testing.T) {
	old := hallCleanupPause
	hallCleanupPause = time.Millisecond
	t.Cleanup(func() { hallCleanupPause = old })

	s, root := newCleanupTestService(t)
	writeFile(t, filepath.Join(s.uploadsDir, "2026", "09", "a.png"))
	writeFile(t, filepath.Join(s.uploadsDir, "2026", "09", "b.pdf"))
	outside := filepath.Join(root, "outside.txt")
	writeFile(t, outside)

	st := &fakeHallCleanupStore{
		msgBatches: []int{1000, 1000, 7},
		fileBatches: [][]store.HallFileRef{
			{{ID: "f1", StoreKey: "2026/09/a.png"}, {ID: "f2", StoreKey: "../outside.txt"}},
			{{ID: "f3", StoreKey: "2026/09/b.pdf"}, {ID: "f4", StoreKey: "2026/09/missing.png"}, {ID: "f5", StoreKey: outside}},
		},
	}
	cutoff := time.Date(2026, 9, 17, 4, 30, 0, 0, time.FixedZone("CST", 8*3600))
	res, err := s.purgeHallBefore(context.Background(), st, store.HallDefaultRoom, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 2007 || st.msgCalls != 4 {
		t.Fatalf("消息应分批删到 0 为止：%+v calls=%d", res, st.msgCalls)
	}
	if res.FileRecords != 5 || len(st.deleted) != 5 {
		t.Fatalf("附件记录应全部删除：%+v", res)
	}
	if res.FilesRemoved != 2 {
		t.Fatalf("只应删除上传目录内真实存在的 2 个文件：%+v", res)
	}
	if st.gotRoom != store.HallDefaultRoom || !st.gotCutoff.Equal(cutoff) {
		t.Fatalf("房间或截止时间未原样传入：%s %v", st.gotRoom, st.gotCutoff)
	}
	for _, p := range []string{"a.png", "b.pdf"} {
		if _, err := os.Stat(filepath.Join(s.uploadsDir, "2026", "09", p)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s 应已删除", p)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("上传目录外的文件绝不能被删除（路径穿越 / 绝对路径）")
	}
}

// 拒绝：删除消息失败时立即返回错误，不继续处理附件（避免消息还在、附件先没了）。
func TestPurgeHallBeforeStopsOnError(t *testing.T) {
	s, _ := newCleanupTestService(t)
	st := &fakeHallCleanupStore{msgErr: errors.New("db down"), fileBatches: [][]store.HallFileRef{{{ID: "f1", StoreKey: "x"}}}}
	if _, err := s.purgeHallBefore(context.Background(), st, store.HallDefaultRoom, time.Now()); err == nil {
		t.Fatal("删除消息失败应返回错误")
	}
	if st.fileCalls != 0 || len(st.deleted) != 0 {
		t.Fatal("消息删除失败后不应继续删除附件")
	}
}

// 健壮性：任务被取消（进程退出 / 超时）时在批间立即返回，不会继续空转。
func TestPurgeHallBeforeHonorsCancel(t *testing.T) {
	old := hallCleanupPause
	hallCleanupPause = time.Hour
	t.Cleanup(func() { hallCleanupPause = old })
	s, _ := newCleanupTestService(t)
	st := &fakeHallCleanupStore{msgBatches: []int{1000, 1000, 1000}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	done := make(chan error, 1)
	go func() {
		_, err := s.purgeHallBefore(ctx, st, store.HallDefaultRoom, time.Now())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应返回取消错误：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后应立即返回")
	}
	if st.msgCalls != 1 {
		t.Fatalf("取消后不应继续删除：calls=%d", st.msgCalls)
	}
}
