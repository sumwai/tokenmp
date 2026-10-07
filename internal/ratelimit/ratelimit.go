// Package ratelimit 实现渠道级的进程内限流：令牌桶（QPS）与并发位。
//
// 口径：按渠道 id 缓存，同一渠道的所有并发请求共享一个令牌桶与一个并发信号量；
// 两个上限取值 0 都表示该项不限。
//
// 多实例口径：限流状态只存在进程内，多实例部署时每个实例各自按配置放行，同一渠道的
// 总放行量可达实例数 × 配置上限。全局一致需要外部存储与跨实例协调，本包不承担；
// 上游自身的配额反馈仍是对同一渠道的最后一道兜底，两者正交。
package ratelimit

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
)

// defaultMaxWait 是未配置等待上限时等待令牌的最长时间。
const defaultMaxWait = 2 * time.Second

// burstTokens 是令牌桶的容量：只补到「够一次尝试」为止。
//
// 不放大突发行，配置的 QPS 就是稳态速率；需要允许瞬时突发时应当调高 QPS，
// 而不是让网关在启动或空闲后替上游决定一次齐发。
const burstTokens = 1

// waitJitterDivisor 决定抖动幅度：实际等待最多比理论需要多出 need/waitJitterDivisor。
//
// 抖动只增加等待、不放宽速率，因此不会突破配置的 QPS。
const waitJitterDivisor = 4

// Options 是限流器的构造参数。零值字段取对应默认值。
type Options struct {
	// MaxWait 是等待令牌的最长时间；<= 0 时取 defaultMaxWait。
	// 超过该上限仍未取到令牌时按可重试的上游失败返回，由调用方换下一条候选。
	MaxWait time.Duration
	// Clock 取当前时刻；nil 时用 time.Now。注入后令牌回填不依赖真实时间。
	Clock func() time.Time
	// Wait 等待指定时长并支持上下文取消；nil 时用定时器实现。
	// 注入它可在不真实等待的前提下断言等待时长。
	Wait func(ctx context.Context, d time.Duration) error
	// Rand 返回 [0, n) 内的随机数；nil 时用 math/rand/v2 的全局源。
	// 用于给令牌等待加抖动，把同一时刻醒来的并发等待者打散，避免对上游齐发。
	Rand func(n int64) int64
}

// Manager 按渠道 id 缓存限流器，实现 domain.ChannelLimiter。
//
// 渠道上限变更通过下一次 Acquire 整体替换该渠道的限流器来生效：令牌桶速率与
// 信号量容量无法就地改写。替换那一刻新桶重新计数（等价于允许一次新的突发），
// 已在途的请求把并发位还给旧实例后由 GC 回收，不会串到新实例上。
type Manager struct {
	settings settings

	mu       sync.Mutex
	limiters map[uint64]*limiter
}

// 编译期断言：本实现满足流水线消费的限流端口。
var _ domain.ChannelLimiter = (*Manager)(nil)

// settings 是缓存中每个限流器共享的构造参数。
type settings struct {
	maxWait time.Duration
	clock   func() time.Time
	wait    func(context.Context, time.Duration) error
	rand    func(int64) int64
}

// NewManager 构造限流器管理。
func NewManager(opts Options) *Manager {
	return &Manager{
		settings: normalize(opts),
		limiters: make(map[uint64]*limiter),
	}
}

// Acquire 实现 domain.ChannelLimiter：取出该渠道的限流器并获取令牌与并发位。
func (m *Manager) Acquire(ctx context.Context, route domain.Route) (func(), time.Duration, error) {
	return m.forChannel(route).acquire(ctx)
}

// forChannel 返回该渠道的限流器；上限取值变化时整体替换。
func (m *Manager) forChannel(route domain.Route) *limiter {
	qps, concurrency := normalizeLimits(route)
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.limiters[route.ChannelID]; ok && current.matches(qps, concurrency) {
		return current
	}
	created := newLimiter(qps, concurrency, m.settings)
	m.limiters[route.ChannelID] = created
	return created
}

// normalize 归一化构造参数：零值与非法值一律取默认值。
func normalize(opts Options) settings {
	s := settings{
		maxWait: opts.MaxWait,
		clock:   opts.Clock,
		wait:    opts.Wait,
		rand:    opts.Rand,
	}
	if s.maxWait <= 0 {
		s.maxWait = defaultMaxWait
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.wait == nil {
		s.wait = waitWithContext
	}
	if s.rand == nil {
		s.rand = defaultRand
	}
	return s
}

// defaultRand 返回 [0, n) 内的随机数。抖动只用于打散等待时刻，不参与任何安全判据。
//
//nolint:gosec // G404：抖动不用于安全用途。
func defaultRand(n int64) int64 {
	if n <= 1 {
		return 0
	}
	return rand.Int64N(n)
}

// normalizeLimits 把渠道配置的取值收敛为非负整数：负数与 0 同义，都表示不限。
func normalizeLimits(route domain.Route) (qps, concurrency int) {
	return nonNegative(route.RateLimitQPS), nonNegative(route.RateLimitConcurrency)
}

// nonNegative 把负值收敛为 0；存储层承诺非负，这里只是防御脏数据。
func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

// limiter 是单条渠道的进程内限流器。
type limiter struct {
	// qps 为 0 表示不限速率；slots 为 nil 表示不限并发。
	qps   float64
	slots chan struct{}

	maxWait time.Duration
	clock   func() time.Time
	wait    func(context.Context, time.Duration) error
	rand    func(int64) int64

	// mu 保护令牌桶状态；并发位由 slots 的缓冲容量表达，不需要额外锁。
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// newLimiter 构造一条渠道的限流器。
func newLimiter(qps, concurrency int, s settings) *limiter {
	l := &limiter{
		maxWait: s.maxWait,
		clock:   s.clock,
		wait:    s.wait,
		rand:    s.rand,
	}
	if qps > 0 {
		l.qps = float64(qps)
		// 起始满桶：渠道刚进入调度时首个请求立即通过，不为「等第一个令牌」平白加一次等待。
		l.tokens = burstTokens
		l.last = s.clock()
	}
	if concurrency > 0 {
		l.slots = make(chan struct{}, concurrency)
	}
	return l
}

// matches 报告两项上限是否与缓存中的限流器一致。
func (l *limiter) matches(qps, concurrency int) bool {
	if (concurrency > 0) != (l.slots != nil) {
		return false
	}
	if concurrency > 0 && cap(l.slots) != concurrency {
		return false
	}
	return float64(qps) == l.qps
}

// acquire 先占并发位、再取令牌，两者都拿到才算成功。
//
// 顺序固定为并发位在前：并发位是渠道容量，占不到就没有必要消耗令牌；
// 令牌不足时等待超时会把已占的并发位还回去，不把连接挂在网关上。
//
// waited 只累计真正发生等待的时长：立即通过的空路径不产生等待，
// 使「未命中限流」在尝试日志里留下确定的 0，而不是几次 time.Now 的开销。
func (l *limiter) acquire(ctx context.Context) (func(), time.Duration, error) {
	var waited time.Duration
	if l.slots != nil {
		select {
		case l.slots <- struct{}{}:
		default:
			start := l.clock()
			select {
			case l.slots <- struct{}{}:
			case <-ctx.Done():
				return nil, l.clock().Sub(start), ctx.Err()
			}
			waited += l.clock().Sub(start)
		}
	}
	if l.qps > 0 {
		tokenWaited, err := l.waitToken(ctx)
		waited += tokenWaited
		if err != nil {
			l.release()
			return nil, waited, err
		}
	}
	var once sync.Once
	return func() { once.Do(l.release) }, waited, nil
}

// release 归还并发位；未配置并发上限时为空操作。
func (l *limiter) release() {
	if l.slots == nil {
		return
	}
	<-l.slots
}

// waitToken 在等待上限内等一个令牌；返回实际等待的时长。
// 取到令牌返回 nil，超出等待上限返回可重试的上游失败。
func (l *limiter) waitToken(ctx context.Context) (time.Duration, error) {
	var waited time.Duration
	deadline := l.clock().Add(l.maxWait)
	for {
		if l.takeToken() {
			return waited, nil
		}
		need := l.waitForNextToken()
		// 下一次唤醒就会超出等待上限时立即失败，不把最后的等待耗在必然超时的一次上。
		// 带 ClassRateLimit：渠道限流可换下一条候选重试，但不计入渠道健康度
		//（渠道仍然存活，只是令牌暂时用尽）。
		if l.clock().Add(need).After(deadline) {
			return waited, failure.NewError(domain.CodeUpstreamRateLimited, "渠道限流：等待令牌超时",
				fmt.Sprintf("等待上限 %s", l.maxWait), failure.ClassRateLimit)
		}
		start := l.clock()
		if err := l.wait(ctx, need); err != nil {
			return waited + l.clock().Sub(start), err
		}
		waited += l.clock().Sub(start)
	}
}

// takeToken 回填令牌并尝试取走一个；令牌不足返回 false。
func (l *limiter) takeToken() bool {
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refillLocked(now)
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// refillLocked 按流逝的时间回填令牌，上限为 burstTokens；时钟倒退时不回填。
func (l *limiter) refillLocked(now time.Time) {
	elapsed := now.Sub(l.last).Seconds()
	if elapsed <= 0 {
		return
	}
	l.tokens += elapsed * l.qps
	if l.tokens > burstTokens {
		l.tokens = burstTokens
	}
	l.last = now
}

// waitForNextToken 返回距下一个令牌可用还需等待的时长。
//
// 叠加 [0, need/4] 内的正向抖动：抖动只增加等待、不会放宽速率，
// 但能让同一时刻醒来的并发等待者错开，避免它们一起撞向下一次取令牌。
func (l *limiter) waitForNextToken() time.Duration {
	l.mu.Lock()
	deficit := burstTokens - l.tokens
	l.mu.Unlock()
	if deficit <= 0 {
		return time.Nanosecond
	}
	need := time.Duration(deficit / l.qps * float64(time.Second))
	if half := int64(need / waitJitterDivisor); half > 0 {
		need += time.Duration(l.rand(half + 1))
	}
	if need <= 0 {
		// 速率极高导致浮点截断到零时退化为最小等待，避免空转不推进时钟。
		need = time.Nanosecond
	}
	return need
}

// waitWithContext 用定时器等待 d，并在 ctx 取消时立即返回 ctx.Err()，
// 使限流等待不阻塞请求取消的传播。
func waitWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
