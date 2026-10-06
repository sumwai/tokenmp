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
