package store

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// list_flight.go [117] 会话列表与计数的并发合并（single-flight）。
//
// 多台设备 / 多个客服同时刷新时，同一时刻会有多份完全相同的列表与计数查询（同 mode、标签、分页）。
// 这里让同一键的并发请求只执行一次数据库查询，其余请求等待并共享结果；查询结束即释放，不做任何缓存，
// 因此不存在数据过期问题。共享结果按行浅拷贝后再交给各调用方（handler 会改写行内字段，避免并发写同一个 map）。
// 首个请求以脱离取消的上下文（带 20 秒超时）执行，客户端断开不会连带让其他等待者失败。

const flightTimeout = 20 * time.Second

type flightCall struct {
	done chan struct{}
	val  any
	err  error
}

type queryFlight struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

func (f *queryFlight) do(ctx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(map[string]*flightCall)
	}
	if call, ok := f.calls[key]; ok {
		f.mu.Unlock()
		select {
		case <-call.done:
			return call.val, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &flightCall{done: make(chan struct{})}
	f.calls[key] = call
	f.mu.Unlock()

	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flightTimeout)
	func() {
		defer func() {
			if p := recover(); p != nil {
				call.err = fmt.Errorf("query flight panic: %v", p)
			}
		}()
		call.val, call.err = fn(runCtx)
	}()
	cancel()

	f.mu.Lock()
	delete(f.calls, key)
	f.mu.Unlock()
	close(call.done)
	return call.val, call.err
}

func listFlightKey(kind string, onlyContacted bool, tag string, limit, offset int) string {
	return fmt.Sprintf("%s|%t|%d|%d|%s", kind, onlyContacted, limit, offset, tag)
}

// copyRows 按行浅拷贝：行内值（字符串、数字、标签切片、预览 map）调用方只读，不会被修改。
func copyRows(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		m := make(map[string]any, len(r))
		for k, v := range r {
			m[k] = v
		}
		out[i] = m
	}
	return out
}

// sharedList 列表查询的并发合并入口。
func (s *Store) sharedList(ctx context.Context, onlyContacted bool, tag string, limit, offset int) ([]map[string]any, error) {
	limit, offset = normalizeListPage(limit, offset)
	v, err := s.flight.do(ctx, listFlightKey("list", onlyContacted, tag, limit, offset), func(c context.Context) (any, error) {
		return s.listVisitorAggregated(c, onlyContacted, tag, limit, offset)
	})
	if err != nil {
		return nil, err
	}
	rows, _ := v.([]map[string]any)
	return copyRows(rows), nil
}

// sharedCount 计数查询的并发合并入口。
func (s *Store) sharedCount(ctx context.Context, onlyContacted bool, tag string) (int, error) {
	v, err := s.flight.do(ctx, listFlightKey("count", onlyContacted, tag, 0, 0), func(c context.Context) (any, error) {
		return s.countVisibleVisitors(c, onlyContacted, tag)
	})
	if err != nil {
		return 0, err
	}
	n, _ := v.(int)
	return n, nil
}
