package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/domain"
)

// fakeStreamMiddleware 是逐事件端口的测试替身：按事件名丢弃，或统一加前缀改写。
type fakeStreamMiddleware struct {
	drop   map[string]bool
	prefix string
	calls  int
}

// OnEvent 实现 domain.StreamMiddleware。
func (f *fakeStreamMiddleware) OnEvent(_ context.Context, _ *domain.Request, chunk domain.Chunk) domain.EventResult {
	f.calls++
	if f.drop[string(chunk.Kind)] {
		return domain.EventResult{Drop: true}
	}
	if f.prefix != "" {
		chunk.TextDelta = f.prefix + chunk.TextDelta
		return domain.EventResult{Chunk: chunk}
	}
	return domain.EventResult{Chunk: chunk, Unchanged: true}
}

// fakeResponseMiddleware 是非流式响应端口的测试替身：给响应体补一个字段。
type fakeResponseMiddleware struct {
	marker string
	calls  int
}

// OnResponse 实现 domain.ResponseMiddleware。
func (f *fakeResponseMiddleware) OnResponse(_ context.Context, _ *domain.Request, body []byte) []byte {
	f.calls++
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return body
	}
	fields[f.marker] = true
	encoded, err := json.Marshal(fields)
	if err != nil {
		return body
	}
	return encoded
}

// TestPassthroughSinkKeepsRawFrameWhenUnchanged 守护未改动时逐字节透传。
func TestPassthroughSinkKeepsRawFrameWhenUnchanged(t *testing.T) {
	var out bytes.Buffer
	events := &fakeStreamMiddleware{}
	sink := &passthroughSink{out: &out, events: events, req: &domain.Request{}}
	raw := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	chunks := []domain.Chunk{{Kind: domain.ChunkTextDelta, TextDelta: "hi"}}
	if err := sink.SendFrame(context.Background(), raw, chunks); err != nil {
		t.Fatalf("SendFrame 失败：%v", err)
	}
	if !bytes.Equal(out.Bytes(), raw) {
		t.Fatalf("未改动时应原样透传：\n实际 %q\n期望 %q", out.String(), string(raw))
	}
}

// TestPassthroughSinkDropsFilteredFrame 守护整帧内容分片被丢弃后不写出任何字节。
func TestPassthroughSinkDropsFilteredFrame(t *testing.T) {
	var out bytes.Buffer
	events := &fakeStreamMiddleware{drop: map[string]bool{string(domain.ChunkTextDelta): true}}
	sink := &passthroughSink{out: &out, events: events, req: &domain.Request{}}
	raw := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	chunks := []domain.Chunk{{Kind: domain.ChunkTextDelta, TextDelta: "hi"}}
	if err := sink.SendFrame(context.Background(), raw, chunks); err != nil {
		t.Fatalf("SendFrame 失败：%v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("被丢弃的帧不应写出字节，实际 %q", out.String())
	}
	if sink.wroteBytes() {
		t.Fatal("被丢弃的帧不应置位已写出")
	}
}

// TestPassthroughSinkReencodesRewrittenFrame 守护改写后按分片重新编码。
func TestPassthroughSinkReencodesRewrittenFrame(t *testing.T) {
	var out bytes.Buffer
	events := &fakeStreamMiddleware{prefix: "PRE-"}
	sink := &passthroughSink{
		out:    &out,
		events: events,
		req:    &domain.Request{},
		client: openaichat.New(),
	}
	raw := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	chunks := []domain.Chunk{{Kind: domain.ChunkTextDelta, TextDelta: "hi"}}
	if err := sink.SendFrame(context.Background(), raw, chunks); err != nil {
		t.Fatalf("SendFrame 失败：%v", err)
	}
	if !strings.Contains(out.String(), "PRE-hi") {
		t.Fatalf("改写后的分片应被重新编码写出，实际 %q", out.String())
	}
	if !sink.wroteBytes() {
		t.Fatal("重新编码写出应置位已写出")
	}
}

// TestPassthroughSinkSkipsMixedFrame 守护混有非内容分片时整帧不介入。
func TestPassthroughSinkSkipsMixedFrame(t *testing.T) {
	var out bytes.Buffer
	events := &fakeStreamMiddleware{drop: map[string]bool{string(domain.ChunkTextDelta): true}}
	sink := &passthroughSink{out: &out, events: events, req: &domain.Request{}, client: openaichat.New()}
	raw := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"usage\":{\"prompt_tokens\":1}}\n\n")
	chunks := []domain.Chunk{
		{Kind: domain.ChunkTextDelta, TextDelta: "hi"},
		{Kind: domain.ChunkUsage},
	}
	if err := sink.SendFrame(context.Background(), raw, chunks); err != nil {
		t.Fatalf("SendFrame 失败：%v", err)
	}
	if !bytes.Equal(out.Bytes(), raw) {
		t.Fatalf("混有非内容分片时应整帧原样透传：\n实际 %q\n期望 %q", out.String(), string(raw))
	}
	if events.calls != 0 {
		t.Fatalf("混有非内容分片时不应调用中间件，实际调用 %d 次", events.calls)
	}
}

// TestRebuildSinkDropsFilteredChunk 守护重建路径上被丢弃的分片不产生客户端字节。
func TestRebuildSinkDropsFilteredChunk(t *testing.T) {
	var out bytes.Buffer
	events := &fakeStreamMiddleware{drop: map[string]bool{string(domain.ChunkTextDelta): true}}
	sink, err := newRebuildSink(&out, openaichat.New(), "model", events, &domain.Request{})
	if err != nil {
		t.Fatalf("构造重建目标失败：%v", err)
	}
	if err := sink.Send(context.Background(), domain.Chunk{Kind: domain.ChunkTextDelta, TextDelta: "hi"}); err != nil {
		t.Fatalf("Send 失败：%v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("被丢弃的分片不应产生客户端字节，实际 %q", out.String())
	}
}

// TestWriteCompletionAppliesResponseMiddleware 守护非流式写出前经过响应中间件。
func TestWriteCompletionAppliesResponseMiddleware(t *testing.T) {
	response := &fakeResponseMiddleware{marker: "mw"}
	pipeline := &Pipeline{response: response}
	var out bytes.Buffer
	req := &domain.Request{Protocol: domain.ProtocolOpenAIChat}
	route := domain.Route{Protocol: domain.ProtocolOpenAIChat}
	completion := &domain.UpstreamResult{Raw: []byte(`{"ok":true}`), Response: &domain.Response{}}
	if _, err := pipeline.writeCompletion(context.Background(), openaichat.New(), req, route, completion, &out); err != nil {
		t.Fatalf("写出失败：%v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatalf("响应体不是 JSON：%v，原文 %s", err, out.String())
	}
	if fields["mw"] != true {
		t.Fatalf("响应中间件未生效：%s", out.String())
	}
	if response.calls != 1 {
		t.Fatalf("响应中间件调用次数 = %d，期望 1", response.calls)
	}
}

// fakeStreamEndMiddleware 在逐事件端口之上再实现流末补发。
//
// 与 fakeStreamMiddleware 分开：补发是可选端口（domain.StreamEndMiddleware），
// 未实现它的中间件不该被迫提供一个空方法。
type fakeStreamEndMiddleware struct {
	fakeStreamMiddleware
	flush    []domain.Chunk
	endCalls int
}

// OnStreamEnd 实现 domain.StreamEndMiddleware。
func (f *fakeStreamEndMiddleware) OnStreamEnd(_ context.Context, _ *domain.Request) []domain.Chunk {
	f.endCalls++
	return f.flush
}

// TestPassthroughSinkFlushesBeforeStreamEnd 守护流末补发写在终止帧之前。
//
// 顺序不是细节：多数客户端见到结束原因后不再处理后续内容分片，补发排在终止帧之后
// 等于白写。逐事件钩子拿不到流结束分片，跨分片缓冲的改写只能靠这个时机冲刷尾巴。
func TestPassthroughSinkFlushesBeforeStreamEnd(t *testing.T) {
	var out bytes.Buffer
	events := &fakeStreamEndMiddleware{
		flush: []domain.Chunk{{Kind: domain.ChunkTextDelta, TextDelta: "tail"}},
	}
	sink := &passthroughSink{out: &out, events: events, req: &domain.Request{}, client: openaichat.New()}
	raw := []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	chunks := []domain.Chunk{{Kind: domain.ChunkStreamEnd, Usage: &domain.Usage{}}}
	if err := sink.SendFrame(context.Background(), raw, chunks); err != nil {
		t.Fatalf("SendFrame 失败：%v", err)
	}
	if events.endCalls != 1 {
		t.Fatalf("流末补发调用次数 = %d，期望 1", events.endCalls)
	}
	written := out.String()
	tail := strings.Index(written, "tail")
	if tail < 0 {
		t.Fatalf("补发分片未写出：%q", written)
	}
	if end := strings.Index(written, "finish_reason"); end < 0 || tail > end {
		t.Fatalf("补发必须排在终止帧之前，实际 %q", written)
	}
}

// TestPassthroughSinkSkipsStreamEndFlushWithoutCapability 守护未实现补发端口时逐字节不变。
//
// 多数中间件只做逐事件改写；给它们加一次空调用等于给每个流式响应加一次无谓开销。
func TestPassthroughSinkSkipsStreamEndFlushWithoutCapability(t *testing.T) {
	var out bytes.Buffer
	events := &fakeStreamMiddleware{}
	sink := &passthroughSink{out: &out, events: events, req: &domain.Request{}, client: openaichat.New()}
	raw := []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	chunks := []domain.Chunk{{Kind: domain.ChunkStreamEnd, Usage: &domain.Usage{}}}
	if err := sink.SendFrame(context.Background(), raw, chunks); err != nil {
		t.Fatalf("SendFrame 失败：%v", err)
	}
	if !bytes.Equal(out.Bytes(), raw) {
		t.Fatalf("未实现补发端口时应原样透传：\n实际 %q\n期望 %q", out.String(), string(raw))
	}
}

// TestRebuildSinkFlushesReasoningBeforeStreamEnd 守护重建路径也补发，且推理分片真能编出帧。
//
// 重建路径曾经对推理分片直接 return nil（「编码方向不下发推理内容」），于是中间件把正文
// 改成推理之后内容静默消失，而透传路径只是回退原帧——两条路结果不一致。
func TestRebuildSinkFlushesReasoningBeforeStreamEnd(t *testing.T) {
	var out bytes.Buffer
	events := &fakeStreamEndMiddleware{
		flush: []domain.Chunk{{Kind: domain.ChunkReasoningDelta, TextDelta: "尾巴"}},
	}
	sink, err := newRebuildSink(&out, openaichat.New(), "up-model", events, &domain.Request{})
	if err != nil {
		t.Fatalf("构造重建下沉目标失败：%v", err)
	}
	if err := sink.Send(context.Background(), domain.Chunk{Kind: domain.ChunkStreamEnd, Usage: &domain.Usage{}}); err != nil {
		t.Fatalf("Send 失败：%v", err)
	}
	if events.endCalls != 1 {
		t.Fatalf("流末补发调用次数 = %d，期望 1", events.endCalls)
	}
	written := out.String()
	if !strings.Contains(written, "reasoning_content") {
		t.Fatalf("重建路径未写出推理补发帧：%q", written)
	}
	if !strings.Contains(written, "尾巴") {
		t.Fatalf("补发内容未写出：%q", written)
	}
}

// TestPassthroughSinkFlushesOnEOFStreamEndChunk 守护 EOF 收尾分片也触发补发。
//
// openai_chat 的结束分片有两条来源：终止帧与 EOF 补齐。后者没有对应的上游帧，
// 经 Send（而不是 SendFrame）单独送达，且该入口刻意不写任何字节。补发只挂在 SendFrame
// 上时，这条路上的缓冲尾巴会静默丢失——正文整段消失，而客户端只看到推理。
func TestPassthroughSinkFlushesOnEOFStreamEndChunk(t *testing.T) {
	var out bytes.Buffer
	events := &fakeStreamEndMiddleware{
		flush: []domain.Chunk{{Kind: domain.ChunkTextDelta, TextDelta: "尾巴"}},
	}
	sink := &passthroughSink{out: &out, events: events, req: &domain.Request{}, client: openaichat.New()}
	if err := sink.Send(context.Background(), domain.Chunk{Kind: domain.ChunkStreamEnd, Usage: &domain.Usage{}}); err != nil {
		t.Fatalf("Send 失败：%v", err)
	}
	if events.endCalls != 1 {
		t.Fatalf("流末补发调用次数 = %d，期望 1", events.endCalls)
	}
	if !strings.Contains(out.String(), "尾巴") {
		t.Fatalf("EOF 收尾分片未触发补发写出：%q", out.String())
	}
}
