package openaichat

import (
	"encoding/json"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
)

// decodeFields 把请求体解成顶层字段表，供断言改写结果。
func decodeFields(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("请求体不是 JSON 对象：%v，原文 %s", err, body)
	}
	return fields
}

// TestRewriteRawBodyInjectsUsageSwitch 验证流式请求被补上 stream_options.include_usage。
//
// 不注入就拿不到上游末尾的用量帧，网关的用量流水会是空的。
func TestRewriteRawBodyInjectsUsageSwitch(t *testing.T) {
	adapter := New()
	body := []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	rewritten, parts, err := adapter.RewriteRawBody(body, domain.RewriteOptions{
		UpstreamModel: "up-model",
		IncludeUsage:  true,
	})
	if err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	if !parts.Has(domain.RewritePartRequestModel) {
		t.Errorf("应标注模型名改写，得到 %v", parts)
	}
	if !parts.Has(domain.RewritePartRequestUsageSwitch) {
		t.Errorf("应标注索取用量开关，得到 %v", parts)
	}
	fields := decodeFields(t, rewritten)
	var options map[string]json.RawMessage
	if err := json.Unmarshal(fields["stream_options"], &options); err != nil {
		t.Fatalf("stream_options 不是对象：%v", err)
	}
	if string(options["include_usage"]) != "true" {
		t.Errorf("include_usage = %s，期望 true", options["include_usage"])
	}
}

// TestRewriteRawBodyMergesExistingStreamOptions 验证已有其它流式选项时逐键保留，只补开关。
func TestRewriteRawBodyMergesExistingStreamOptions(t *testing.T) {
	adapter := New()
	body := []byte(`{"model":"alias","stream":true,"stream_options":{"foo":"bar"},"messages":[{"role":"user","content":"hi"}]}`)
	rewritten, parts, err := adapter.RewriteRawBody(body, domain.RewriteOptions{IncludeUsage: true})
	if err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	if !parts.Has(domain.RewritePartRequestUsageSwitch) {
		t.Errorf("应标注索取用量开关，得到 %v", parts)
	}
	fields := decodeFields(t, rewritten)
	var options map[string]json.RawMessage
	if err := json.Unmarshal(fields["stream_options"], &options); err != nil {
		t.Fatalf("stream_options 不是对象：%v", err)
	}
	if string(options["foo"]) != `"bar"` {
		t.Errorf("已有选项应保留，得到 %s", options["foo"])
	}
	if string(options["include_usage"]) != "true" {
		t.Errorf("include_usage = %s，期望 true", options["include_usage"])
	}
}

// TestRewriteRawBodyUsageSwitchIdempotent 验证开关已是 true 时不改写也不标注。
func TestRewriteRawBodyUsageSwitchIdempotent(t *testing.T) {
	adapter := New()
	body := []byte(`{"model":"alias","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)
	rewritten, parts, err := adapter.RewriteRawBody(body, domain.RewriteOptions{IncludeUsage: true})
	if err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	if len(parts) != 0 {
		t.Errorf("标志已置位时不应标注任何改写，得到 %v", parts)
	}
	if string(rewritten) != string(body) {
		t.Errorf("未发生改写时应逐字节返回入参：\n实际 %s\n期望 %s", rewritten, body)
	}
}

// TestRewriteRawBodyUsageSwitchSkippedForNonStream 验证非流式请求不注入：该开关对非流式无意义。
func TestRewriteRawBodyUsageSwitchSkippedForNonStream(t *testing.T) {
	adapter := New()
	body := []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	rewritten, parts, err := adapter.RewriteRawBody(body, domain.RewriteOptions{IncludeUsage: true})
	if err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	if len(parts) != 0 {
		t.Errorf("非流式请求不应标注改写，得到 %v", parts)
	}
	if string(rewritten) != string(body) {
		t.Errorf("未发生改写时应逐字节返回入参：\n实际 %s\n期望 %s", rewritten, body)
	}
}

// TestRewriteRawBodyAppliesRequestOverridesAfterModelRewrite 验证覆盖项按顶层键覆盖并入请求体。
func TestRewriteRawBodyAppliesRequestOverridesAfterModelRewrite(t *testing.T) {
	adapter := New()
	body := []byte(`{"model":"alias","temperature":0.5,"messages":[{"role":"user","content":"hi"}]}`)
	rewritten, parts, err := adapter.RewriteRawBody(body, domain.RewriteOptions{
		UpstreamModel:    "up-model",
		RequestOverrides: json.RawMessage(`{"temperature":0.2,"top_p":0.9}`),
	})
	if err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	if !parts.Has(domain.RewritePartRequestOverrides) {
		t.Errorf("应标注覆盖项改写，得到 %v", parts)
	}
	fields := decodeFields(t, rewritten)
	if string(fields["temperature"]) != "0.2" {
		t.Errorf("temperature = %s，期望 0.2", fields["temperature"])
	}
	if string(fields["top_p"]) != "0.9" {
		t.Errorf("top_p = %s，期望 0.9", fields["top_p"])
	}
	if string(fields["model"]) != `"up-model"` {
		t.Errorf("model = %s，期望 up-model", fields["model"])
	}
}

// TestRewriteRawBodyOverridesCoexistWithUsageSwitch 验证覆盖项与用量索取开关同帧落地，互不覆盖。
func TestRewriteRawBodyOverridesCoexistWithUsageSwitch(t *testing.T) {
	adapter := New()
	body := []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	rewritten, parts, err := adapter.RewriteRawBody(body, domain.RewriteOptions{
		IncludeUsage:     true,
		RequestOverrides: json.RawMessage(`{"temperature":0.3}`),
	})
	if err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	if !parts.Has(domain.RewritePartRequestUsageSwitch) || !parts.Has(domain.RewritePartRequestOverrides) {
		t.Errorf("应同时标注两项改写，得到 %v", parts)
	}
	fields := decodeFields(t, rewritten)
	if string(fields["temperature"]) != "0.3" {
		t.Errorf("temperature = %s，期望 0.3", fields["temperature"])
	}
	var options map[string]json.RawMessage
	if err := json.Unmarshal(fields["stream_options"], &options); err != nil {
		t.Fatalf("stream_options 不是对象：%v", err)
	}
	if string(options["include_usage"]) != "true" {
		t.Errorf("include_usage = %s，期望 true", options["include_usage"])
	}
}

// TestRewriteRawBodySkipsInvalidOverrides 验证非法覆盖项不中断转发也不改动请求体。
func TestRewriteRawBodySkipsInvalidOverrides(t *testing.T) {
	adapter := New()
	body := []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	rewritten, parts, err := adapter.RewriteRawBody(body, domain.RewriteOptions{
		RequestOverrides: json.RawMessage(`[1,2]`),
	})
	if err != nil {
		t.Fatalf("非法覆盖项不应中断转发：%v", err)
	}
	if len(parts) != 0 {
		t.Errorf("无效覆盖项不应产生改写标注，得到 %v", parts)
	}
	if string(rewritten) != string(body) {
		t.Errorf("未发生改写时应逐字节返回入参：\n实际 %s\n期望 %s", rewritten, body)
	}
}

// TestEncodeRequestAppliesRequestOverrides 验证跨协议重建路径同样应用覆盖项。
func TestEncodeRequestAppliesRequestOverrides(t *testing.T) {
	adapter := New()
	req := &domain.Request{
		Protocol: domain.ProtocolAnthropicMessages,
		Model:    "alias",
		Messages: []domain.Message{{
			Role:  domain.RoleUser,
			Parts: []domain.Part{{Kind: domain.PartText, Text: "hi"}},
		}},
	}
	body, parts, err := adapter.EncodeRequest(req, domain.RewriteOptions{
		UpstreamModel:    "up-model",
		RequestOverrides: json.RawMessage(`{"temperature":0.4}`),
	})
	if err != nil {
		t.Fatalf("重建失败：%v", err)
	}
	if !parts.Has(domain.RewritePartRequestOverrides) {
		t.Errorf("应标注覆盖项改写，得到 %v", parts)
	}
	fields := decodeFields(t, body)
	if string(fields["temperature"]) != "0.4" {
		t.Errorf("temperature = %s，期望 0.4", fields["temperature"])
	}
}

// TestEncodeRequestInjectsUsageSwitch 验证跨协议重建的上游请求同样带上用量索取开关。
func TestEncodeRequestInjectsUsageSwitch(t *testing.T) {
	adapter := New()
	req := &domain.Request{
		Protocol: domain.ProtocolAnthropicMessages,
		Model:    "alias",
		Stream:   true,
		Messages: []domain.Message{{
			Role:  domain.RoleUser,
			Parts: []domain.Part{{Kind: domain.PartText, Text: "hi"}},
		}},
	}
	body, parts, err := adapter.EncodeRequest(req, domain.RewriteOptions{UpstreamModel: "up-model", IncludeUsage: true})
	if err != nil {
		t.Fatalf("重建失败：%v", err)
	}
	if !parts.Has(domain.RewritePartRequestUsageSwitch) {
		t.Errorf("应标注索取用量开关，得到 %v", parts)
	}
	fields := decodeFields(t, body)
	var options map[string]json.RawMessage
	if err := json.Unmarshal(fields["stream_options"], &options); err != nil {
		t.Fatalf("stream_options 不是对象：%v", err)
	}
	if string(options["include_usage"]) != "true" {
		t.Errorf("include_usage = %s，期望 true", options["include_usage"])
	}
}
