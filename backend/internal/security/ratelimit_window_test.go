package security

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// 固定窗口限流回归测试（[119] 主代理在 Mac 集成测试发现：每次请求都刷新过期时间，计数窗口变成「从最后一次请求起算」，
// 只要请求间隔小于窗口就永不清零，持续的慢速请求最终被永久误拒）。

func newTestLimiter(t *testing.T) (*RateLimiter, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRateLimiter(rdb, zap.NewNop()), mr, rdb
}

func rlKey(action, dim string) string { return "rl:" + action + ":" + hashDimension(dim) }

// 正常 / 拒绝：窗口内前 limit 次放行，超出拒绝；窗口到期后计数重置。
func TestAllowFixedWindowRejectsOverLimit(t *testing.T) {
	rl, mr, _ := newTestLimiter(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		ok, err := rl.AllowAction(ctx, "t_burst", 5, 10*time.Second, "agent:1")
		if err != nil || !ok {
			t.Fatalf("第 %d 次应放行：ok=%v err=%v", i, ok, err)
		}
	}
	if ok, err := rl.AllowAction(ctx, "t_burst", 5, 10*time.Second, "agent:1"); err != nil || ok {
		t.Fatalf("超出上限应拒绝：ok=%v err=%v", ok, err)
	}
	// 被拒的请求不能延长窗口：从第一次请求起 10 秒后必须恢复
	mr.FastForward(10*time.Second + time.Millisecond)
	if ok, err := rl.AllowAction(ctx, "t_burst", 5, 10*time.Second, "agent:1"); err != nil || !ok {
		t.Fatalf("窗口到期后应恢复：ok=%v err=%v", ok, err)
	}
}

// 绕过/误伤：持续低于上限的速率（每 600ms 一次，10 秒约 17 次 < 20）跨多个窗口都不能被误拒。
func TestAllowSteadyRateBelowLimitNeverRejected(t *testing.T) {
	rl, mr, _ := newTestLimiter(t)
	ctx := context.Background()
	for i := 1; i <= 60; i++ { // 36 秒，跨 3 个以上窗口
		ok, err := rl.AllowAction(ctx, "t_steady", 20, 10*time.Second, "agent:2")
		if err != nil || !ok {
			t.Fatalf("第 %d 次（约 %.1f 秒）被误拒：ok=%v err=%v", i, float64(i-1)*0.6, ok, err)
		}
		mr.FastForward(600 * time.Millisecond)
	}
}

// 访客消息限流（AllowVisitorMessage，1 分钟窗口）同样是固定窗口。
func TestAllowVisitorMessageFixedWindow(t *testing.T) {
	rl, mr, _ := newTestLimiter(t)
	ctx := context.Background()
	for i := 1; i <= 90; i++ { // 每 2 秒 1 条 = 30 条/分钟，低于 40 条/分钟上限
		ok, err := rl.AllowVisitorMessage(ctx, "vid-steady", 40)
		if err != nil || !ok {
			t.Fatalf("第 %d 条被误拒：ok=%v err=%v", i, ok, err)
		}
		mr.FastForward(2 * time.Second)
	}
}

// 键一定带过期：首次计数即设置过期；即使计数键异常丢失过期（PTTL=-1），下一次请求也会补设，防止永不过期。
func TestAllowKeyAlwaysHasTTL(t *testing.T) {
	rl, mr, rdb := newTestLimiter(t)
	ctx := context.Background()
	if _, err := rl.AllowAction(ctx, "t_ttl", 3, 10*time.Second, "agent:3"); err != nil {
		t.Fatal(err)
	}
	key := rlKey("t_ttl", "agent:3")
	ttl, err := rdb.PTTL(ctx, key).Result()
	if err != nil || ttl <= 0 || ttl > 10*time.Second {
		t.Fatalf("计数键必须带过期且不超过窗口：%v %v", ttl, err)
	}
	// 后续请求不刷新过期时间（固定窗口）
	mr.FastForward(4 * time.Second)
	if _, err := rl.AllowAction(ctx, "t_ttl", 3, 10*time.Second, "agent:3"); err != nil {
		t.Fatal(err)
	}
	if ttl2, _ := rdb.PTTL(ctx, key).Result(); ttl2 > 6*time.Second+50*time.Millisecond {
		t.Fatalf("后续请求不应刷新过期时间：剩余 %v", ttl2)
	}
	// 模拟过期丢失：计数键变成永久键
	if err := rdb.Persist(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	if ttl3, _ := rdb.PTTL(ctx, key).Result(); ttl3 != -1 {
		t.Fatalf("前置条件：应已是永久键，实际 %v", ttl3)
	}
	_, _ = rl.AllowAction(ctx, "t_ttl", 3, 10*time.Second, "agent:3")
	if ttl4, _ := rdb.PTTL(ctx, key).Result(); ttl4 <= 0 || ttl4 > 10*time.Second {
		t.Fatalf("丢失过期的计数键应被补设过期：%v", ttl4)
	}
	// 亚毫秒窗口也不能写出永久键
	if _, err := rl.AllowAction(ctx, "t_tiny", 3, 100*time.Microsecond, "agent:3"); err != nil {
		t.Fatal(err)
	}
	if ttl5, _ := rdb.PTTL(ctx, rlKey("t_tiny", "agent:3")).Result(); ttl5 == -1 {
		t.Fatal("亚毫秒窗口不能产生永久键")
	}
}

// 多维度：任一维度超限即拒绝（换账号不换令牌、换令牌不换账号都挡得住）。
func TestAllowActionAnyDimensionExhaustedRejects(t *testing.T) {
	rl, _, _ := newTestLimiter(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if ok, _ := rl.AllowAction(ctx, "t_multi", 3, time.Minute, "user:a", "tok:x"); !ok {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if ok, _ := rl.AllowAction(ctx, "t_multi", 3, time.Minute, "user:b", "tok:x"); ok {
		t.Fatal("同一令牌维度已超限，换账号也应拒绝")
	}
	if ok, _ := rl.AllowAction(ctx, "t_multi", 3, time.Minute, "user:c", "tok:y"); !ok {
		t.Fatal("全新维度应放行")
	}
}

// 高并发：计数原子，并发 100 个请求恰好放行 limit 个。
func TestAllowAtomicUnderConcurrency(t *testing.T) {
	rl, _, _ := newTestLimiter(t)
	ctx := context.Background()
	var allowed int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := rl.AllowAction(ctx, "t_conc", 20, time.Minute, "agent:9"); err == nil && ok {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	wg.Wait()
	if allowed != 20 {
		t.Fatalf("并发下应恰好放行 20 个，实际 %d", allowed)
	}
}

// Redis 故障：返回错误且拒绝（调用方据此拒绝高风险动作，不因限流失效被滥用）。
func TestAllowRedisFailureDenies(t *testing.T) {
	rl, mr, _ := newTestLimiter(t)
	mr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ok, err := rl.AllowAction(ctx, "t_down", 5, time.Minute, "agent:1")
	if err == nil || ok {
		t.Fatalf("Redis 故障应返回错误并拒绝：ok=%v err=%v", ok, err)
	}
	ok, err = rl.AllowVisitorMessage(ctx, "vid-down", 10)
	if err == nil || ok {
		t.Fatalf("访客消息限流在 Redis 故障时应返回错误并拒绝：ok=%v err=%v", ok, err)
	}
}
