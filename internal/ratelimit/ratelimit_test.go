package ratelimit

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

// fakeClock 是可推进的测试时钟：令牌回填不依赖真实时间，测试不做 sleep。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// virtualOptions 返回一套不真实等待的构造参数：等待即时推进虚拟时钟。
func virtualOptions(clock *fakeClock, maxWait time.Duration) Options {
	return Options{
		MaxWait: maxWait,
		Clock:   clock.Now,
		Wait: func(_ context.Context, d time.Duration) error {
			clock.Advance(d)
			return nil
		},
		// 固定为 0：抖动不参与本文件的取值断言。
		Rand: func(int64) int64 { return 0 },
	}
}

// newVirtualManager 构造一个使用虚拟时钟的限流器管理。
func newVirtualManager(clock *fakeClock, maxWait time.Duration) *Manager {
	return NewManager(virtualOptions(clock, maxWait))
}

// TestFirstAcquirePassesImmediately 断言令牌桶起始满桶：渠道首个请求不为等令牌平白等待。
func TestFirstAcquirePassesImmediately(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newVirtualManager(clock, 5*time.Second)
	route := domain.Route{ChannelID: 1, RateLimitQPS: 2}

	release, waited, err := m.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("首次获取失败：%v", err)
	}
	defer release()
	if waited != 0 {
		t.Errorf("首次等待 = %s，期望 0", waited)
	}
}

// TestTokenBucketRate 断言第二与第三次获取分别按 1/QPS 的间隔等待。
func TestTokenBucketRate(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newVirtualManager(clock, 5*time.Second)
	route := domain.Route{ChannelID: 1, RateLimitQPS: 2}

	first, _, err := m.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("首次获取失败：%v", err)
	}
	defer first()

	second, waited, err := m.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("第二次获取失败：%v", err)
	}
	defer second()
	if waited != 500*time.Millisecond {
		t.Errorf("第二次等待 = %s，期望 500ms", waited)
	}
	if got := clock.Now().Sub(time.Unix(1_700_000_000, 0)); got != 500*time.Millisecond {
		t.Errorf("虚拟时钟推进 = %s，期望 500ms", got)
	}

	third, waited, err := m.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("第三次获取失败：%v", err)
	}
	defer third()
	if waited != 500*time.Millisecond {
		t.Errorf("第三次等待 = %s，期望 500ms", waited)
	}
}

// TestWaitTimeoutReturnsRetryableError 断言等待上限内取不到令牌时返回可重试的上游失败。
func TestWaitTimeoutReturnsRetryableError(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newVirtualManager(clock, 500*time.Millisecond)
	route := domain.Route{ChannelID: 1, RateLimitQPS: 1}

	first, _, err := m.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("首次获取失败：%v", err)
	}
	defer first()

	release, _, err := m.Acquire(context.Background(), route)
	if err == nil {
		release()
		t.Fatal("等待超时应返回错误")
	}
	if release != nil {
		t.Error("失败时不应返回 release")
	}
	domainErr := domain.AsError(err)
	if domainErr == nil || domainErr.Code != domain.CodeUpstreamRateLimited {
		t.Fatalf("错误码 = %v，期望 %s", err, domain.CodeUpstreamRateLimited)
	}
	if !domain.Retryable(err) {
		t.Error("限流等待超时应按可重试处理，以便换下一条候选")
	}
}

// TestZeroLimitsAreUnlimited 断言两项上限为 0 时不限流。
func TestZeroLimitsAreUnlimited(t *testing.T) {
	m := NewManager(Options{})
	route := domain.Route{ChannelID: 1}

	releases := make([]func(), 0, 3)
	for i := 0; i < 3; i++ {
		release, waited, err := m.Acquire(context.Background(), route)
		if err != nil {
			t.Fatalf("第 %d 次获取失败：%v", i+1, err)
		}
		if waited != 0 {
			t.Errorf("第 %d 次等待 = %s，期望 0", i+1, waited)
		}
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}
}

// TestConcurrencyLimitBlocksUntilRelease 断言并发位独占：占满后 ctx 取消即失败，释放后可再获取。
//
// 用已取消的 ctx 替代「真等一会儿再看有没有拿到」，使断言不依赖调度时机。
func TestConcurrencyLimitBlocksUntilRelease(t *testing.T) {
	m := NewManager(Options{})
	route := domain.Route{ChannelID: 1, RateLimitConcurrency: 1}

	release, _, err := m.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("首次获取失败：%v", err)
	}
	if got := len(m.forChannel(route).slots); got != 1 {
		t.Fatalf("已占并发位 = %d，期望 1", got)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, waitErr := m.Acquire(cancelled, route); !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("并发位占满时应等待至取消，实际错误 %v", waitErr)
	}

	release()
	if got := len(m.forChannel(route).slots); got != 0 {
		t.Fatalf("释放后已占并发位 = %d，期望 0", got)
	}
	second, _, err := m.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("释放后再次获取失败：%v", err)
	}
	second()
}

// TestChannelsAreIndependent 断言不同渠道 id 各自计数，互不干扰。
func TestChannelsAreIndependent(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newVirtualManager(clock, 5*time.Second)
	limited := domain.Route{ChannelID: 1, RateLimitQPS: 1}
	other := domain.Route{ChannelID: 2, RateLimitQPS: 1}

	first, _, err := m.Acquire(context.Background(), limited)
	if err != nil {
		t.Fatalf("渠道 1 首次获取失败：%v", err)
	}
	defer first()

	release, waited, err := m.Acquire(context.Background(), other)
	if err != nil {
		t.Fatalf("渠道 2 获取失败：%v", err)
	}
	defer release()
	if waited != 0 {
		t.Errorf("另一条渠道的等待 = %s，期望 0（令牌桶互不共享）", waited)
	}
}

// TestLimitChangeRefreshesLimiter 断言渠道上限变化后重建限流器，新上限立即生效。
func TestLimitChangeRefreshesLimiter(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newVirtualManager(clock, 5*time.Second)
	route := domain.Route{ChannelID: 1, RateLimitQPS: 1}

	first, _, err := m.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("首次获取失败：%v", err)
	}
	defer first()

	// 只看第二个请求必须等待，证明旧桶确实已空。
	second, waited, err := m.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("第二次获取失败：%v", err)
	}
	defer second()
	if waited == 0 {
		t.Fatal("旧上限下第二次应等待")
	}

	// 提高上限后重建：新桶重新计数，立即通过。
	raised := route
	raised.RateLimitQPS = 100
	release, waited, err := m.Acquire(context.Background(), raised)
	if err != nil {
		t.Fatalf("提高上限后获取失败：%v", err)
	}
	defer release()
	if waited != 0 {
		t.Errorf("提高上限后的等待 = %s，期望 0（限流器应已重建）", waited)
	}
}

// TestConcurrentAcquireReleaseKeepsConcurrencyBound 在 -race 下压并发位不泄漏：
//
// 并发上限 4，200 个 goroutine 反复获取与释放，任一时刻在途数不得超过 4，
// 结束后必须没有残留占用。
func TestConcurrentAcquireReleaseKeepsConcurrencyBound(t *testing.T) {
	const (
		limit  = 4
		rounds = 200
	)
	m := NewManager(Options{})
	route := domain.Route{ChannelID: 9, RateLimitConcurrency: limit}

	var (
		wg        sync.WaitGroup
		active    int64
		maxActive int64
	)
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, _, err := m.Acquire(context.Background(), route)
			if err != nil {
				t.Errorf("获取失败：%v", err)
				return
			}
			current := atomic.AddInt64(&active, 1)
			for {
				observed := atomic.LoadInt64(&maxActive)
				if current <= observed || atomic.CompareAndSwapInt64(&maxActive, observed, current) {
					break
				}
			}
			runtime.Gosched()
			atomic.AddInt64(&active, -1)
			release()
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&maxActive); got > limit {
		t.Errorf("并发峰值 = %d，超过上限 %d", got, limit)
	}
	if got := len(m.forChannel(route).slots); got != 0 {
		t.Errorf("结束后残留并发位 = %d，期望 0", got)
	}
}
