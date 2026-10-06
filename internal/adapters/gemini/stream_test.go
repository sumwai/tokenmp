package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
)

// splitSSE 把编码产物拆成逐帧的 JSON 正文，供回解断言。
func splitSSE(t *testing.T, raw []byte) []string {
	t.Helper()
	frames := make([]string, 0)
	for _, block := range strings.Split(string(raw), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		payload, ok := strings.CutPrefix(block, "data: ")
		if !ok {
			t.Fatalf("SSE 帧缺少 data: 前缀：%q", block)
		}
		frames = append(frames, payload)
	}
	return frames
}

// TestDecodeStreamFrame 守护流式解码：文本增量即时产出，结束原因与用量积累到 EOF 才成结束分片。
func TestDecodeStreamFrame(t *testing.T) {
	adapter := New()
	text, err := adapter.DecodeStreamFrame("", []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"你"}]},"index":0}],"modelVersion":"up-model"}`))
	if err != nil {
		t.Fatalf("解码首帧失败：%v", err)
	}
	if len(text) != 1 || text[0].Kind != domain.ChunkTextDelta || text[0].TextDelta != "你" {
		t.Fatalf("首帧分片 = %+v", text)
	}
	// 结束前 FinishStream 不得产出结束分片（尚未观察到结束原因）。
	if chunks := adapter.FinishStream(); len(chunks) != 0 {
		t.Fatalf("结束原因未出现时不应产出结束分片，实际 %+v", chunks)
	}
	if _, err := adapter.DecodeStreamFrame("", []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3},"modelVersion":"up-model"}`)); err != nil {
		t.Fatalf("解码结束帧失败：%v", err)
	}
	chunks := adapter.FinishStream()
	if len(chunks) != 1 || chunks[0].Kind != domain.ChunkStreamEnd {
		t.Fatalf("EOF 收尾分片 = %+v", chunks)
	}
	if chunks[0].FinishReason != domain.FinishStop {
		t.Errorf("结束原因 = %q，期望 stop", chunks[0].FinishReason)
	}
	if chunks[0].Usage == nil || chunks[0].Usage.InputTokens != 5 || chunks[0].Usage.OutputTokens != 3 {
		t.Errorf("用量 = %+v", chunks[0].Usage)
	}
	// 幂等：再次调用不得产出第二个结束分片。
	if again := adapter.FinishStream(); len(again) != 0 {
		t.Errorf("FinishStream 应幂等，实际再次产出 %+v", again)
	}
}

// TestDecodeStreamFrameError 守护流式错误帧：上游错误体必须转成失败，不得被当成内容帧吞掉。
func TestDecodeStreamFrameError(t *testing.T) {
	_, err := New().DecodeStreamFrame("", []byte(`{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`))
	if err == nil {
		t.Fatal("错误帧必须返回错误")
	}
	if code := domain.AsError(err).Code; code != domain.CodeUpstreamRateLimited {
		t.Errorf("错误码 = %q，期望 %q", code, domain.CodeUpstreamRateLimited)
	}
}

// TestStreamEncodeRoundTrip 守护流式编码与解码自洽：文本、工具调用与结束帧回解后与输入一致。
func TestStreamEncodeRoundTrip(t *testing.T) {
	source := New()
	streamAdapter, ok := source.NewStream().(*Adapter)
	if !ok {
		t.Fatal("NewStream 应返回 *Adapter")
	}
	if start, err := streamAdapter.EncodeStreamStart("up-model"); err != nil || start != nil {
		t.Fatalf("流开始帧应为空：%q, %v", start, err)
	}
	textFrame, err := streamAdapter.EncodeChunk(domain.Chunk{Kind: domain.ChunkTextDelta, Model: "up-model", TextDelta: "hello"})
	if err != nil {
		t.Fatalf("编码文本帧失败：%v", err)
	}
	// 工具调用先缓存、参数分片合并：单次调用返回空帧，分片到结束帧才合成完整 functionCall。
	if toolFrame, toolErr := streamAdapter.EncodeChunk(domain.Chunk{Kind: domain.ChunkToolCallDelta, ToolCall: &domain.ToolCall{Index: 0, Name: "get_weather", Arguments: `{"city":`}}); toolErr != nil || toolFrame != nil {
		t.Fatalf("工具调用分片应缓存，实际 %q, %v", toolFrame, toolErr)
	}
	if toolFrame, toolErr := streamAdapter.EncodeChunk(domain.Chunk{Kind: domain.ChunkToolCallDelta, ToolCall: &domain.ToolCall{Index: 0, Arguments: `"北京"}`}}); toolErr != nil || toolFrame != nil {
		t.Fatalf("工具调用缝片应缓存，实际 %q, %v", toolFrame, toolErr)
	}
	endFrame, err := streamAdapter.EncodeStreamEnd(domain.Chunk{
		Kind:         domain.ChunkStreamEnd,
		Model:        "up-model",
		Usage:        &domain.Usage{Source: domain.UsageSourceUpstream, InputTokens: 5, OutputTokens: 3},
		FinishReason: domain.FinishStop,
	})
	if err != nil {
		t.Fatalf("编码结束帧失败：%v", err)
	}

	// 回解：文本帧、工具调用帧与结束帧。
	decoder := New()
	var text, calls int
	for _, payload := range splitSSE(t, textFrame) {
		if _, err := decoder.DecodeStreamFrame("", []byte(payload)); err != nil {
			t.Fatalf("回解文本帧失败：%v", err)
		}
		text++
	}
	for _, payload := range splitSSE(t, endFrame) {
		chunks, err := decoder.DecodeStreamFrame("", []byte(payload))
		if err != nil {
			t.Fatalf("回解结束帧失败：%v", err)
		}
		for _, chunk := range chunks {
			if chunk.Kind == domain.ChunkToolCallDelta {
				calls++
				if chunk.ToolCall.Name != "get_weather" {
					t.Errorf("工具名 = %q", chunk.ToolCall.Name)
				}
			}
		}
	}
	if text != 1 {
		t.Errorf("文本帧数 = %d，期望 1", text)
	}
	if calls != 1 {
		t.Errorf("工具调用分片数 = %d，期望 1", calls)
	}
	chunks := decoder.FinishStream()
	if len(chunks) != 1 || chunks[0].Kind != domain.ChunkStreamEnd || chunks[0].FinishReason != domain.FinishStop {
		t.Fatalf("回解结束分片 = %+v", chunks)
	}
}

// TestEncodeChunkRejectsEndKinds 守护编码方向的下发路径唯一：用量与结束原因不得经 EncodeChunk 下发。
func TestEncodeChunkRejectsEndKinds(t *testing.T) {
	for _, kind := range []domain.ChunkKind{domain.ChunkUsage, domain.ChunkFinish, domain.ChunkStreamEnd} {
		if _, err := New().EncodeChunk(domain.Chunk{Kind: kind}); err == nil {
			t.Errorf("分片类型 %q 应经 EncodeStreamEnd 下发", kind)
		}
	}
}

// TestRewriteRawBody 守护同协议透传的输出上限改写：maxOutputTokens 嵌在 generationConfig 里。
func TestRewriteRawBody(t *testing.T) {
	adapter := New()
	limit := 64
	body := []byte(`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":256,"temperature":0.5}}`)
	rewritten, parts, err := adapter.RewriteRawBody(body, domain.RewriteOptions{MaxOutputTokens: &limit})
	if err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	if !parts.Has(domain.RewritePartRequestOutputLimit) {
		t.Errorf("应标注输出上限改写，实际 %v", parts)
	}
	var fields struct {
		GenerationConfig struct {
			MaxOutputTokens int     `json:"maxOutputTokens"`
			Temperature     float64 `json:"temperature"`
		} `json:"generationConfig"`
	}
	if unmarshalErr := json.Unmarshal(rewritten, &fields); unmarshalErr != nil {
		t.Fatalf("改写结果不是合法 JSON：%v", unmarshalErr)
	}
	if fields.GenerationConfig.MaxOutputTokens != 64 {
		t.Errorf("maxOutputTokens = %d，期望钳制到 64", fields.GenerationConfig.MaxOutputTokens)
	}
	if fields.GenerationConfig.Temperature != 0.5 {
		t.Errorf("未建模字段应保持原样，temperature = %v", fields.GenerationConfig.Temperature)
	}

	// 没有任何改写项时逐字节返回入参。
	same, noParts, err := adapter.RewriteRawBody(body, domain.RewriteOptions{})
	if err != nil {
		t.Fatalf("空改写失败：%v", err)
	}
	if string(same) != string(body) || len(noParts) != 0 {
		t.Error("无改写项时应逐字节返回原报文")
	}
}
