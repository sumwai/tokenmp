package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// geminiRecordedUpstream 记录假上游收到的一次调用：路径、查询参数与凭据头。
type geminiRecordedUpstream struct {
	path    string
	rawQuer string
	header  http.Header
	body    []byte
}

// geminiUpstream 是 Gemini 端点的进程内假上游：按路径回固定的 Gemini 报文，
// 并记录收到的路径、查询参数与凭据头。
type geminiUpstream struct {
	server *httptest.Server
	// stream 为 true 时响应 SSE，否则回非流式 JSON。
	stream bool

	mu      sync.Mutex
	records []geminiRecordedUpstream
}

// newGeminiUpstream 起一个假上游并注册关闭。
func newGeminiUpstream(t *testing.T, stream bool) *geminiUpstream {
	t.Helper()
	upstream := &geminiUpstream{stream: stream}
	upstream.server = httptest.NewServer(http.HandlerFunc(upstream.handle))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func (u *geminiUpstream) handle(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "读取请求体失败", http.StatusInternalServerError)
		return
	}
	u.mu.Lock()
	u.records = append(u.records, geminiRecordedUpstream{
		path:    r.URL.Path,
		rawQuer: r.URL.RawQuery,
		header:  r.Header.Clone(),
		body:    raw,
	})
	u.mu.Unlock()

	if u.stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, geminiStreamFrames)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, geminiResponseBody)
}

// last 返回最近一次调用记录。
func (u *geminiUpstream) last(t *testing.T) geminiRecordedUpstream {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.records) == 0 {
		t.Fatal("假上游未收到任何调用")
	}
	return u.records[len(u.records)-1]
}

// 假上游的固定应答：正文 hello、用量 5/3。
const (
	geminiResponseBody = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8},"modelVersion":"up-model"}`
	geminiStreamFrames = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]},\"index\":0}],\"modelVersion\":\"up-model\"}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":3},\"modelVersion\":\"up-model\"}\n\n"
)

// geminiChannel 构造一条 Gemini 渠道候选；config 为空表示不配置注入形态。
func geminiChannel(baseURL string, config string) store.RouteCandidate {
	candidate := store.RouteCandidate{
		ChannelID:     10,
		ChannelType:   store.ChannelTypeGeminiGenerate,
		BaseURL:       baseURL,
		CredGroup:     "group-a",
		UpstreamModel: "up-model",
	}
	if config != "" {
		candidate.Config = []byte(config)
	}
	return candidate
}

// geminiTestStore 构造只含一条 Gemini 渠道或跨协议渠道的内存存储。
func geminiTestStore(candidate store.RouteCandidate, sameProtocol bool) *fakeGatewayStore {
	st := &fakeGatewayStore{
		auth:           activeAuth(),
		credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		wantMerchantID: 1,
	}
	if sameProtocol {
		st.routes = []store.RouteCandidate{candidate}
	} else {
		st.crossRoutes = []store.RouteCandidate{candidate}
	}
	return st
}

// TestGatewayGeminiNonStreaming 守护 Gemini 同协议非流式的端到端：
// 模型名在路径上替换、凭据走 x-goog-api-key、响应逐字节透传。
func TestGatewayGeminiNonStreaming(t *testing.T) {
	upstream := newGeminiUpstream(t, false)
	st := geminiTestStore(geminiChannel(upstream.server.URL, ""), true)
	server := httptest.NewServer(newTestGateway(t, st).handler)
	defer server.Close()

	result := doPost(t, server.URL+"/v1beta/models/alias:generateContent", authSchemePrefix+testAPIKey,
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if string(result.body) != geminiResponseBody {
		t.Errorf("同协议非流式应逐字节透传上游响应：\n实际 %s\n期望 %s", result.body, geminiResponseBody)
	}

	recorded := upstream.last(t)
	if recorded.path != "/models/up-model:generateContent" {
		t.Errorf("上游路径 = %q，期望 /models/up-model:generateContent", recorded.path)
	}
	if got := recorded.header.Get("x-goog-api-key"); got != "sk-upstream" {
		t.Errorf("x-goog-api-key = %q，期望 sk-upstream", got)
	}
	if strings.Contains(string(recorded.body), "up-model") {
		t.Errorf("模型名由路径携带，请求体里不应出现模型名：%s", recorded.body)
	}
}

// TestGatewayGeminiStreaming 守护 Gemini 同协议流式：路径带 :streamGenerateContent?alt=sse，
// 上游 SSE 帧原样透传给客户端。
func TestGatewayGeminiStreaming(t *testing.T) {
	upstream := newGeminiUpstream(t, true)
	st := geminiTestStore(geminiChannel(upstream.server.URL, ""), true)
	server := httptest.NewServer(newTestGateway(t, st).handler)
	defer server.Close()

	result := doPost(t, server.URL+"/v1beta/models/alias:streamGenerateContent?alt=sse", authSchemePrefix+testAPIKey,
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if result.contentType != "text/event-stream" {
		t.Errorf("Content-Type = %q，期望 text/event-stream", result.contentType)
	}
	if !strings.Contains(string(result.body), "hello") || !strings.Contains(string(result.body), "STOP") {
		t.Errorf("透传的 SSE 应含正文与结束原因，实际 %s", result.body)
	}

	recorded := upstream.last(t)
	if recorded.path != "/models/up-model:streamGenerateContent" {
		t.Errorf("上游路径 = %q", recorded.path)
	}
	query, err := url.ParseQuery(recorded.rawQuer)
	if err != nil {
		t.Fatalf("解析上游查询串失败：%v", err)
	}
	if query.Get("alt") != "sse" {
		t.Errorf("上游查询串应带 alt=sse，实际 %q", recorded.rawQuer)
	}
}

// TestGatewayGeminiQueryCredentialStyle 守护渠道 config 把凭据切到查询参数形态：
// 上游收到 ?key=，且不再出现凭据请求头。
func TestGatewayGeminiQueryCredentialStyle(t *testing.T) {
	upstream := newGeminiUpstream(t, false)
	st := geminiTestStore(geminiChannel(upstream.server.URL, `{"credential_style":"query"}`), true)
	server := httptest.NewServer(newTestGateway(t, st).handler)
	defer server.Close()

	result := doPost(t, server.URL+"/v1beta/models/alias:generateContent", authSchemePrefix+testAPIKey,
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	recorded := upstream.last(t)
	query, err := url.ParseQuery(recorded.rawQuer)
	if err != nil {
		t.Fatalf("解析上游查询串失败：%v", err)
	}
	if query.Get("key") != "sk-upstream" {
		t.Errorf("查询参数 key = %q，期望 sk-upstream", query.Get("key"))
	}
	if got := recorded.header.Get("x-goog-api-key"); got != "" {
		t.Errorf("查询参数形态不应注入凭据头，实际 %q", got)
	}
}

// TestGatewayCrossProtocolToGemini 守护客户端 OpenAI Chat、渠道 Gemini 的跨协议重建：
// 上游收到 Gemini 形态的 contents，客户端拿到 Chat 形态的响应。
func TestGatewayCrossProtocolToGemini(t *testing.T) {
	upstream := newGeminiUpstream(t, false)
	st := geminiTestStore(geminiChannel(upstream.server.URL, ""), false)
	server := httptest.NewServer(newTestGateway(t, st).handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(), authSchemePrefix+testAPIKey,
		`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if !strings.Contains(string(result.body), `"content":"hello"`) {
		t.Errorf("客户端应拿到 Chat 形态响应，实际 %s", result.body)
	}
	recorded := upstream.last(t)
	if !strings.Contains(string(recorded.body), `"contents"`) {
		t.Errorf("上游请求应按 Gemini 重建，实际 %s", recorded.body)
	}
	if got := recorded.header.Get("x-goog-api-key"); got != "sk-upstream" {
		t.Errorf("x-goog-api-key = %q，期望 sk-upstream", got)
	}
}

// TestGatewayCrossProtocolFromGemini 守护客户端 Gemini、渠道 OpenAI Chat 的跨协议重建：
// 上游收到 Chat 形态请求，客户端拿到 Gemini 形态响应。
func TestGatewayCrossProtocolFromGemini(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`)
	}))
	defer upstream.Close()

	candidate := store.RouteCandidate{
		ChannelID: 20, ChannelType: store.ChannelTypeOpenAIChat,
		BaseURL: upstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
	}
	server := httptest.NewServer(newTestGateway(t, geminiTestStore(candidate, false)).handler)
	defer server.Close()

	result := doPost(t, server.URL+"/v1beta/models/alias:generateContent", authSchemePrefix+testAPIKey,
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if !strings.Contains(string(result.body), `"candidates"`) || !strings.Contains(string(result.body), `"text":"hello"`) {
		t.Errorf("客户端应拿到 Gemini 形态响应，实际 %s", result.body)
	}
}

// TestGatewayGeminiPathNotRegistered 守护其它路径仍回未注册路径的 JSON 404，不被前缀匹配误收。
func TestGatewayGeminiPathNotRegistered(t *testing.T) {
	st := &fakeGatewayStore{auth: activeAuth(), wantMerchantID: 1}
	server := httptest.NewServer(newTestGateway(t, st).handler)
	defer server.Close()

	for _, path := range []string{"/v1beta/models/gemini:countTokens", "/v1beta/models", "/v1beta/models/"} {
		result := doPost(t, server.URL+path, authSchemePrefix+testAPIKey, `{}`)
		if result.status != http.StatusNotFound {
			t.Errorf("路径 %s 状态码 = %d，期望 404", path, result.status)
		}
	}
}
