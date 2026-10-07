package circuit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
)

// fakeClock 是可推进的虚拟时钟：冷却判定不依赖真实时间，测试不做 sleep。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0).UTC()}
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

// 测试用的上游错误构造器：计入熔断与否由失败分类给出，与错误码无关。
//
// ClassUpstream 带 ActionCountBreaker；请求级与限流虽然也来自上游，
// 但说明渠道仍然存活，不该计入连续失败。
func upstreamUnavailable() error {
	return failure.NewError(domain.CodeUpstreamUnavailable, "上游不可用", "", failure.ClassUpstream)
}

func upstreamTimeout() error {
	return failure.NewError(domain.CodeUpstreamTimeout, "上游超时", "", failure.ClassUpstream)
}

// upstreamRejected 是认不出的 4xx：请求侧问题，换渠道重试无用，也不计入熔断。
func upstreamRejected() error {
	return failure.NewError(domain.CodeUpstreamRejected, "上游拒绝请求", "", failure.ClassRequest)
}

// upstreamRateLimited 是上游限流：可重试但不算渠道故障。
func upstreamRateLimited() error {
	return failure.NewError(domain.CodeUpstreamRateLimited, "上游限流", "", failure.ClassRateLimit)
}

// clientCancelled 是客户端取消在本仓库的口径：上游客户端把它归为平台内部错误。
func clientCancelled() error {
	return domain.NewError(domain.CodeInternal, "上游调用已取消").WithCause(context.Canceled)
}

// 状态机步骤的动作取值。
const (
	actionAllow   = "allow"
	actionRecord  = "record"
	actionAdvance = "advance"
)

// breakerStep 是表驱动状态机的一步：执行一个动作后断言快照状态。
type breakerStep struct {
	action string
	// err 是 record 动作上报的结果；nil 表示成功。
	err error
	// advance 是 advance 动作推进的虚拟时长。
	advance time.Duration
	// wantAllow 是 allow 动作的期望返回值。
	wantAllow bool
	// wantState 与 wantFailures 是动作执行后的期望状态快照。
	wantState    State
	wantFailures int
}

// TestBreakerStateMachine 用表驱动守护三态流转与计数口径。
func TestBreakerStateMachine(t *testing.T) {
	const (
		threshold = 3
		cooldown  = 30 * time.Second
		id        = "channel-1"
	)
	cases := []struct {
		name  string
		steps []breakerStep
	}{
		{
			name: "连续三次上游 5xx 打开熔断并停止放行",
			steps: []breakerStep{
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 1},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateOpen, wantFailures: threshold},
				{action: actionAllow, wantAllow: false, wantState: StateOpen, wantFailures: threshold},
			},
		},
		{
			name: "网络超时与 5xx 混合计数",
			steps: []breakerStep{
				{action: actionRecord, err: upstreamTimeout(), wantState: StateClosed, wantFailures: 1},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: upstreamTimeout(), wantState: StateOpen, wantFailures: threshold},
			},
		},
		{
			name: "成功清零连续失败计数",
			steps: []breakerStep{
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 1},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: nil, wantState: StateClosed, wantFailures: 0},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 1},
			},
		},
		{
			name: "中性结果不打断连续失败计数",
			steps: []breakerStep{
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 1},
				{action: actionRecord, err: upstreamRateLimited(), wantState: StateClosed, wantFailures: 1},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: upstreamRejected(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: clientCancelled(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: errors.New("非统一错误"), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateOpen, wantFailures: threshold},
			},
		},
		{
			name: "客户端取消不计入熔断",
			steps: []breakerStep{
				{action: actionRecord, err: clientCancelled(), wantState: StateClosed, wantFailures: 0},
				{action: actionRecord, err: clientCancelled(), wantState: StateClosed, wantFailures: 0},
				{action: actionRecord, err: clientCancelled(), wantState: StateClosed, wantFailures: 0},
				{action: actionRecord, err: clientCancelled(), wantState: StateClosed, wantFailures: 0},
				{action: actionAllow, wantAllow: true, wantState: StateClosed, wantFailures: 0},
			},
		},
		{
			name: "上游限流不计入熔断",
			steps: []breakerStep{
				{action: actionRecord, err: upstreamRateLimited(), wantState: StateClosed, wantFailures: 0},
				{action: actionRecord, err: upstreamRateLimited(), wantState: StateClosed, wantFailures: 0},
				{action: actionRecord, err: upstreamRateLimited(), wantState: StateClosed, wantFailures: 0},
				{action: actionAllow, wantAllow: true, wantState: StateClosed, wantFailures: 0},
			},
		},
		{
			name: "冷却期未满不放行",
			steps: []breakerStep{
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 1},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateOpen, wantFailures: threshold},
				{action: actionAdvance, advance: cooldown - time.Second, wantState: StateOpen, wantFailures: threshold},
				{action: actionAllow, wantAllow: false, wantState: StateOpen, wantFailures: threshold},
			},
		},
		{
			name: "冷却期已满转半开且只放一笔探测",
			steps: []breakerStep{
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 1},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateOpen, wantFailures: threshold},
				{action: actionAdvance, advance: cooldown, wantState: StateOpen, wantFailures: threshold},
				{action: actionAllow, wantAllow: true, wantState: StateHalfOpen, wantFailures: threshold},
				{action: actionAllow, wantAllow: false, wantState: StateHalfOpen, wantFailures: threshold},
			},
		},
		{
			name: "半开探测成功恢复闭合",
			steps: []breakerStep{
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 1},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateOpen, wantFailures: threshold},
				{action: actionAdvance, advance: cooldown, wantState: StateOpen, wantFailures: threshold},
				{action: actionAllow, wantAllow: true, wantState: StateHalfOpen, wantFailures: threshold},
				{action: actionRecord, err: nil, wantState: StateClosed, wantFailures: 0},
				{action: actionAllow, wantAllow: true, wantState: StateClosed, wantFailures: 0},
			},
		},
		{
			name: "半开探测失败重新打开并重新计时",
			steps: []breakerStep{
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 1},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateOpen, wantFailures: threshold},
				{action: actionAdvance, advance: cooldown, wantState: StateOpen, wantFailures: threshold},
				{action: actionAllow, wantAllow: true, wantState: StateHalfOpen, wantFailures: threshold},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateOpen, wantFailures: threshold},
				{action: actionAdvance, advance: cooldown - time.Second, wantState: StateOpen, wantFailures: threshold},
				{action: actionAllow, wantAllow: false, wantState: StateOpen, wantFailures: threshold},
			},
		},
		{
			name: "半开中性结果释放探测位",
			steps: []breakerStep{
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 1},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateClosed, wantFailures: 2},
				{action: actionRecord, err: upstreamUnavailable(), wantState: StateOpen, wantFailures: threshold},
				{action: actionAdvance, advance: cooldown, wantState: StateOpen, wantFailures: threshold},
				{action: actionAllow, wantAllow: true, wantState: StateHalfOpen, wantFailures: threshold},
				{action: actionAllow, wantAllow: false, wantState: StateHalfOpen, wantFailures: threshold},
				{action: actionRecord, err: upstreamRateLimited(), wantState: StateHalfOpen, wantFailures: threshold},
				{action: actionAllow, wantAllow: true, wantState: StateHalfOpen, wantFailures: threshold},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock()
			breaker := NewBreaker(Options{
				FailureThreshold: threshold,
				Cooldown:         cooldown,
				Clock:            clock.Now,
			})
			for i, step := range tc.steps {
				switch step.action {
				case actionAllow:
					if got := breaker.Allow(id); got != step.wantAllow {
						t.Fatalf("第 %d 步 Allow() = %v，期望 %v", i+1, got, step.wantAllow)
					}
				case actionRecord:
					breaker.Record(id, step.err)
				case actionAdvance:
					clock.Advance(step.advance)
				default:
					t.Fatalf("第 %d 步动作未知: %q", i+1, step.action)
				}
				status := breaker.Status(id)
				if status.State != step.wantState || status.ConsecutiveFailures != step.wantFailures {
					t.Fatalf("第 %d 步状态 = {%s failures=%d}，期望 {%s failures=%d}",
						i+1, status.State, status.ConsecutiveFailures, step.wantState, step.wantFailures)
				}
			}
		})
	}
}

// TestBreakerAllOpenProbeForcesProbeBeforeCooldown 守护「全部候选被拒时放行一个探测」：
// 冷却期未满、Allow 仍拒绝时，Probe 放弃剩余冷却直接放行一次探测；探测成功即闭合。
func TestBreakerAllOpenProbeForcesProbeBeforeCooldown(t *testing.T) {
	const (
		threshold = 2
		cooldown  = time.Minute
		id        = "channel-only"
	)
	clock := newFakeClock()
	breaker := NewBreaker(Options{FailureThreshold: threshold, Cooldown: cooldown, Clock: clock.Now})

	breaker.Record(id, upstreamUnavailable())
	breaker.Record(id, upstreamUnavailable())
	if got := breaker.Status(id).State; got != StateOpen {
		t.Fatalf("状态 = %s，期望打开", got)
	}
	if breaker.Allow(id) {
		t.Fatal("冷却期未满时 Allow() = true，期望 false")
	}

	// 流水线里的唯一候选被 Allow 拒绝后走 Probe：冷却未满也要放行一次探测。
	if !breaker.Probe(id) {
		t.Fatal("Probe() = false，期望放行一次探测")
	}
	if got := breaker.Status(id).State; got != StateHalfOpen {
		t.Fatalf("Probe 后状态 = %s，期望半开", got)
	}
	if breaker.Probe(id) {
		t.Fatal("探测在途时 Probe() = true，期望 false")
	}

	breaker.Record(id, nil)
	if got := breaker.Status(id); got.State != StateClosed || got.ConsecutiveFailures != 0 {
		t.Fatalf("探测成功后状态 = {%s failures=%d}，期望闭合且计数 0", got.State, got.ConsecutiveFailures)
	}
	if !breaker.Allow(id) {
		t.Fatal("探测成功后 Allow() = false，期望恢复放行")
	}
}

// TestBreakerProbeConcurrency 守护探测并发配置：半开态最多同时放行配置条数的探测。
func TestBreakerProbeConcurrency(t *testing.T) {
	const (
		cooldown = time.Minute
		probes   = 2
		id       = "channel-1"
	)
	clock := newFakeClock()
	breaker := NewBreaker(Options{
		FailureThreshold: 1,
		Cooldown:         cooldown,
		ProbeConcurrency: probes,
		Clock:            clock.Now,
	})
	breaker.Record(id, upstreamUnavailable())
	clock.Advance(cooldown)

	for i := 0; i < probes; i++ {
		if !breaker.Allow(id) {
			t.Fatalf("第 %d 条探测未被放行", i+1)
		}
	}
	if breaker.Allow(id) {
		t.Fatalf("探测位已满时仍放行了第 %d 条", probes+1)
	}

	// 一条探测以中性结果结束，释放一个探测位后可以再放行一条。
	breaker.Record(id, upstreamRateLimited())
	if !breaker.Allow(id) {
		t.Fatal("释放探测位后未放行新探测")
	}
}

// TestBreakerProbeTimeoutReleasesChannel 守护探测超时兜底：
// 在途探测超过一个冷却期仍无结果时按丢失处理，放行新的探测，渠道不会被永久困在半开态。
func TestBreakerProbeTimeoutReleasesChannel(t *testing.T) {
	const (
		cooldown = 30 * time.Second
		id       = "channel-1"
	)
	clock := newFakeClock()
	breaker := NewBreaker(Options{FailureThreshold: 1, Cooldown: cooldown, Clock: clock.Now})
	breaker.Record(id, upstreamUnavailable())
	clock.Advance(cooldown)
	if !breaker.Allow(id) {
		t.Fatal("冷却期已满后未放行探测")
	}
	if breaker.Allow(id) {
		t.Fatal("探测在途时放行了第二笔")
	}
	clock.Advance(cooldown - time.Second)
	if breaker.Allow(id) {
		t.Fatal("探测未超时却放行了第二笔")
	}
	clock.Advance(time.Second)
	if !breaker.Allow(id) {
		t.Fatal("探测超时后未放行新的探测")
	}
}

// TestBreakerOpenIgnoresLateReports 守护「打开态忽略迟到回报」：
// 熔断打开前已发出的请求在打开后到达的成功回报不得把熔断立刻关掉。
func TestBreakerOpenIgnoresLateReports(t *testing.T) {
	const id = "channel-1"
	clock := newFakeClock()
	breaker := NewBreaker(Options{FailureThreshold: 2, Cooldown: time.Minute, Clock: clock.Now})
	breaker.Record(id, upstreamUnavailable())
	breaker.Record(id, upstreamUnavailable())
	if got := breaker.Status(id).State; got != StateOpen {
		t.Fatalf("状态 = %s，期望打开", got)
	}
	breaker.Record(id, nil)
	if got := breaker.Status(id).State; got != StateOpen {
		t.Fatalf("迟到成功回报后状态 = %s，期望仍打开", got)
	}
	if breaker.Allow(id) {
		t.Fatal("打开态 Allow() = true，期望 false")
	}
}

// TestBreakerEmptyUpstreamIDAlwaysAllowed 守护「无渠道标识不参与熔断」：
// 空 upstreamID 恒放行且不建档，避免把无标识流量误伤到同一个虚拟渠道上。
func TestBreakerEmptyUpstreamIDAlwaysAllowed(t *testing.T) {
	breaker := NewBreaker(Options{FailureThreshold: 1})
	for i := 0; i < 3; i++ {
		breaker.Record("", upstreamUnavailable())
		if !breaker.Allow("") {
			t.Fatalf("第 %d 次对空 upstreamID 的 Allow() = false，期望恒为 true", i+1)
		}
		if !breaker.Probe("") {
			t.Fatalf("第 %d 次对空 upstreamID 的 Probe() = false，期望恒为 true", i+1)
		}
	}
	if got := breaker.Status("").ConsecutiveFailures; got != 0 {
		t.Fatalf("空 upstreamID 失败计数 = %d，期望 0", got)
	}
}

// TestBreakerDefaults 守护零值参数取默认阈值与默认冷却期。
func TestBreakerDefaults(t *testing.T) {
	clock := newFakeClock()
	breaker := NewBreaker(Options{Clock: clock.Now})
	const id = "channel-default"
	for i := 1; i < defaultFailureThreshold; i++ {
		breaker.Record(id, upstreamUnavailable())
		if got := breaker.Status(id).State; got != StateClosed {
			t.Fatalf("第 %d 次失败后状态 = %s，期望闭合", i, got)
		}
	}
	breaker.Record(id, upstreamUnavailable())
	if got := breaker.Status(id).State; got != StateOpen {
		t.Fatalf("第 %d 次失败后状态 = %s，期望打开", defaultFailureThreshold, got)
	}
	if breaker.Allow(id) {
		t.Fatal("默认冷却期未满时 Allow() = true，期望 false")
	}
	clock.Advance(defaultCooldown)
	if !breaker.Allow(id) {
		t.Fatal("默认冷却期满后 Allow() = false，期望放行一笔探测")
	}
}

// TestBreakerStatusIsReadOnly 守护只读状态查询：查询未登记渠道返回闭合态且不新建状态行。
func TestBreakerStatusIsReadOnly(t *testing.T) {
	breaker := NewBreaker(Options{})
	unknown := breaker.Status("not-registered")
	if unknown.State != StateClosed || unknown.ConsecutiveFailures != 0 {
		t.Fatalf("未登记渠道快照 = %+v，期望闭合且失败计数为 0", unknown)
	}
	breaker.Record("channel-b", upstreamUnavailable())
	breaker.Record("channel-c", upstreamUnavailable())
	breaker.Record("channel-c", nil)
	if got := breaker.Status("channel-c"); got.State != StateClosed || got.ConsecutiveFailures != 0 {
		t.Fatalf("成功清零后快照 = %+v，期望闭合且计数 0", got)
	}
}

// TestBreakerLogsStateTransitions 守护状态迁移写入结构化日志：
// 打开、半开与恢复闭合各留下一条记录，字段含渠道标识与迁移后的状态取值。
func TestBreakerLogsStateTransitions(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	clock := newFakeClock()
	breaker := NewBreaker(Options{
		FailureThreshold: 1,
		Cooldown:         time.Minute,
		Clock:            clock.Now,
		Logger:           logger,
	})
	const id = "channel-1"

	breaker.Record(id, upstreamUnavailable())
	clock.Advance(time.Minute)
	if !breaker.Allow(id) {
		t.Fatal("冷却期已满后未放行探测")
	}
	breaker.Record(id, nil)

	logs := buf.String()
	for _, want := range []string{
		`"upstream_id":"channel-1"`,
		`"to":"open"`,
		`"to":"half_open"`,
		`"to":"closed"`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("状态迁移日志缺少 %s：%s", want, logs)
		}
	}
}

// TestBreakerConcurrentAccess 用并发读写配合竞态检测器守护线程安全：
// Allow、Probe、Record 与 Status 在多个渠道上并发交错，结束后状态必须落在合法区间。
func TestBreakerConcurrentAccess(t *testing.T) {
	breaker := NewBreaker(Options{FailureThreshold: 3, Cooldown: time.Millisecond})
	const (
		goroutines = 16
		iterations = 200
		channels   = 4
	)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			id := fmt.Sprintf("channel-%d", g%channels)
			for i := 0; i < iterations; i++ {
				switch i % 5 {
				case 0:
					breaker.Allow(id)
				case 1:
					breaker.Probe(id)
				case 2:
					breaker.Record(id, upstreamUnavailable())
				case 3:
					breaker.Record(id, nil)
				default:
					breaker.Status(id)
				}
			}
		}(g)
	}
	wg.Wait()

	for i := 0; i < channels; i++ {
		status := breaker.Status(fmt.Sprintf("channel-%d", i))
		switch status.State {
		case StateClosed, StateOpen, StateHalfOpen:
		default:
			t.Errorf("渠道 %q 收敛到非法状态 %q", status.UpstreamID, status.State)
		}
		if status.ConsecutiveFailures < 0 || status.ConsecutiveFailures > 3 {
			t.Errorf("渠道 %q 失败计数 = %d，超出 [0, 3]", status.UpstreamID, status.ConsecutiveFailures)
		}
	}
}
