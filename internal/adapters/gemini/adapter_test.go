package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
)

// TestEndpointFormat 守护客户端路径与上游路径的解析口径。
//
// 客户端路径携带模型名与流式后缀，上游路径由本适配器按模型名拼出；两者都只在本包表达，
// 一旦形态写错，端点注册、模型名替换与上游地址三处会同时偏。
func TestEndpointFormat(t *testing.T) {
	adapter := New()
	if got := adapter.ClientPathPrefix(); got != "/v1beta/models/" {
		t.Fatalf("客户端前缀 = %q，期望 /v1beta/models/", got)
	}

	matchCases := []struct {
		name    string
		path    string
		model   string
		stream  bool
		matched bool
	}{
		{name: "非流式", path: "/v1beta/models/gemini-2.0-flash:generateContent", model: "gemini-2.0-flash", matched: true},
		{name: "流式", path: "/v1beta/models/gemini-2.0-flash:streamGenerateContent", model: "gemini-2.0-flash", stream: true, matched: true},
		{name: "查询串不参与判定", path: "/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse", model: "gemini-2.0-flash", stream: true, matched: true},
		{name: "Vertex 的带斜杠模型名", path: "/v1beta/models/publishers/google/models/gemini:generateContent", model: "publishers/google/models/gemini", matched: true},
		{name: "前缀之外", path: "/v1/chat/completions", matched: false},
		{name: "缺少方法后缀", path: "/v1beta/models/gemini-2.0-flash", matched: false},
		{name: "未知方法", path: "/v1beta/models/gemini-2.0-flash:countTokens", matched: false},
		{name: "空模型名", path: "/v1beta/models/:generateContent", matched: false},
	}
	for _, tc := range matchCases {
		t.Run("MatchClientPath/"+tc.name, func(t *testing.T) {
			model, stream, matched := adapter.MatchClientPath(tc.path)
			if matched != tc.matched {
				t.Fatalf("命中 = %v，期望 %v", matched, tc.matched)
			}
			if !matched {
				return
			}
			if model != tc.model || stream != tc.stream {
				t.Fatalf("解析 = (%q, %v)，期望 (%q, %v)", model, stream, tc.model, tc.stream)
			}
		})
	}

	if got := adapter.UpstreamPath("up-model", false); got != "/models/up-model:generateContent" {
		t.Errorf("非流式上游路径 = %q", got)
	}
	if got := adapter.UpstreamPath("up-model", true); got != "/models/up-model:streamGenerateContent?alt=sse" {
		t.Errorf("流式上游路径 = %q", got)
	}
	if got := adapter.UpstreamPath("", true); got != "" {
		t.Errorf("空模型名应返回空路径，实际 %q", got)
	}
}

// TestDecodeRequest 守护请求体解码：contents、systemInstruction、工具与生成参数。
//
// 模型名与流式形态不在请求体里，故解码后 Model 为空、Stream 为 false，由入口层用路径补齐。
func TestDecodeRequest(t *testing.T) {
	body := []byte(`{
		"systemInstruction": {"parts": [{"text": "你是助手"}]},
		"contents": [
			{"role": "user", "parts": [{"text": "你好"}]},
			{"role": "model", "parts": [{"text": "在的"}]}
		],
		"tools": [{"functionDeclarations": [{"name": "get_weather", "description": "查天气", "parameters": {"type": "object"}}]}],
		"toolConfig": {"functionCallingConfig": {"mode": "AUTO"}},
		"generationConfig": {"maxOutputTokens": 128, "temperature": 0.7}
	}`)
	req, err := New().DecodeRequest(body)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if req.Protocol != domain.ProtocolGeminiGenerate {
		t.Errorf("协议 = %q", req.Protocol)
	}
	if req.Model != "" || req.Stream {
		t.Errorf("模型名与流式形态应由路径给出，实际 Model=%q Stream=%v", req.Model, req.Stream)
	}
	if req.MaxTokens != 128 {
		t.Errorf("maxOutputTokens = %d，期望 128", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Errorf("temperature = %v", req.Temperature)
	}
	if req.ToolChoice.Mode != domain.ToolChoiceAuto {
		t.Errorf("工具选择 = %q，期望 auto", req.ToolChoice.Mode)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Fatalf("工具定义 = %+v", req.Tools)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("消息条数 = %d，期望 3（system + user + model）", len(req.Messages))
	}
	if req.Messages[0].Role != domain.RoleSystem || req.Messages[0].Parts[0].Text != "你是助手" {
		t.Errorf("system 消息 = %+v", req.Messages[0])
	}
	if req.Messages[2].Role != domain.RoleAssistant {
		t.Errorf("model 角色应映射为 assistant，实际 %q", req.Messages[2].Role)
	}
	if !strings.HasPrefix(string(req.RawBody), "{") {
		t.Error("RawBody 应保留原始请求体")
	}
}

// TestDecodeRequestToolCallAndResponse 守护工具调用与工具结果的 id 对齐。
//
// Gemini 的 functionCall 不带 id、functionResponse 只按函数名关联；内部格式要求成对 id，
// 本适配器需合成并让结果对上同一条调用。
func TestDecodeRequestToolCallAndResponse(t *testing.T) {
	body := []byte(`{"contents": [
		{"role": "user", "parts": [{"text": "查天气"}]},
		{"role": "model", "parts": [{"functionCall": {"name": "get_weather", "args": {"city": "北京"}}}]},
		{"role": "user", "parts": [{"functionResponse": {"name": "get_weather", "response": {"temp": 25}}}]}
	]}`)
	req, err := New().DecodeRequest(body)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("消息条数 = %d，期望 3", len(req.Messages))
	}
	call := req.Messages[1].Parts[0].ToolCall
	if call == nil || call.Name != "get_weather" || call.Arguments != `{"city":"北京"}` {
		t.Fatalf("工具调用 = %+v", call)
	}
	result := req.Messages[2].Parts[0].ToolResult
	if result == nil {
		t.Fatal("工具结果缺失")
	}
	if result.ToolCallID != call.ID {
		t.Errorf("工具结果的 id = %q，期望对上调用 id %q", result.ToolCallID, call.ID)
	}
}

// TestDecodeResponse 守护非流式响应解码：文本、结束原因与用量口径。
func TestDecodeResponse(t *testing.T) {
	body := []byte(`{
		"candidates": [{"content": {"role": "model", "parts": [{"text": "hello"}]}, "finishReason": "STOP", "index": 0}],
		"usageMetadata": {"promptTokenCount": 12, "candidatesTokenCount": 7, "totalTokenCount": 24, "cachedContentTokenCount": 3, "thoughtsTokenCount": 2},
		"modelVersion": "up-model"
	}`)
	resp, err := New().DecodeResponse(body)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if resp.Model != "up-model" {
		t.Errorf("模型名 = %q", resp.Model)
	}
	if resp.FinishReason != domain.FinishStop {
		t.Errorf("结束原因 = %q，期望 stop", resp.FinishReason)
	}
	if len(resp.Message.Parts) != 1 || resp.Message.Parts[0].Text != "hello" {
		t.Fatalf("消息 = %+v", resp.Message)
	}
	usage := resp.Usage
	if usage.Source != domain.UsageSourceUpstream {
		t.Errorf("用量来源 = %q", usage.Source)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 7 {
		t.Errorf("输入/输出 = %d/%d，期望 12/7", usage.InputTokens, usage.OutputTokens)
	}
	if usage.CacheReadTokens != 3 || usage.ReasoningTokens != 2 {
		t.Errorf("缓存读/推理子项 = %d/%d，期望 3/2", usage.CacheReadTokens, usage.ReasoningTokens)
	}
}

// TestDecodeResponseUpstreamError 守护 2xx 返回错误体时的分级：请求级错误不可重试。
func TestDecodeResponseUpstreamError(t *testing.T) {
	body := []byte(`{"error": {"code": 400, "message": "API key not valid", "status": "INVALID_ARGUMENT"}}`)
	_, err := New().DecodeResponse(body)
	if err == nil {
		t.Fatal("错误体应被识别为失败")
	}
	if code := domain.AsError(err).Code; code != domain.CodeUpstreamRejected {
		t.Errorf("错误码 = %q，期望 %q", code, domain.CodeUpstreamRejected)
	}
}

// TestEncodeResponseRoundTrip 守护响应编码与非流式解码自洽：编码后再解码得到同一文本。
func TestEncodeResponseRoundTrip(t *testing.T) {
	adapter := New()
	resp := &domain.Response{
		Model:        "up-model",
		Message:      domain.Message{Role: domain.RoleAssistant, Parts: []domain.Part{{Kind: domain.PartText, Text: "hello"}}},
		FinishReason: domain.FinishStop,
		Usage: domain.Usage{
			Source:       domain.UsageSourceUpstream,
			InputTokens:  5,
			OutputTokens: 3,
		},
	}
	body, err := adapter.EncodeResponse(resp)
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	decoded, err := adapter.DecodeResponse(body)
	if err != nil {
		t.Fatalf("回解失败：%v", err)
	}
	if decoded.Message.Parts[0].Text != "hello" {
		t.Errorf("文本 = %q", decoded.Message.Parts[0].Text)
	}
	if decoded.Usage.InputTokens != 5 || decoded.Usage.OutputTokens != 3 {
		t.Errorf("用量 = %+v", decoded.Usage)
	}
}

// TestEncodeError 守护方言错误体形态。
func TestEncodeError(t *testing.T) {
	status, body := New().EncodeError(domain.NewError(domain.CodeInvalidRequest, "缺少 contents"))
	if status != 400 {
		t.Fatalf("状态码 = %d，期望 400", status)
	}
	var envelope struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("错误体不是合法 JSON：%v，原文 %s", err, body)
	}
	if envelope.Error.Status != "INVALID_ARGUMENT" || envelope.Error.Code != 400 {
		t.Errorf("错误体 = %+v", envelope.Error)
	}
}
