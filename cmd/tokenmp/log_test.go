package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/transport"
)

// allowedLogKeys 是请求日志允许出现的全部键。任何新增键都必须显式登记到这里，
// 以免凭据、API key 或 Authorization 头经某个新字段混进日志。
var allowedLogKeys = map[string]struct{}{
	"time":            {},
	"level":           {},
	"msg":             {},
	"request_id":      {},
	"protocol":        {},
	"model":           {},
	"channel_id":      {},
	"upstream_status": {},
	"http_status":     {},
	"duration_ms":     {},
}

// TestAccessLoggerWritesOneJSONLineWithExpectedFields 断言每请求一条 JSON 行，字段齐全。
func TestAccessLoggerWritesOneJSONLineWithExpectedFields(t *testing.T) {
	var buf bytes.Buffer
	logger := newAccessLogger(&buf)
	logger.LogAccess(transport.AccessRecord{
		RequestID:      "req-123",
		Protocol:       domain.ProtocolOpenAIChat,
		Model:          "glm-5",
		ChannelID:      42,
		UpstreamStatus: 200,
		HTTPStatus:     200,
		DurationMS:     12,
	})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("应写出恰好一行 JSON，实际 %d 行：%q", len(lines), buf.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &fields); err != nil {
		t.Fatalf("日志行不是 JSON：%v，原文 %s", err, lines[0])
	}
	want := map[string]string{
		"request_id":      `"req-123"`,
		"protocol":        `"openai_chat"`,
		"model":           `"glm-5"`,
		"channel_id":      `42`,
		"upstream_status": `200`,
		"http_status":     `200`,
		"duration_ms":     `12`,
	}
	for key, value := range want {
		if got := string(fields[key]); got != value {
			t.Errorf("%s = %s，期望 %s", key, got, value)
		}
	}
	for key := range fields {
		if _, ok := allowedLogKeys[key]; !ok {
			t.Errorf("日志出现未登记字段 %q：新增字段必须显式确认不含敏感信息", key)
		}
	}
}

// TestAccessLoggerOmitsSensitiveValues 断言日志正文不含凭据类字面量。
func TestAccessLoggerOmitsSensitiveValues(t *testing.T) {
	var buf bytes.Buffer
	logger := newAccessLogger(&buf)
	logger.LogAccess(transport.AccessRecord{
		RequestID:      "req-1",
		Protocol:       domain.ProtocolAnthropicMessages,
		Model:          "claude",
		ChannelID:      7,
		UpstreamStatus: 401,
		HTTPStatus:     502,
		DurationMS:     5,
	})
	lower := strings.ToLower(buf.String())
	for _, forbidden := range []string{"authorization", "api_key", "apikey", "bearer", "x-api-key", "secret"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("日志不应出现 %q：%s", forbidden, buf.String())
		}
	}
}
