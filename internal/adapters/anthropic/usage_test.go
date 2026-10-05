package anthropic

import (
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
)

// TestDecodeResponseTieredCacheWrite 守护非流式响应里 cache_creation 分档明细的解析：
// 两档分别落入 CacheWrite5mTokens / CacheWrite1hTokens，不分档字段保持 0，
// 输入总数仍取线格式 input_tokens 与缓存读、缓存写合计之和。
func TestDecodeResponseTieredCacheWrite(t *testing.T) {
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"m",` +
		`"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":5,"output_tokens":3,"cache_read_input_tokens":2,` +
		`"cache_creation_input_tokens":9,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":6,"ephemeral_1h_input_tokens":3}}}`
	resp, err := New().DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	usage := resp.Usage
	if usage.CacheWrite5mTokens != 6 || usage.CacheWrite1hTokens != 3 {
		t.Errorf("分档缓存写 = 5m %d / 1h %d，期望 6 / 3",
			usage.CacheWrite5mTokens, usage.CacheWrite1hTokens)
	}
	if usage.CacheWriteTokens != 0 {
		t.Errorf("给出分档时不分档字段应为 0，得到 %d", usage.CacheWriteTokens)
	}
	if usage.InputTokens != 16 {
		t.Errorf("输入总数 = %d，期望 5 + 2 + 9 = 16", usage.InputTokens)
	}
	if usage.CacheReadTokens != 2 {
		t.Errorf("缓存读 = %d，期望 2", usage.CacheReadTokens)
	}
	if !usage.NonNegative() {
		t.Error("解析结果应为非负")
	}
}

// TestDecodeResponseUnTieredCacheWrite 守护不分档形态维持现状：上游只给
// cache_creation_input_tokens 时落入不分档字段，分档字段保持 0。
func TestDecodeResponseUnTieredCacheWrite(t *testing.T) {
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"m",` +
		`"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":5,"output_tokens":3,"cache_creation_input_tokens":4}}`
	resp, err := New().DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	usage := resp.Usage
	if usage.CacheWriteTokens != 4 {
		t.Errorf("不分档缓存写 = %d，期望 4", usage.CacheWriteTokens)
	}
	if usage.CacheWrite5mTokens != 0 || usage.CacheWrite1hTokens != 0 {
		t.Errorf("不分档形态下分档字段应为 0，得到 %d / %d",
			usage.CacheWrite5mTokens, usage.CacheWrite1hTokens)
	}
	if usage.InputTokens != 9 {
		t.Errorf("输入总数 = %d，期望 5 + 4 = 9", usage.InputTokens)
	}
}

// TestDecodeStreamTieredCacheWrite 守护流式路径的两处分档来源：
// message_start 的 message.usage 与随后 message_delta 的 usage，二者都按互斥口径入档。
func TestDecodeStreamTieredCacheWrite(t *testing.T) {
	tests := []struct {
		name         string
		messageStart string
		messageDelta string
	}{
		{
			name: "message_start",
			messageStart: `{"type":"message_start","message":{"model":"m","usage":{"input_tokens":5,` +
				`"cache_read_input_tokens":2,"cache_creation_input_tokens":9,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":6,"ephemeral_1h_input_tokens":3}}}}`,
			messageDelta: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		},
		{
			name: "message_delta",
			messageStart: `{"type":"message_start","message":{"model":"m","usage":{"input_tokens":5,` +
				`"cache_read_input_tokens":2}}}`,
			messageDelta: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},` +
				`"usage":{"output_tokens":3,"cache_creation_input_tokens":9,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":6,"ephemeral_1h_input_tokens":3}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := New()
			if _, err := adapter.DecodeStreamFrame("message_start", []byte(tt.messageStart)); err != nil {
				t.Fatalf("解码 message_start 失败：%v", err)
			}
			if _, err := adapter.DecodeStreamFrame("message_delta", []byte(tt.messageDelta)); err != nil {
				t.Fatalf("解码 message_delta 失败：%v", err)
			}
			chunks, err := adapter.DecodeStreamFrame("message_stop", []byte(`{"type":"message_stop"}`))
			if err != nil {
				t.Fatalf("解码 message_stop 失败：%v", err)
			}
			if len(chunks) != 1 || chunks[0].Usage == nil {
				t.Fatalf("message_stop 应产出一个携带用量的结束分片，得到 %+v", chunks)
			}
			usage := *chunks[0].Usage
			if usage.CacheWrite5mTokens != 6 || usage.CacheWrite1hTokens != 3 {
				t.Errorf("分档缓存写 = 5m %d / 1h %d，期望 6 / 3",
					usage.CacheWrite5mTokens, usage.CacheWrite1hTokens)
			}
			if usage.CacheWriteTokens != 0 {
				t.Errorf("给出分档时不分档字段应为 0，得到 %d", usage.CacheWriteTokens)
			}
			if usage.InputTokens != 16 {
				t.Errorf("输入总数 = %d，期望 5 + 2 + 9 = 16", usage.InputTokens)
			}
			if usage.Source != domain.UsageSourceUpstream {
				t.Errorf("来源 = %q，期望 %q", string(usage.Source), string(domain.UsageSourceUpstream))
			}
		})
	}
}

// TestDecodeStreamUnTieredCacheWrite 守护流式不分档形态：只有 cache_creation_input_tokens 时
// 落不分档字段，分档字段保持 0。
func TestDecodeStreamUnTieredCacheWrite(t *testing.T) {
	adapter := New()
	start := `{"type":"message_start","message":{"model":"m","usage":{"input_tokens":5,` +
		`"cache_read_input_tokens":2,"cache_creation_input_tokens":4}}}`
	if _, err := adapter.DecodeStreamFrame("message_start", []byte(start)); err != nil {
		t.Fatalf("解码 message_start 失败：%v", err)
	}
	chunks, err := adapter.DecodeStreamFrame("message_stop", []byte(`{"type":"message_stop"}`))
	if err != nil {
		t.Fatalf("解码 message_stop 失败：%v", err)
	}
	usage := *chunks[0].Usage
	if usage.CacheWriteTokens != 4 || usage.CacheWrite5mTokens != 0 || usage.CacheWrite1hTokens != 0 {
		t.Errorf("不分档缓存写 = %d，分档 = %d / %d，期望 4 / 0 / 0",
			usage.CacheWriteTokens, usage.CacheWrite5mTokens, usage.CacheWrite1hTokens)
	}
	if usage.InputTokens != 11 {
		t.Errorf("输入总数 = %d，期望 5 + 2 + 4 = 11", usage.InputTokens)
	}
}
