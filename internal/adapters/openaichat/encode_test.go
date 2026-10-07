package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
)

// TestEncodeChunkEmitsReasoningContent 守护推理增量编到 delta.reasoning_content。
//
// 这条取舍曾经是反的（推理在编码方向不下发）。改回来的理由是同一份上游数据在
// 「同协议透传」与「跨协议重建」下必须得到同一种结果：前者本来就原样转发上游的
// reasoning_content（raw 透传不经过编码器），只有后者丢弃。
func TestEncodeChunkEmitsReasoningContent(t *testing.T) {
	frame, err := New().EncodeChunk(domain.Chunk{
		Kind: domain.ChunkReasoningDelta, Model: "up-model", TextDelta: "先想一步",
	})
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(frame)), "data: "))
	var wire struct {
		Choices []struct {
			Delta map[string]any `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &wire); err != nil {
		t.Fatalf("帧不是合法 JSON：%v，原文 %s", err, payload)
	}
	if len(wire.Choices) != 1 {
		t.Fatalf("帧里应有 1 个 choice，实际 %d：%s", len(wire.Choices), payload)
	}
	if got := wire.Choices[0].Delta["reasoning_content"]; got != "先想一步" {
		t.Errorf("reasoning_content = %v，期望 先想一步", got)
	}
	// 推理与正文是两个字段：推理帧里不该顺手塞一个空 content，那会让客户端把
	// 「这段是正文」也算进去。
	if value, ok := wire.Choices[0].Delta["content"]; ok {
		t.Errorf("推理帧不该带 content 字段，实际 %v", value)
	}
}

// TestEncodeChunkSkipsEmptyReasoning 守护空推理不发帧。
//
// 中间件做流末补发时会构造分片，空文本不该产出无载荷的帧。
func TestEncodeChunkSkipsEmptyReasoning(t *testing.T) {
	frame, err := New().EncodeChunk(domain.Chunk{Kind: domain.ChunkReasoningDelta})
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	if len(frame) != 0 {
		t.Fatalf("空推理应不产出帧，实际 %q", frame)
	}
}

// TestEncodeMessageEmitsReasoningContent 守护非流式响应把推理编到 message.reasoning_content。
//
// 流式与非流式必须一致：只改一处会让同一上游在两种请求形态下给出不同字段。
func TestEncodeMessageEmitsReasoningContent(t *testing.T) {
	message, err := encodeMessage(domain.Message{
		Role: domain.RoleAssistant,
		Parts: []domain.Part{
			{Kind: domain.PartReasoning, Text: "推理"},
			{Kind: domain.PartText, Text: "答案"},
		},
	})
	if err != nil {
		t.Fatalf("编码消息失败：%v", err)
	}
	if message.ReasoningContent != "推理" {
		t.Errorf("reasoning_content = %q，期望 推理", message.ReasoningContent)
	}
	if message.Content == nil || *message.Content != "答案" {
		t.Errorf("content = %v，期望 答案", message.Content)
	}
}

// TestEncodeMessageOmitsEmptyReasoning 守护无推理时不下发该字段。
//
// 绝大多数响应没有推理，恒发一个空串会让客户端以为「这次有推理但没内容」。
func TestEncodeMessageOmitsEmptyReasoning(t *testing.T) {
	body, err := json.Marshal(responseMessage{Role: string(domain.RoleAssistant)})
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	if strings.Contains(string(body), "reasoning_content") {
		t.Fatalf("空推理不该出现在报文里：%s", body)
	}
	message, err := encodeMessage(domain.Message{
		Role:  domain.RoleAssistant,
		Parts: []domain.Part{{Kind: domain.PartText, Text: "答案"}},
	})
	if err != nil {
		t.Fatalf("编码消息失败：%v", err)
	}
	if message.ReasoningContent != "" {
		t.Errorf("无推理片段时 reasoning_content = %q，期望空串", message.ReasoningContent)
	}
}

// TestDecodeStreamFrameIgnoresEmptyFinishReason 守护「空串 finish_reason 不算收尾」。
//
// 部分兼容上游（MiniMax 官方 OpenAI 兼容端点即如此）在**每一帧**都发
// "finish_reason": ""（空串而非 null）。只看非 nil 会出两个问题：
//
//   - finishReasonSeen 在首帧就置位，截断的流被当成完整结束；
//   - 每帧都多出一个收尾分片，中间件因「帧里含非内容分片」而整帧放弃介入，
//     于是插件在这些上游上完全失效。
func TestDecodeStreamFrameIgnoresEmptyFinishReason(t *testing.T) {
	adapter := New()
	body := []byte(`{"id":"c1","object":"chat.completion.chunk","model":"m",` +
		`"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":""}]}`)
	chunks, err := adapter.DecodeStreamFrame("", body)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	kinds := make([]domain.ChunkKind, 0, len(chunks))
	for _, chunk := range chunks {
		kinds = append(kinds, chunk.Kind)
	}
	if len(chunks) != 1 || chunks[0].Kind != domain.ChunkTextDelta {
		t.Fatalf("空串 finish_reason 的帧应只产出正文分片，实际 %v", kinds)
	}
	// 从未见到真实收尾即 EOF：属真截断，FinishStream 不得给出结束分片。
	if end := adapter.FinishStream(); len(end) != 0 {
		t.Fatalf("只见到空串 finish_reason 时不该判为完整结束，实际产出 %v", end)
	}
}

// TestDecodeStreamFrameEmitsFinishChunk 守护真实 finish_reason 产出收尾分片。
//
// 它给中间件的流末补发提供写出时机：部分上游不发 [DONE]，只给 finish_reason 后直接 EOF，
// 那类上游的 ChunkStreamEnd 要等到 EOF 才产出，届时终止帧已经写出去了。
func TestDecodeStreamFrameEmitsFinishChunk(t *testing.T) {
	adapter := New()
	body := []byte(`{"id":"c1","object":"chat.completion.chunk","model":"m",` +
		`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	chunks, err := adapter.DecodeStreamFrame("", body)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if len(chunks) != 1 || chunks[0].Kind != domain.ChunkFinish {
		t.Fatalf("真实 finish_reason 应产出收尾分片，实际 %v", chunks)
	}
}
