package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

// allowedAttemptLogKeys 是尝试日志允许出现的全部键。任何新增键都必须显式登记到这里，
// 以免凭据、api key 或 Authorization 头经某个新字段混进日志。
var allowedAttemptLogKeys = map[string]struct{}{
	"time":                  {},
	"level":                 {},
	"msg":                   {},
	"request_id":            {},
	"attempt":               {},
	"retry":                 {},
	"channel_id":            {},
	"channel_switched":      {},
	"credential_switched":   {},
	"protocol":              {},
	"upstream_protocol":     {},
	"protocol_switched":     {},
	"requested_model":       {},
	"upstream_model":        {},
	"outcome":               {},
	"upstream_status":       {},
	"duration_ms":           {},
	"error_code":            {},
	"error_detail":          {},
	"rewritten_parts":       {},
	"usage_source":          {},
	"input_tokens":          {},
	"output_tokens":         {},
	"cache_read_tokens":     {},
	"cache_write_tokens":    {},
	"cache_write_5m_tokens": {},
	"cache_write_1h_tokens": {},
	"reasoning_tokens":      {},
	"server_tool_uses":      {},
}

// attemptLogFields 解析一行 JSON 日志为键到原始取值的映射。
func attemptLogFields(t *testing.T, buf *bytes.Buffer) map[string]json.RawMessage {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("应写出恰好一行 JSON，实际 %d 行：%q", len(lines), buf.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &fields); err != nil {
		t.Fatalf("尝试日志不是 JSON：%v，原文 %s", err, lines[0])
	}
	return fields
}

// TestAttemptObserverWritesFullFacts 断言一次失败尝试的全部事实字段齐全且取值正确。
func TestAttemptObserverWritesFullFacts(t *testing.T) {
	var buf bytes.Buffer
	start := time.Unix(0, 0)
	rec := domain.AttemptRecord{
		RequestID:        "req-1",
		Attempt:          2,
		ClientProtocol:   domain.ProtocolOpenAIChat,
		UpstreamProtocol: domain.ProtocolAnthropicMessages,
		RequestedModel:   "alias",
		UpstreamID:       "10",
		ChannelID:        42,
		UpstreamModel:    "up-model",
		Outcome:          domain.AttemptFailed,
		UpstreamStatus:   401,
		ChannelSwitched:  true,
		ErrorCode:        string(domain.CodeUpstreamRejected),
		ErrorDetail:      "上游 HTTP 状态码 401：invalid api key",
		StartedAt:        start,
		EndedAt:          start.Add(1500 * time.Millisecond),
		RewrittenParts:   domain.RewriteParts{domain.RewritePartRequestModel},
		Usage: domain.Usage{
			Source:          domain.UsageSourceUpstream,
			InputTokens:     17,
			OutputTokens:    74,
			CacheReadTokens: 4,
			ReasoningTokens: 61,
			ServerToolUses:  2,
		},
	}
	if err := newAttemptObserver(newJSONLogger(&buf)).RecordAttempt(context.Background(), rec); err != nil {
		t.Fatalf("记录尝试日志不得返回错误：%v", err)
	}

	fields := attemptLogFields(t, &buf)
	want := map[string]string{
		"request_id":            `"req-1"`,
		"attempt":               `2`,
		"retry":                 `true`,
		"channel_id":            `42`,
		"channel_switched":      `true`,
		"credential_switched":   `false`,
		"protocol":              `"openai_chat"`,
		"upstream_protocol":     `"anthropic_messages"`,
		"protocol_switched":     `true`,
		"requested_model":       `"alias"`,
		"upstream_model":        `"up-model"`,
		"outcome":               `"failed"`,
		"upstream_status":       `401`,
		"duration_ms":           `1500`,
		"error_code":            `"upstream_rejected"`,
		"usage_source":          `"upstream"`,
		"input_tokens":          `17`,
		"output_tokens":         `74`,
		"cache_read_tokens":     `4`,
		"cache_write_tokens":    `0`,
		"cache_write_5m_tokens": `0`,
		"cache_write_1h_tokens": `0`,
		"reasoning_tokens":      `61`,
		"server_tool_uses":      `2`,
	}
	for key, value := range want {
		if got := string(fields[key]); got != value {
			t.Errorf("%s = %s，期望 %s", key, got, value)
		}
	}
	if !strings.Contains(string(fields["error_detail"]), "401") {
		t.Errorf("error_detail 应保留上游排障细节：%s", fields["error_detail"])
	}
	for key := range fields {
		if _, ok := allowedAttemptLogKeys[key]; !ok {
			t.Errorf("尝试日志出现未登记字段 %q：新增字段必须显式确认不含敏感信息", key)
		}
	}
}

// TestAttemptObserverMarksCredentialSwitch 守护「同渠道内重试即凭据轮换」的判定：
// 渠道未切换时的重试只可能来自凭据轮换，本字段让消费方不必自行跟踪上一条记录。
func TestAttemptObserverMarksCredentialSwitch(t *testing.T) {
	var buf bytes.Buffer
	rec := domain.AttemptRecord{
		RequestID:       "req-2",
		Attempt:         2,
		ChannelID:       7,
		Outcome:         domain.AttemptFailed,
		ChannelSwitched: false,
	}
	if err := newAttemptObserver(newJSONLogger(&buf)).RecordAttempt(context.Background(), rec); err != nil {
		t.Fatalf("记录尝试日志不得返回错误：%v", err)
	}
	fields := attemptLogFields(t, &buf)
	if got := string(fields["channel_switched"]); got != `false` {
		t.Errorf("channel_switched = %s，期望 false", got)
	}
	if got := string(fields["credential_switched"]); got != `true` {
		t.Errorf("同渠道内重试应标记 credential_switched=true，实际 %s", got)
	}
}

// TestAttemptObserverUnknownUsageLogsOnlySource 守护「上游没给用量」的表达：
// 只打 usage_source=unknown，不打计数。把零值计数照打出来会被误读成「上游报了 0」。
func TestAttemptObserverUnknownUsageLogsOnlySource(t *testing.T) {
	var buf bytes.Buffer
	rec := domain.AttemptRecord{RequestID: "req-3", Attempt: 1, Outcome: domain.AttemptOK}
	if err := newAttemptObserver(newJSONLogger(&buf)).RecordAttempt(context.Background(), rec); err != nil {
		t.Fatalf("记录尝试日志不得返回错误：%v", err)
	}
	fields := attemptLogFields(t, &buf)
	if got := string(fields["usage_source"]); got != `"unknown"` {
		t.Errorf("未取得用量时 usage_source = %s，期望 \"unknown\"", got)
	}
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens"} {
		if _, ok := fields[key]; ok {
			t.Errorf("未取得用量时不应打出计数 %q", key)
		}
	}
}

// TestAttemptObserverOmitsSensitiveValues 断言日志正文不含凭据类字面量。
func TestAttemptObserverOmitsSensitiveValues(t *testing.T) {
	var buf bytes.Buffer
	rec := domain.AttemptRecord{
		RequestID:      "req-4",
		Attempt:        1,
		ChannelID:      7,
		Outcome:        domain.AttemptFailed,
		UpstreamStatus: 401,
		ErrorCode:      string(domain.CodeUpstreamRejected),
	}
	if err := newAttemptObserver(newJSONLogger(&buf)).RecordAttempt(context.Background(), rec); err != nil {
		t.Fatalf("记录尝试日志不得返回错误：%v", err)
	}
	lower := strings.ToLower(buf.String())
	for _, forbidden := range []string{"authorization", "api_key", "apikey", "bearer", "x-api-key", "secret"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("尝试日志不应出现 %q：%s", forbidden, buf.String())
		}
	}
}

// TestAttemptObserverNilLoggerIsSafe 守护未注入日志器时的空实现：
// 装配层可能不配尝试日志，此时记录不得 panic。
func TestAttemptObserverNilLoggerIsSafe(t *testing.T) {
	if err := newAttemptObserver(nil).RecordAttempt(context.Background(), domain.AttemptRecord{}); err != nil {
		t.Errorf("未配置日志器时不得返回错误：%v", err)
	}
	var nilObserver *attemptObserver
	if err := nilObserver.RecordAttempt(context.Background(), domain.AttemptRecord{}); err != nil {
		t.Errorf("nil 接收者不得返回错误：%v", err)
	}
}

// failingObserver 是一个恒返回错误的观测器，用于验证实现内部的失败不回传转发链。
type failingObserver struct{}

func (failingObserver) RecordAttempt(context.Context, domain.AttemptRecord) error {
	return errors.New("观测后端不可用")
}
