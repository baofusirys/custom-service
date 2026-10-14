package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueryFlightCoalescesConcurrentCalls(t *testing.T) {
	var f queryFlight
	var runs atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]any, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			v, err := f.do(context.Background(), "k", func(context.Context) (any, error) {
				runs.Add(1)
				time.Sleep(50 * time.Millisecond)
				return 42, nil
			})
			if err != nil {
				t.Errorf("unexpected err: %v", err)
			}
			results[i] = v
		}(i)
	}
	close(start)
	wg.Wait()
	if runs.Load() != 1 {
		t.Fatalf("并发同键查询应只执行一次，实际 %d 次", runs.Load())
	}
	for _, r := range results {
		if r != 42 {
			t.Fatalf("所有等待者应拿到同一结果，实际 %v", r)
		}
	}
	if _, err := f.do(context.Background(), "k", func(context.Context) (any, error) { return nil, errors.New("x") }); err == nil {
		t.Fatal("结束后同键应重新执行（不缓存）")
	}
}

func TestQueryFlightLeaderCancelDoesNotFailFollowers(t *testing.T) {
	var f queryFlight
	leaderCtx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := f.do(leaderCtx, "k", func(c context.Context) (any, error) {
			close(started)
			select {
			case <-c.Done():
				return nil, c.Err()
			case <-time.After(80 * time.Millisecond):
				return 1, nil
			}
		})
		done <- err
	}()
	<-started
	cancel() // 发起者断开
	v, err := f.do(context.Background(), "k", func(context.Context) (any, error) { return 2, nil })
	if err != nil || v != 1 {
		t.Fatalf("发起者取消不应影响等待者，实际 v=%v err=%v", v, err)
	}
	<-done
}

func TestCopyRowsIsolatesCallers(t *testing.T) {
	rows := []map[string]any{{"ip_cipher": "x", "id": "1"}}
	a := copyRows(rows)
	delete(a[0], "ip_cipher")
	a[0]["ip"] = "1.2.3.4"
	if _, ok := rows[0]["ip_cipher"]; !ok {
		t.Fatal("调用方修改不得影响共享结果")
	}
}
