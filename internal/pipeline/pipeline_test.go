package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/anthropic"
	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
)

// 本文件覆盖用量落库的时序与终态判定。
//
// 用真实适配器 + 假上游/假选路/假记账：时序断言只关心「记账发生在哪个动作之前 / 之后」，
// 协议编解码细节不属于本文件的目标，复用真实适配器可以避免另造一套假实现。

// errRetryable 是一次可换渠道重试的上游失败。
// errRetryable 是可换渠道重试的上游故障：类别带换渠道动作与熔断计数。
var errRetryable = failure.NewError(domain.CodeUpstreamUnavailable, "上游不可用", "", failure.ClassUpstream)

// fakeRouteResolver 按声明顺序返回固定候选。
type fakeRouteResolver struct {
	routes []domain.Route
}

func (r fakeRouteResolver) Candidates(context.Context, *domain.Request) ([]domain.Route, error) {
	return r.routes, nil
}

// fakeCaller 是 domain.UpstreamCaller 的假实现。
type fakeCaller struct {
	complete func(ctx context.Context, route domain.Route, req *domain.Request, body []byte) (*domain.UpstreamResult, error)
	stream   func(ctx context.Context, route domain.Route, req *domain.Request, body []byte, sink domain.ChunkSink) error
}

func (c fakeCaller) Complete(ctx context.Context, route domain.Route, req *domain.Request, body []byte) (*domain.UpstreamResult, error) {
	if c.complete == nil {
		return nil, errors.New("测试未设置 Complete")
	}
	return c.complete(ctx, route, req, body)
}

func (c fakeCaller) Stream(ctx context.Context, route domain.Route, req *domain.Request, body []byte, sink domain.ChunkSink) error {
	if c.stream == nil {
		return errors.New("测试未设置 Stream")
	}
	return c.stream(ctx, route, req, body, sink)
}

// recordingRecorder 记录每次记账调用，并把事件名追加到共享时间线。
type recordingRecorder struct {
	mu      sync.Mutex
	events  *[]string
	records []domain.UsageRecord
}

func (r *recordingRecorder) RecordUsage(_ context.Context, rec domain.UsageRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events != nil {
		*r.events = append(*r.events, "usage")
	}
	r.records = append(r.records, rec)
	return nil
}

func (r *recordingRecorder) snapshot() []domain.UsageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.UsageRecord(nil), r.records...)
}

// recordingWriter 记录每次向客户端写出，并把事件名追加到共享时间线。
type recordingWriter struct {
	events *[]string
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	*w.events = append(*w.events, "write")
	return len(p), nil
}

// chatAdapterLookup 返回一个只认 openai_chat 的适配器查找函数。
func chatAdapterLookup(adapter domain.Adapter) AdapterLookup {
	return func(protocol domain.Protocol) (domain.Adapter, error) {
		if protocol != domain.ProtocolOpenAIChat {
			return nil, domain.NewError(domain.CodeInternal, "测试只提供 openai_chat 适配器")
		}
		return adapter, nil
	}
}

// newChatRequest 构造一个可解码的最小 Chat Completions 请求。
func newChatRequest(stream bool) *domain.Request {
	return &domain.Request{
		RequestID: "req-1",
		Protocol:  domain.ProtocolOpenAIChat,
		Model:     "alias",
		Messages: []domain.Message{{
			Role:  domain.RoleUser,
			Parts: []domain.Part{{Kind: domain.PartText, Text: "hi"}},
		}},
		Stream:  stream,
		RawBody: []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}
}

// chatRoute 返回一条同协议的上游候选。
func chatRoute() domain.Route {
	return domain.Route{
		ChannelID:     7,
		UpstreamID:    "7",
		Protocol:      domain.ProtocolOpenAIChat,
		UpstreamModel: "up-model",
		BaseURL:       "http://upstream.test",
	}
}

// chatRouteWithID 返回指定渠道 id 的同协议候选。
func chatRouteWithID(id uint64) domain.Route {
	route := chatRoute()
	route.ChannelID = id
	route.UpstreamID = strconv.FormatUint(id, 10)
	return route
}

// crossProtocolRouteWithID 返回指定渠道 id 的跨协议候选：客户端说 openai_chat、上游说 anthropic。
func crossProtocolRouteWithID(id uint64) domain.Route {
	route := chatRouteWithID(id)
	route.Protocol = domain.ProtocolAnthropicMessages
	return route
}

// TestNewSinkCarriesMiddlewareLogger 守护中间件留痕出口注入到透传下沉目标。
//
// 检测点只在流水线（混帧那里的中间件未介入只在透传下沉目标内可见），注入链断在装配处
// 整条就失效；未注入时不留痕，透传行为与既有实现逐字节相同。
func TestNewSinkCarriesMiddlewareLogger(t *testing.T) {
	newPipeline := func(opts Options) *Pipeline {
		t.Helper()
		opts.Adapters = chatAdapterLookup(openaichat.New())
		opts.Upstream = fakeCaller{}
		opts.Routes = fakeRouteResolver{}
		pipeline, err := New(opts)
		if err != nil {
			t.Fatalf("构造流水线失败：%v", err)
		}
		return pipeline
	}
	passthroughOf := func(p *Pipeline) *passthroughSink {
		t.Helper()
		sink, err := p.newSink(newChatRequest(true), chatRoute(), io.Discard, openaichat.New())
		if err != nil {
			t.Fatalf("构造下沉目标失败：%v", err)
		}
		passthrough, ok := sink.(*passthroughSink)
		if !ok {
			t.Fatalf("同协议候选应走透传下沉目标，实际 %T", sink)
		}
		return passthrough
	}

	if sink := passthroughOf(newPipeline(Options{
		MiddlewareLogger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})); sink.mixedFrames == nil {
		t.Fatal("注入中间件日志后，透传下沉目标应带留痕出口")
	}
	if sink := passthroughOf(newPipeline(Options{})); sink.mixedFrames != nil {
		t.Fatal("未注入中间件日志时不应带留痕出口")
	}
}

// TestForwardRecordsUsageBeforeClientWrite 断言非流式路径「先落 billing_usage、再回写客户端」。
//
// 流水是扣费与对账的事实来源，客户端拿到响应时它必须已经存在；
// 用事件时间线验证记账调用发生在第一次写客户端之前。
func TestForwardRecordsUsageBeforeClientWrite(t *testing.T) {
	events := []string{}
	usage := domain.Usage{Source: domain.UsageSourceUpstream, InputTokens: 11, OutputTokens: 4}
	recorder := &recordingRecorder{events: &events}
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		return &domain.UpstreamResult{Raw: []byte(`{"ok":true}`), Response: &domain.Response{Usage: usage}}, nil
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		Usage:    recorder,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &events}); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	if want := []string{"usage", "write"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("事件顺序 = %v，期望 %v", events, want)
	}
	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("记账次数 = %d，期望 1", len(records))
	}
	if records[0].ChannelID != 7 || records[0].Model != "up-model" {
		t.Errorf("归属 = channel %d model %q，期望 7 / up-model", records[0].ChannelID, records[0].Model)
	}
	if records[0].Usage.InputTokens != 11 || records[0].Usage.OutputTokens != 4 {
		t.Errorf("用量 = %+v，期望输入 11 输出 4", records[0].Usage)
	}
}

// TestForwardStreamRecordsUsageAfterStream 断言流式路径在流结束后落库，
// 且落在上游流产生的用量上。
func TestForwardStreamRecordsUsageAfterStream(t *testing.T) {
	usage := domain.Usage{Source: domain.UsageSourceUpstream, InputTokens: 5, OutputTokens: 3}
	recorder := &recordingRecorder{}
	caller := fakeCaller{stream: func(ctx context.Context, _ domain.Route, _ *domain.Request, _ []byte, sink domain.ChunkSink) error {
		return sink.Send(ctx, domain.Chunk{Kind: domain.ChunkStreamEnd, Usage: &usage})
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		Usage:    recorder,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	events := []string{}
	if err := p.Forward(context.Background(), adapter, newChatRequest(true), &recordingWriter{events: &events}); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("记账次数 = %d，期望 1", len(records))
	}
	if records[0].Usage.InputTokens != 5 || records[0].Usage.OutputTokens != 3 {
		t.Errorf("用量 = %+v，期望输入 5 输出 3", records[0].Usage)
	}
}

// attemptInfoWriter 记录流水线回流的渠道 id 与上游状态码，同时保留写出事件。
type attemptInfoWriter struct {
	events         *[]string
	channelID      uint64
	upstreamStatus int
	crossProtocol  bool
}

func (w *attemptInfoWriter) Write([]byte) (int, error) {
	*w.events = append(*w.events, "write")
	return 0, nil
}

func (w *attemptInfoWriter) SetAttemptChannel(channelID uint64) { w.channelID = channelID }

func (w *attemptInfoWriter) SetUpstreamStatus(status int) { w.upstreamStatus = status }

func (w *attemptInfoWriter) SetCrossProtocol(cross bool) { w.crossProtocol = cross }

// statusCarrierError 是携带上游 HTTP 状态码的测试错误，模拟上游客户端包在错误上的能力。
type statusCarrierError struct {
	err    error
	status int
}

func (e statusCarrierError) Error() string       { return e.err.Error() }
func (e statusCarrierError) Unwrap() error       { return e.err }
func (e statusCarrierError) UpstreamStatus() int { return e.status }

// TestForwardReportsAttemptInfoToSink 断言成功的非流式尝试把渠道 id 与上游状态码回流。
func TestForwardReportsAttemptInfoToSink(t *testing.T) {
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		return &domain.UpstreamResult{Raw: []byte(`{"ok":true}`), Response: &domain.Response{}}, nil
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRoute()}},
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}
	events := []string{}
	out := &attemptInfoWriter{events: &events}
	if err := p.Forward(context.Background(), adapter, newChatRequest(false), out); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	if out.channelID != 7 {
		t.Errorf("渠道 id = %d，期望 7", out.channelID)
	}
	if out.upstreamStatus != http.StatusOK {
		t.Errorf("上游状态码 = %d，期望 200", out.upstreamStatus)
	}
	if out.crossProtocol {
		t.Error("同协议尝试不应标记 cross_protocol")
	}
}

// crossProtocolAdapterLookup 返回客户端与上游两类协议各自的适配器。
func crossProtocolAdapterLookup() AdapterLookup {
	return func(protocol domain.Protocol) (domain.Adapter, error) {
		switch protocol {
		case domain.ProtocolOpenAIChat:
			return openaichat.New(), nil
		case domain.ProtocolAnthropicMessages:
			return anthropic.New(), nil
		default:
			return nil, domain.NewError(domain.CodeInternal, "测试未提供该协议的适配器")
		}
	}
}

// TestForwardReportsCrossProtocolToSink 断言跨协议尝试把 cross_protocol 回流到写出目标，
// 并写进尝试记录：客户端说 chat、上游渠道说 anthropic，入口层据此在访问日志里标记降级。
func TestForwardReportsCrossProtocolToSink(t *testing.T) {
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		return &domain.UpstreamResult{Raw: []byte(`{}`), Response: &domain.Response{}}, nil
	}}
	route := chatRoute()
	route.Protocol = domain.ProtocolAnthropicMessages
	observer := &recordingObserver{}
	p, err := New(Options{
		Adapters: crossProtocolAdapterLookup(),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{route}},
		Observer: observer,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}
	events := []string{}
	out := &attemptInfoWriter{events: &events}
	if err := p.Forward(context.Background(), openaichat.New(), newChatRequest(false), out); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	if !out.crossProtocol {
		t.Error("跨协议尝试应标记 cross_protocol")
	}
	if out.channelID != 7 {
		t.Errorf("渠道 id = %d，期望 7", out.channelID)
	}
	records := observer.snapshot()
	if len(records) != 1 {
		t.Fatalf("尝试记录数 = %d，期望 1", len(records))
	}
	if !records[0].CrossProtocol || records[0].UpstreamProtocol != domain.ProtocolAnthropicMessages {
		t.Errorf("尝试记录应标记跨协议与上游协议，得到 %+v", records[0])
	}
	if len(records[0].RewrittenParts) == 0 || !records[0].RewrittenParts.Has(domain.RewritePartResponseReencoded) {
		t.Errorf("跨协议非流式响应应标注 response_reencoded，得到 %v", records[0].RewrittenParts)
	}
}

// TestForwardReportsUpstreamStatusOnFailure 断言失败尝试把上游返回的状态码回流。
func TestForwardReportsUpstreamStatusOnFailure(t *testing.T) {
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		return nil, statusCarrierError{err: errRetryable, status: http.StatusTooManyRequests}
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}
	events := []string{}
	out := &attemptInfoWriter{events: &events}
	if err := p.Forward(context.Background(), adapter, newChatRequest(false), out); err == nil {
		t.Fatal("上游失败时应返回错误")
	}
	if out.channelID != 7 {
		t.Errorf("渠道 id = %d，期望 7", out.channelID)
	}
	if out.upstreamStatus != http.StatusTooManyRequests {
		t.Errorf("上游状态码 = %d，期望 429", out.upstreamStatus)
	}
}

// TestForwardStreamRetriedAttemptProducesNoUsage 断言流式可重试失败换渠道后只留一行流水。
//
// 第一次尝试还没向客户端写出字节就失败，属可重试；它不产生流水。第二次成功才落库。
func TestForwardStreamRetriedAttemptProducesNoUsage(t *testing.T) {
	usage := domain.Usage{Source: domain.UsageSourceUpstream, InputTokens: 9, OutputTokens: 2}
	recorder := &recordingRecorder{}
	attempts := 0
	caller := fakeCaller{stream: func(ctx context.Context, _ domain.Route, _ *domain.Request, _ []byte, sink domain.ChunkSink) error {
		attempts++
		if attempts == 1 {
			return errRetryable
		}
		return sink.Send(ctx, domain.Chunk{Kind: domain.ChunkStreamEnd, Usage: &usage})
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRoute(), chatRoute()}},
		Usage:       recorder,
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	events := []string{}
	if err := p.Forward(context.Background(), adapter, newChatRequest(true), &recordingWriter{events: &events}); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	if attempts != 2 {
		t.Fatalf("上游尝试次数 = %d，期望 2", attempts)
	}
	if records := recorder.snapshot(); len(records) != 1 {
		t.Fatalf("记账次数 = %d，期望 1（重试的失败尝试不产生流水）", len(records))
	}
}

// TestForwardTruncatedStreamRecordsCollectedUsage 断言流被截断且已向客户端写出字节时，
// 仍把已收集到的用量落成流水：该尝试没有重试机会，它就是本次请求的结果。
func TestForwardTruncatedStreamRecordsCollectedUsage(t *testing.T) {
	usage := domain.Usage{Source: domain.UsageSourceUpstream, InputTokens: 6, OutputTokens: 1}
	recorder := &recordingRecorder{}
	caller := fakeCaller{stream: func(ctx context.Context, _ domain.Route, _ *domain.Request, _ []byte, sink domain.ChunkSink) error {
		frameSink, ok := sink.(domain.FrameSink)
		if !ok {
			t.Fatal("同协议透传路径的下沉目标应实现 FrameSink")
		}
		if err := frameSink.SendFrame(ctx, []byte("data: x\n\n"), []domain.Chunk{{Kind: domain.ChunkUsage, Usage: &usage}}); err != nil {
			return err
		}
		return errRetryable
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		Usage:    recorder,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	events := []string{}
	if err := p.Forward(context.Background(), adapter, newChatRequest(true), &recordingWriter{events: &events}); err == nil {
		t.Fatal("截断的流应返回错误")
	}
	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("记账次数 = %d，期望 1", len(records))
	}
	if records[0].Usage.InputTokens != 6 || records[0].Usage.OutputTokens != 1 {
		t.Errorf("用量 = %+v，期望输入 6 输出 1", records[0].Usage)
	}
}

// fakeRotation 是 domain.CredentialRotation 的测试替身。
//
// 只对凭据类失败切换，且切换次数由 remaining 限定：这样能同时断言
// 「非凭据类失败不切换」与「组内用尽后本次渠道尝试失败」。
type fakeRotation struct {
	remaining int
	begun     int
	advanced  int
	renewed   int
}

func (r *fakeRotation) Begin(ctx context.Context, _ domain.Route) context.Context {
	r.begun++
	return ctx
}

func (r *fakeRotation) Advance(ctx context.Context, _ domain.Route, cause error) (context.Context, bool) {
	if r.remaining <= 0 || !failure.ActionsOf(cause).Has(failure.ActionRetryNextAccount) {
		return ctx, false
	}
	r.remaining--
	r.advanced++
	return ctx, true
}

func (r *fakeRotation) Renew(context.Context, domain.Route) { r.renewed++ }

// credentialRejectedError 模拟上游客户端标注「本次凭据不被接受」的错误。
//
// 动作集里同时含换渠道：额度与余额类失败在组内凭据用尽后应当换候选渠道，
// 只有换凭据会让「候选渠道都试一遍」的断言失去意义。
type credentialRejectedError struct{}

func (credentialRejectedError) Error() string { return "凭据被拒绝" }

func (credentialRejectedError) Actions() failure.Action {
	return failure.ActionSuspendAccount | failure.ActionRetryNextAccount | failure.ActionRetryNextRoute
}

// TestForwardSwitchesCredentialWithinAttempt 断言凭据类失败在同一次渠道尝试内切换，
// 且不消耗换渠道的尝试预算。
//
// MaxAttempts 固定为 1：若凭据切换消耗预算，第二次上游调用就发不出去，本用例会失败。
func TestForwardSwitchesCredentialWithinAttempt(t *testing.T) {
	usage := domain.Usage{Source: domain.UsageSourceUpstream, InputTokens: 7, OutputTokens: 2}
	recorder := &recordingRecorder{}
	calls := 0
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		calls++
		if calls == 1 {
			return nil, credentialRejectedError{}
		}
		return &domain.UpstreamResult{Raw: []byte(`{"ok":true}`), Response: &domain.Response{Usage: usage}}, nil
	}}
	rotation := &fakeRotation{remaining: 1}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		Usage:       recorder,
		Credentials: rotation,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err != nil {
		t.Fatalf("切换后应成功：%v", err)
	}
	if calls != 2 {
		t.Fatalf("上游调用次数 = %d，期望 2", calls)
	}
	if rotation.begun != 1 || rotation.advanced != 1 {
		t.Fatalf("轮换状态 = begin %d / advance %d，期望 1 / 1", rotation.begun, rotation.advanced)
	}
	if records := recorder.snapshot(); len(records) != 1 {
		t.Fatalf("记账次数 = %d，期望 1（中间失败尝试不产生流水）", len(records))
	}
}

// TestForwardCredentialExhaustedFallsBackToNextChannel 断言组内凭据试遍后
// 本次渠道尝试失败，并走渠道回退。
func TestForwardCredentialExhaustedFallsBackToNextChannel(t *testing.T) {
	calls := 0
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		calls++
		return nil, credentialRejectedError{}
	}}
	rotation := &fakeRotation{remaining: 0}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRoute(), chatRoute()}},
		Credentials: rotation,
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err == nil {
		t.Fatal("组内凭据全部失败时应返回错误")
	}
	if calls != 2 {
		t.Fatalf("上游调用次数 = %d，期望 2（每条渠道各一次）", calls)
	}
}

// TestForwardNonCredentialFailureDoesNotSwitchCredential 断言非凭据类失败不触发切换。
func TestForwardNonCredentialFailureDoesNotSwitchCredential(t *testing.T) {
	calls := 0
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		calls++
		return nil, errRetryable
	}}
	rotation := &fakeRotation{remaining: 3}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRoute(), chatRoute()}},
		Credentials: rotation,
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err == nil {
		t.Fatal("可重试失败换渠道后仍失败，应返回错误")
	}
	if calls != 2 {
		t.Fatalf("上游调用次数 = %d，期望 2（每渠道一次，不因非凭据类失败换凭据）", calls)
	}
	if rotation.advanced != 0 {
		t.Fatalf("切换次数 = %d，期望 0", rotation.advanced)
	}
}

// TestForwardStreamWroteBytesDoesNotSwitchCredential 断言流式已向客户端写出字节后不再切换凭据。
func TestForwardStreamWroteBytesDoesNotSwitchCredential(t *testing.T) {
	calls := 0
	caller := fakeCaller{stream: func(ctx context.Context, _ domain.Route, _ *domain.Request, _ []byte, sink domain.ChunkSink) error {
		calls++
		if err := sink.Send(ctx, domain.Chunk{Kind: domain.ChunkTextDelta, TextDelta: "hello"}); err != nil {
			return err
		}
		return credentialRejectedError{}
	}}
	rotation := &fakeRotation{remaining: 3}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		Credentials: rotation,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(true), &recordingWriter{events: &[]string{}}); err == nil {
		t.Fatal("已写出字节后的失败应终止转发")
	}
	if calls != 1 {
		t.Fatalf("上游调用次数 = %d，期望 1（开写后不切换）", calls)
	}
	if rotation.advanced != 0 {
		t.Fatalf("切换次数 = %d，期望 0", rotation.advanced)
	}
}

// TestForwardWithoutRecorder 断言未注入记账实现时转发照常完成（记账是旁路）。
func TestForwardWithoutRecorder(t *testing.T) {
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		return &domain.UpstreamResult{Raw: []byte(`{"ok":true}`), Response: &domain.Response{}}, nil
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRoute()}},
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}
	events := []string{}
	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &events}); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
}

// recordingObserver 记录每次尝试，供限流相关的断言读取。
type recordingObserver struct {
	mu      sync.Mutex
	records []domain.AttemptRecord
}

func (o *recordingObserver) RecordAttempt(_ context.Context, rec domain.AttemptRecord) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, rec)
	return nil
}

func (o *recordingObserver) snapshot() []domain.AttemptRecord {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]domain.AttemptRecord(nil), o.records...)
}

// fakeLimiter 是 domain.ChannelLimiter 的测试替身：按渠道 id 给出固定结果。
type fakeLimiter struct {
	mu      sync.Mutex
	replies map[uint64]limiterReply
	// acquired 与 released 按调用顺序记录渠道 id，用于断言取用与释放。
	acquired []uint64
	released []uint64
}

// limiterReply 是一条渠道的限流结果：等待时长与获取错误。
type limiterReply struct {
	waited time.Duration
	err    error
}

func (l *fakeLimiter) Acquire(_ context.Context, route domain.Route) (func(), time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	reply := l.replies[route.ChannelID]
	if reply.err != nil {
		return nil, reply.waited, reply.err
	}
	l.acquired = append(l.acquired, route.ChannelID)
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.released = append(l.released, route.ChannelID)
	}, reply.waited, nil
}

func (l *fakeLimiter) releasedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.released)
}

// fastBackoff 返回一套不做真实等待的退避参数，使回退断言不受实时退避影响。
func fastBackoff() BackoffOptions {
	return BackoffOptions{
		Base:   time.Nanosecond,
		Max:    time.Second,
		Jitter: func(int64) int64 { return 0 },
		Wait:   func(context.Context, time.Duration) error { return nil },
	}
}

// TestForwardRateLimitTimeoutFallsBackToNextChannel 断言限流等待超时按可重试失败处理，
// 换下一条候选而不是直接把失败回给客户端。
func TestForwardRateLimitTimeoutFallsBackToNextChannel(t *testing.T) {
	timeoutErr := failure.NewError(domain.CodeUpstreamRateLimited, "渠道限流：等待令牌超时", "", failure.ClassRateLimit)
	limiter := &fakeLimiter{replies: map[uint64]limiterReply{
		7: {waited: 300 * time.Millisecond, err: timeoutErr},
		8: {},
	}}
	calls := 0
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		calls++
		return &domain.UpstreamResult{Raw: []byte(`{"ok":true}`), Response: &domain.Response{}}, nil
	}}
	observer := &recordingObserver{}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRouteWithID(7), chatRouteWithID(8)}},
		Limiter:     limiter,
		Observer:    observer,
		MaxAttempts: 2,
		Backoff:     fastBackoff(),
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err != nil {
		t.Fatalf("回退后应成功：%v", err)
	}
	if calls != 1 {
		t.Fatalf("上游调用次数 = %d，期望 1（首次限流超时未发上游调用）", calls)
	}
	records := observer.snapshot()
	if len(records) != 2 {
		t.Fatalf("尝试记录数 = %d，期望 2", len(records))
	}
	if records[0].ErrorCode != string(domain.CodeUpstreamRateLimited) {
		t.Errorf("首条错误码 = %q，期望 %q", records[0].ErrorCode, domain.CodeUpstreamRateLimited)
	}
	if records[0].RateLimitWait != 300*time.Millisecond {
		t.Errorf("首条等待 = %s，期望 300ms", records[0].RateLimitWait)
	}
	if !records[1].ChannelSwitched || records[1].ChannelID != 8 {
		t.Errorf("第二条应为渠道回退：%+v", records[1])
	}
}

// TestForwardRecordsRateLimitWait 断言命中限流并等到令牌时，等待时长写进尝试记录。
func TestForwardRecordsRateLimitWait(t *testing.T) {
	limiter := &fakeLimiter{replies: map[uint64]limiterReply{7: {waited: 250 * time.Millisecond}}}
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		return &domain.UpstreamResult{Raw: []byte(`{"ok":true}`), Response: &domain.Response{}}, nil
	}}
	observer := &recordingObserver{}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		Limiter:  limiter,
		Observer: observer,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	records := observer.snapshot()
	if len(records) != 1 {
		t.Fatalf("尝试记录数 = %d，期望 1", len(records))
	}
	if records[0].RateLimitWait != 250*time.Millisecond {
		t.Errorf("等待 = %s，期望 250ms", records[0].RateLimitWait)
	}
}

// TestForwardStreamHoldsLimiterUntilStreamEnds 断言流式请求在整个流期间占着并发位，
// 流结束后才释放，不在开始流式写出时就释放。
func TestForwardStreamHoldsLimiterUntilStreamEnds(t *testing.T) {
	limiter := &fakeLimiter{replies: map[uint64]limiterReply{7: {}}}
	releasedDuringStream := false
	caller := fakeCaller{stream: func(ctx context.Context, _ domain.Route, _ *domain.Request, _ []byte, sink domain.ChunkSink) error {
		releasedDuringStream = limiter.releasedCount() > 0
		return sink.Send(ctx, domain.Chunk{Kind: domain.ChunkStreamEnd})
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: chatAdapterLookup(adapter),
		Upstream: caller,
		Routes:   fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		Limiter:  limiter,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(true), &recordingWriter{events: &[]string{}}); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	if releasedDuringStream {
		t.Error("流未结束时并发位已被释放")
	}
	if limiter.releasedCount() != 1 {
		t.Errorf("流结束后释放次数 = %d，期望 1", limiter.releasedCount())
	}
}

// TestForwardCrossProtocolAttemptAfterSameProtocolSegmentExhausted 断言同协议段预算耗尽后
// 仍会尝试跨协议段的候选：预算按段计量，同协议段的多条候选不会挤掉跨协议降级的机会。
func TestForwardCrossProtocolAttemptAfterSameProtocolSegmentExhausted(t *testing.T) {
	var calls []domain.Protocol
	caller := fakeCaller{complete: func(_ context.Context, route domain.Route, _ *domain.Request, _ []byte) (*domain.UpstreamResult, error) {
		calls = append(calls, route.Protocol)
		if route.Protocol == domain.ProtocolOpenAIChat {
			return nil, errRetryable
		}
		return successResult(), nil
	}}
	observer := &recordingObserver{}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters: crossProtocolAdapterLookup(),
		Upstream: caller,
		Routes: fakeRouteResolver{routes: []domain.Route{
			chatRouteWithID(7), chatRouteWithID(8), crossProtocolRouteWithID(9),
		}},
		Observer:    observer,
		MaxAttempts: 2,
		Backoff:     fastBackoff(),
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err != nil {
		t.Fatalf("跨协议候选应被尝试并成功：%v", err)
	}
	want := []domain.Protocol{domain.ProtocolOpenAIChat, domain.ProtocolOpenAIChat, domain.ProtocolAnthropicMessages}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("上游协议序列 = %v，期望 %v", calls, want)
	}
	records := observer.snapshot()
	if len(records) != 3 {
		t.Fatalf("尝试记录数 = %d，期望 3", len(records))
	}
	// 用既有字段区分「段切换」与「预算耗尽」：末条标记换渠道且跨协议。
	if last := records[2]; !last.CrossProtocol || !last.ChannelSwitched {
		t.Errorf("末条记录应标记段切换：%+v", last)
	}
}

// TestForwardCrossProtocolAttemptKeptWithSingleSameProtocolBudget 断言同协议段预算为 1 时，
// 跨协议段仍保有独立的一次尝试，两段预算互不挤占。
func TestForwardCrossProtocolAttemptKeptWithSingleSameProtocolBudget(t *testing.T) {
	var calls []domain.Protocol
	caller := fakeCaller{complete: func(_ context.Context, route domain.Route, _ *domain.Request, _ []byte) (*domain.UpstreamResult, error) {
		calls = append(calls, route.Protocol)
		if route.Protocol == domain.ProtocolOpenAIChat {
			return nil, errRetryable
		}
		return successResult(), nil
	}}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    crossProtocolAdapterLookup(),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRouteWithID(7), crossProtocolRouteWithID(9)}},
		MaxAttempts: 1,
		Backoff:     fastBackoff(),
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err != nil {
		t.Fatalf("跨协议候选应被尝试并成功：%v", err)
	}
	want := []domain.Protocol{domain.ProtocolOpenAIChat, domain.ProtocolAnthropicMessages}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("上游协议序列 = %v，期望 %v", calls, want)
	}
}

// TestForwardSegmentBudgetWithoutCrossProtocolCandidate 断言跨协议段无候选时不空转：
// 同协议段预算用尽即结束遍历，不产生额外上游调用，也不出现跨协议尝试。
func TestForwardSegmentBudgetWithoutCrossProtocolCandidate(t *testing.T) {
	calls := 0
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		calls++
		return nil, errRetryable
	}}
	observer := &recordingObserver{}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRouteWithID(7), chatRouteWithID(8), chatRouteWithID(9)}},
		Observer:    observer,
		MaxAttempts: 2,
		Backoff:     fastBackoff(),
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err == nil {
		t.Fatal("全部候选失败应返回错误")
	}
	if calls != 2 {
		t.Fatalf("上游调用次数 = %d，期望 2（同协议段预算用尽，第三条不再试）", calls)
	}
	for _, rec := range observer.snapshot() {
		if rec.CrossProtocol {
			t.Errorf("无跨协议候选时不应出现跨协议尝试：%+v", rec)
		}
	}
}

// TestForwardTotalAttemptCap 断言总次数防御性上限生效：段预算被调大也不能让一次请求打穿整条候选链。
func TestForwardTotalAttemptCap(t *testing.T) {
	calls := 0
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		calls++
		return nil, errRetryable
	}}
	observer := &recordingObserver{}
	adapter := openaichat.New()
	routes := make([]domain.Route, 0, 10)
	for id := uint64(1); id <= 5; id++ {
		routes = append(routes, chatRouteWithID(id))
	}
	for id := uint64(6); id <= 10; id++ {
		routes = append(routes, crossProtocolRouteWithID(id))
	}
	p, err := New(Options{
		Adapters:              crossProtocolAdapterLookup(),
		Upstream:              caller,
		Routes:                fakeRouteResolver{routes: routes},
		Observer:              observer,
		MaxAttempts:           10,
		CrossProtocolAttempts: 10,
		Backoff:               fastBackoff(),
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err == nil {
		t.Fatal("全部候选失败应返回错误")
	}
	if calls != maxTotalAttempts {
		t.Fatalf("上游调用次数 = %d，期望总上限 %d", calls, maxTotalAttempts)
	}
	if got := len(observer.snapshot()); got != maxTotalAttempts {
		t.Fatalf("尝试记录数 = %d，期望 %d", got, maxTotalAttempts)
	}
}
