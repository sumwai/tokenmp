package pipeline

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/domain"
)

// 本文件覆盖用量落库的时序与终态判定。
//
// 用真实适配器 + 假上游/假选路/假记账：时序断言只关心「记账发生在哪个动作之前 / 之后」，
// 协议编解码细节不属于本文件的目标，复用真实适配器可以避免另造一套假实现。

// errRetryable 是一次可换渠道重试的上游失败。
var errRetryable = domain.NewError(domain.CodeUpstreamUnavailable, "上游不可用")

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
