package pipeline

import (
	"bytes"
	"context"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
)

// TestPassthroughSinkSuppressesUsageOnlyFrame 守护客户端未索取用量时的抑制语义：
// 只承载用量的帧不写回客户端，用量仍照常记账，且不置位「已写出字节」。
func TestPassthroughSinkSuppressesUsageOnlyFrame(t *testing.T) {
	var out bytes.Buffer
	usage := domain.Usage{Source: domain.UsageSourceUpstream, InputTokens: 5, OutputTokens: 3}
	sink := &passthroughSink{out: &out, suppressUsageFrames: true}

	raw := []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5}}\n\n")
	if err := sink.SendFrame(context.Background(), raw, []domain.Chunk{{Kind: domain.ChunkUsage, Usage: &usage}}); err != nil {
		t.Fatalf("SendFrame 失败：%v", err)
	}
	if out.Len() != 0 {
		t.Errorf("用量帧不应写回客户端，实际写出 %q", out.String())
	}
	if sink.wroteBytes() {
		t.Error("抑制用量帧不应置位「已写出字节」")
	}
	if got := sink.collectedUsage(); got.InputTokens != 5 || got.OutputTokens != 3 {
		t.Errorf("用量应照常记账，得到 %+v", got)
	}
}

// TestPassthroughSinkForwardsUsageFrameWhenRequested 守护客户端索取用量时的透传语义：
// 同一帧原样写回。
func TestPassthroughSinkForwardsUsageFrameWhenRequested(t *testing.T) {
	var out bytes.Buffer
	usage := domain.Usage{Source: domain.UsageSourceUpstream, InputTokens: 5}
	sink := &passthroughSink{out: &out}

	raw := []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5}}\n\n")
	if err := sink.SendFrame(context.Background(), raw, []domain.Chunk{{Kind: domain.ChunkUsage, Usage: &usage}}); err != nil {
		t.Fatalf("SendFrame 失败：%v", err)
	}
	if !bytes.Equal(out.Bytes(), raw) {
		t.Errorf("索取用量时帧应原样转发：\n实际 %q\n期望 %q", out.String(), string(raw))
	}
	if !sink.wroteBytes() {
		t.Error("写回用量帧应置位「已写出字节」")
	}
}

// TestPassthroughSinkForwardsUnrecognizedFrame 守护「宁多勿丢」：
// 分片里没有只承载用量的帧时，原始字节原样转发，不做字节前缀匹配。
func TestPassthroughSinkForwardsUnrecognizedFrame(t *testing.T) {
	var out bytes.Buffer
	sink := &passthroughSink{out: &out, suppressUsageFrames: true}

	frames := []struct {
		name   string
		raw    string
		chunks []domain.Chunk
	}{
		{name: "无分片", raw: "data: {\"foo\":\"bar\"}\n\n"},
		{name: "心跳", raw: ": keep-alive\n\n"},
		{name: "内容帧", raw: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n", chunks: []domain.Chunk{{Kind: domain.ChunkTextDelta, TextDelta: "hi"}}},
	}
	for _, frame := range frames {
		t.Run(frame.name, func(t *testing.T) {
			out.Reset()
			sink.wrote = false
			if err := sink.SendFrame(context.Background(), []byte(frame.raw), frame.chunks); err != nil {
				t.Fatalf("SendFrame 失败：%v", err)
			}
			if out.String() != frame.raw {
				t.Errorf("无法识别为用量帧时应原样转发：\n实际 %q\n期望 %q", out.String(), frame.raw)
			}
		})
	}
}
