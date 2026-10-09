package auth

import (
	"testing"
	"time"
)

// TestRateLimiterRetryAfter 断言退避提示取窗口内最早一次调用的到期时刻。
//
// 滑动窗口下这是最短等待时长：页面按它显示倒计时，写大了会让用户白等。
func TestRateLimiterRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	l := newRateLimiter(time.Minute, 2, func() time.Time { return now })

	if got := l.retryAfter("k"); got != 0 {
		t.Fatalf("无记录时退避提示应为 0，实际 %v", got)
	}

	if ok := l.allow("k"); !ok {
		t.Fatal("窗口内第 1 次调用应放行")
	}
	now = now.Add(20 * time.Second)
	if ok := l.allow("k"); !ok {
		t.Fatal("窗口内第 2 次调用应放行")
	}

	now = now.Add(20 * time.Second)
	if ok := l.allow("k"); ok {
		t.Fatal("窗口内第 3 次调用应被拒")
	}
	if got := l.retryAfter("k"); got != 20*time.Second {
		t.Fatalf("退避提示应为 20s（12:00:00 那次滑出窗口的时刻），实际 %v", got)
	}

	now = now.Add(time.Minute)
	if got := l.retryAfter("k"); got != 0 {
		t.Fatalf("记录全部滑出窗口后应为 0，实际 %v", got)
	}
}
