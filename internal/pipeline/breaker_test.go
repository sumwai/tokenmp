package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/circuit"
	"github.com/sumwai/tokenmp/internal/domain"
)

// 本文件覆盖流水线与渠道熔断端口的接线：熔断过滤、全部打开时的探测兜底、
// 以及熔断与限流、凭据冷却两种机制的叠加是否互不干扰。
//
// 熔断器自身的三态流转在 internal/circuit 覆盖；这里用替身关注流水线侧的动作与顺序，
// 组合用例才注入真实熔断器。

// breakerRecord 是一次上报给熔断器的结果。
type breakerRecord struct {
	upstreamID string
	err        error
}

// fakeBreaker 是熔断端口的测试替身：按渠道标识给出拒绝集合，并记录上报与探测请求。
type fakeBreaker struct {
	mu      sync.Mutex
	denied  map[string]bool
	records []breakerRecord
	probes  []string
	probeOK bool
}

func (b *fakeBreaker) Allow(upstreamID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.denied[upstreamID]
}

func (b *fakeBreaker) Record(upstreamID string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records = append(b.records, breakerRecord{upstreamID: upstreamID, err: err})
}

func (b *fakeBreaker) Probe(upstreamID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probes = append(b.probes, upstreamID)
	return b.probeOK
}

func (b *fakeBreaker) snapshotRecords() []breakerRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]breakerRecord(nil), b.records...)
}

func (b *fakeBreaker) snapshotProbes() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.probes...)
}

// blockingBreaker 只实现核心熔断端口，不提供 Probe 能力。
type blockingBreaker struct {
	denied map[string]bool
}

func (b blockingBreaker) Allow(upstreamID string) bool { return !b.denied[upstreamID] }
func (b blockingBreaker) Record(string, error)         {}

// recordedCaller 记录每次上游调用命中的渠道标识，并按 channelID 给出结果。
type recordedCaller struct {
	mu     sync.Mutex
	calls  []string
	result func(route domain.Route) (*domain.UpstreamResult, error)
}

func (c *recordedCaller) Complete(_ context.Context, route domain.Route, _ *domain.Request, _ []byte) (*domain.UpstreamResult, error) {
	c.mu.Lock()
	c.calls = append(c.calls, route.UpstreamID)
	c.mu.Unlock()
	return c.result(route)
}

func (c *recordedCaller) Stream(context.Context, domain.Route, *domain.Request, []byte, domain.ChunkSink) error {
	return nil
}

func (c *recordedCaller) snapshotCalls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// successResult 是一份最小的成功上游结果。
func successResult() *domain.UpstreamResult {
	return &domain.UpstreamResult{Raw: []byte(`{"ok":true}`), Response: &domain.Response{}}
}

// TestForwardSkipsOpenChannelAndRecordsSkip 断言打开态候选在候选阶段被过滤：
// 请求由后续健康候选服务，且被跳过的候选在尝试记录里留下 skipped 结果。
func TestForwardSkipsOpenChannelAndRecordsSkip(t *testing.T) {
	breaker := &fakeBreaker{denied: map[string]bool{"10": true}}
	caller := &recordedCaller{result: func(domain.Route) (*domain.UpstreamResult, error) {
		return successResult(), nil
	}}
	observer := &recordingObserver{}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRouteWithID(10), chatRouteWithID(20)}},
		Observer: observer,
		Breaker:  breaker,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	if calls := caller.snapshotCalls(); len(calls) != 1 || calls[0] != "20" {
		t.Fatalf("上游调用 = %v，期望只打到渠道 20", calls)
	}
	records := observer.snapshot()
	if len(records) != 2 {
		t.Fatalf("尝试记录数 = %d，期望 2（1 条跳过 + 1 条成功）", len(records))
	}
	if records[0].Outcome != domain.AttemptSkipped || records[0].UpstreamID != "10" {
		t.Errorf("首条记录 = {%s %s}，期望渠道 10 被跳过", records[0].Outcome, records[0].UpstreamID)
	}
	if records[1].Outcome != domain.AttemptOK || records[1].UpstreamID != "20" {
		t.Errorf("次条记录 = {%s %s}，期望渠道 20 成功", records[1].Outcome, records[1].UpstreamID)
	}
	reported := breaker.snapshotRecords()
	if len(reported) != 1 || reported[0].upstreamID != "20" || reported[0].err != nil {
		t.Fatalf("熔断上报 = %+v，期望只有渠道 20 的成功", reported)
	}
}

// TestForwardAllCandidatesOpenProbesFirst 断言全部候选被熔断时放行第一条候选做探测，
// 而不是直接返回失败。
func TestForwardAllCandidatesOpenProbesFirst(t *testing.T) {
	breaker := &fakeBreaker{denied: map[string]bool{"10": true, "20": true}, probeOK: true}
	caller := &recordedCaller{result: func(domain.Route) (*domain.UpstreamResult, error) {
		return successResult(), nil
	}}
	observer := &recordingObserver{}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRouteWithID(10), chatRouteWithID(20)}},
		Observer: observer,
		Breaker:  breaker,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err != nil {
		t.Fatalf("全部候选熔断时应放行探测，实际错误：%v", err)
	}
	if calls := caller.snapshotCalls(); len(calls) != 1 || calls[0] != "10" {
		t.Fatalf("上游调用 = %v，期望只探测第一条候选 10", calls)
	}
	if probes := breaker.snapshotProbes(); len(probes) != 1 || probes[0] != "10" {
		t.Fatalf("探测请求 = %v，期望只对渠道 10 一次", probes)
	}
}

// TestForwardAllCandidatesOpenWithoutProberFailsClosed 断言熔断器未提供探测能力时
// 维持既有的失败封闭：全部候选被拒即返回上游不可用，不发起任何上游调用。
func TestForwardAllCandidatesOpenWithoutProberFailsClosed(t *testing.T) {
	breaker := blockingBreaker{denied: map[string]bool{"10": true, "20": true}}
	calls := 0
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		calls++
		return successResult(), nil
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRouteWithID(10), chatRouteWithID(20)}},
		Breaker:  breaker,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	err = p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}})
	if err == nil {
		t.Fatal("全部候选被熔断时应返回错误")
	}
	domainErr := domain.AsError(err)
	if domainErr == nil || domainErr.Code != domain.CodeUpstreamUnavailable {
		t.Fatalf("错误 = %v，期望错误码 %s", err, domain.CodeUpstreamUnavailable)
	}
	if calls != 0 {
		t.Fatalf("上游调用 = %d，期望 0", calls)
	}
}

// TestForwardRecordsChannelOutcomeAfterCredentialRotation 断言熔断按渠道尝试的最终结果上报：
// 渠道内的凭据类失败不单独计入，凭据轮换后成功只上报一次成功。
func TestForwardRecordsChannelOutcomeAfterCredentialRotation(t *testing.T) {
	breaker := &fakeBreaker{}
	calls := 0
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		calls++
		if calls == 1 {
			return nil, credentialRejectedError{}
		}
		return successResult(), nil
	}}
	rotation := &fakeRotation{remaining: 1}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		Credentials: rotation,
		Breaker:     breaker,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	if calls != 2 {
		t.Fatalf("上游调用 = %d，期望 2（凭据轮换一次）", calls)
	}
	reported := breaker.snapshotRecords()
	if len(reported) != 1 || reported[0].err != nil {
		t.Fatalf("熔断上报 = %+v，期望只有一次成功（中间凭据失败不单独上报）", reported)
	}
}

// TestForwardBreakerNeutralToRateLimit 断言熔断与渠道限流正交：
// 限流等待超时虽可换候选重试，但它不是渠道故障，连续多次也不会打开熔断。
func TestForwardBreakerNeutralToRateLimit(t *testing.T) {
	breaker := circuit.NewBreaker(circuit.Options{FailureThreshold: 1, Cooldown: time.Minute})
	limiter := &fakeLimiter{replies: map[uint64]limiterReply{
		10: {err: domain.NewError(domain.CodeUpstreamRateLimited, "渠道限流：等待令牌超时")},
	}}
	caller := fakeCaller{complete: func(_ context.Context, route domain.Route, _ *domain.Request, _ []byte) (*domain.UpstreamResult, error) {
		if route.ChannelID == 10 {
			t.Error("被限流的渠道不应发起上游调用")
		}
		return successResult(), nil
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRouteWithID(10), chatRouteWithID(20)}},
		Limiter:  limiter,
		Breaker:  breaker,
		Backoff:  fastBackoff(),
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	for i := 0; i < 3; i++ {
		if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err != nil {
			t.Fatalf("第 %d 次转发失败：%v", i+1, err)
		}
	}
	if !breaker.Allow("10") {
		t.Fatal("连续限流后渠道 10 被熔断：限流不应计入渠道故障")
	}
	if got := breaker.Status("10").ConsecutiveFailures; got != 0 {
		t.Fatalf("渠道 10 失败计数 = %d，期望 0", got)
	}
}

// TestForwardBreakerNeutralToCredentialRejection 断言熔断与凭据冷却正交：
// 凭据不被接受是凭据层事实，换下一组凭据可能成功，不计入渠道健康度。
func TestForwardBreakerNeutralToCredentialRejection(t *testing.T) {
	breaker := circuit.NewBreaker(circuit.Options{FailureThreshold: 1, Cooldown: time.Minute})
	rejection := wrappedCredentialRejection{err: domain.NewError(domain.CodeUpstreamRejected, "上游拒绝请求")}
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		return nil, rejection
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRouteWithID(10)}},
		Breaker:  breaker,
		Backoff:  fastBackoff(),
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	for i := 0; i < 3; i++ {
		if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err == nil {
			t.Fatalf("第 %d 次转发期望失败", i+1)
		}
	}
	if !breaker.Allow("10") {
		t.Fatal("连续凭据被拒后渠道 10 被熔断：凭据类失败不应计入渠道故障")
	}
	if got := breaker.Status("10").ConsecutiveFailures; got != 0 {
		t.Fatalf("渠道 10 失败计数 = %d，期望 0", got)
	}
}

// wrappedCredentialRejection 模拟上游客户端在统一错误之上标注「凭据不被接受」的包装错误。
type wrappedCredentialRejection struct {
	err error
}

func (e wrappedCredentialRejection) Error() string            { return e.err.Error() }
func (e wrappedCredentialRejection) Unwrap() error            { return e.err }
func (e wrappedCredentialRejection) CredentialRejected() bool { return true }
