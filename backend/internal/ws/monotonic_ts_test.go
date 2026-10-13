package ws

import "testing"

// [110] 服务端消息时间戳必须严格递增：同一毫秒、时钟回拨都不能产生重复或倒序。
func TestMonotonicTS(t *testing.T) {
	cases := []struct{ last, now, want int64 }{
		{0, 1000, 1000},
		{1000, 1001, 1001},
		{1001, 1001, 1002}, // 同一毫秒
		{1002, 900, 1003},  // 时钟回拨
	}
	for _, c := range cases {
		if got := monotonicTS(c.last, c.now); got != c.want {
			t.Fatalf("monotonicTS(%d,%d)=%d want %d", c.last, c.now, got, c.want)
		}
	}
	last := int64(0)
	for i := 0; i < 1000; i++ {
		next := monotonicTS(last, 5000)
		if next <= last {
			t.Fatalf("not strictly increasing at %d: %d <= %d", i, next, last)
		}
		last = next
	}
}
