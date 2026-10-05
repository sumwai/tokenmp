package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// testAPIKey 是测试客户端的明文密钥；库中存的是它的 SHA-256 hex。
const testAPIKey = "test-client-key"

// fakeGatewayStore 是 gatewayStore 的内存替身。
//
// wantMerchantID 非零时只在该商家下返回候选与凭据：鉴权没把商家带进上下文时，
// 端到端用例会因无候选而失败，这比另建一处断言的覆盖更直接。
type fakeGatewayStore struct {
	auth           *store.APIKeyAuth
	routes         []store.RouteCandidate
	credentials    []store.Credential
	wantMerchantID uint64

	// mu 保护用量记录：流式请求下 InsertUsage 在服务端 goroutine 里被调用。
	mu sync.Mutex
	// usageRows 按写入顺序保存流水行，供端到端用例断言分量。
	usageRows []store.UsageRow
	// insertErr 非 nil 时 InsertUsage 返回它，用于验证落库失败不影响转发。
	insertErr error
}

func (f *fakeGatewayStore) LookupAPIKey(_ context.Context, keyHash string, _ time.Time) (*store.APIKeyAuth, error) {
	if keyHash != hashAPIKey(testAPIKey) {
		return nil, fmt.Errorf("store: 查询 account_api_key 失败: %w", sql.ErrNoRows)
	}
	return f.auth, nil
}

func (f *fakeGatewayStore) RouteCandidates(_ context.Context, _ store.ChannelType, _ string, merchantID uint64) ([]store.RouteCandidate, error) {
	if f.wantMerchantID != 0 && merchantID != f.wantMerchantID {
		return nil, nil
	}
	return f.routes, nil
}

func (f *fakeGatewayStore) CredentialsByGroup(_ context.Context, _ string, merchantID uint64) ([]store.Credential, error) {
	if f.wantMerchantID != 0 && merchantID != f.wantMerchantID {
		return nil, nil
	}
	return f.credentials, nil
}

func (f *fakeGatewayStore) InsertUsage(_ context.Context, row store.UsageRow) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	f.usageRows = append(f.usageRows, row)
	return uint64(len(f.usageRows)), nil
}

// usageSnapshot 返回已写入流水行的一份拷贝。
func (f *fakeGatewayStore) usageSnapshot() []store.UsageRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.UsageRow(nil), f.usageRows...)
}

// activeAuth 返回一份鉴权通过的最小事实。
func activeAuth() *store.APIKeyAuth {
	return &store.APIKeyAuth{APIKeyID: 1, AccountID: 2, AccountStatus: accountStatusActive, MerchantID: 1}
}

// newTestGateway 装配网关；装配失败即让用例失败。
func newTestGateway(t *testing.T, st gatewayStore) *gateway {
	t.Helper()
	gw, err := newGateway(st, gatewayOptions{UpstreamTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("装配网关失败：%v", err)
	}
	t.Cleanup(gw.Close)
	return gw
}

// httpResult 是一次客户端调用的结果快照。
//
// 响应体在这里就被读完并关闭，不把 *http.Response 交给用例：
// 让用例拿着未关闭的响应体是 bodyclose 一类泄漏的常见形态。
type httpResult struct {
	status      int
	contentType string
	body        []byte
}

// doPost 向网关发一次 POST，返回结果快照。
func doPost(t *testing.T, url, authorization, body string) httpResult {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set(authorizationHeader, authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("发送请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	return httpResult{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), body: respBody}
}

// assertJSONError 断言响应是期望状态码的 JSON，且带统一错误体的 error 字段。
func assertJSONError(t *testing.T, result httpResult, wantStatus int) {
	t.Helper()
	if result.status != wantStatus {
		t.Fatalf("状态码 = %d，期望 %d，响应体 %s", result.status, wantStatus, result.body)
	}
	if !strings.HasPrefix(result.contentType, jsonContentType) {
		t.Errorf("Content-Type = %q，期望 %q 前缀", result.contentType, jsonContentType)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result.body, &envelope); err != nil {
		t.Fatalf("响应体不是 JSON：%v，原文 %s", err, result.body)
	}
	if _, ok := envelope["error"]; !ok {
		t.Errorf("响应体缺少 error 字段：%s", result.body)
	}
}

// recordedUpstream 是模拟上游收到的一次请求的快照。
type recordedUpstream struct {
	path    string
	headers http.Header
	model   string
	// includeUsage 记录上游请求体是否带了用量索取开关。
	includeUsage bool
}

// TestGatewayForwardingThreeDialects 覆盖三方言的非流式端到端往返。
//
// 每条用例断言四件事：上游收到的是拼好的端点段、模型名已替换为 upstream_model、
// 凭据头按方言注入、上游响应逐字节回写给客户端。
func TestGatewayForwardingThreeDialects(t *testing.T) {
	tests := []struct {
		name         string
		protocol     domain.Protocol
		requestBody  string
		responseBody string
		wantHeader   string
		wantValue    string
	}{
		{
			name:         "openai_chat",
			protocol:     domain.ProtocolOpenAIChat,
			requestBody:  `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`,
			responseBody: `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantHeader:   "Authorization",
			wantValue:    "Bearer sk-upstream",
		},
		{
			name:         "openai_responses",
			protocol:     domain.ProtocolOpenAIResponses,
			requestBody:  `{"model":"alias","input":"hi"}`,
			responseBody: `{"id":"resp_1","model":"up-model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`,
			wantHeader:   "Authorization",
			wantValue:    "Bearer sk-upstream",
		},
		{
			name:         "anthropic_messages",
			protocol:     domain.ProtocolAnthropicMessages,
			requestBody:  `{"model":"alias","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			responseBody: `{"id":"msg_1","type":"message","role":"assistant","model":"up-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`,
			wantHeader:   "x-api-key",
			wantValue:    "sk-upstream",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := make(chan recordedUpstream, 1)
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var fields map[string]any
				_ = json.Unmarshal(raw, &fields)
				model, _ := fields["model"].(string)
				records <- recordedUpstream{path: r.URL.Path, headers: r.Header.Clone(), model: model}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tt.responseBody)
			}))
			defer upstreamServer.Close()

			st := &fakeGatewayStore{
				auth: activeAuth(),
				routes: []store.RouteCandidate{{
					ChannelID: 10, BaseURL: upstreamServer.URL, CredGroup: "group-a", UpstreamModel: "up-model",
				}},
				credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
				wantMerchantID: 1,
			}
			gatewayServer := httptest.NewServer(newTestGateway(t, st).handler)
			defer gatewayServer.Close()

			result := doPost(t, gatewayServer.URL+tt.protocol.EndpointPath(), authSchemePrefix+testAPIKey, tt.requestBody)
			if result.status != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
			}
			if string(result.body) != tt.responseBody {
				t.Errorf("响应体应逐字节透传上游：\n实际 %s\n期望 %s", result.body, tt.responseBody)
			}

			recorded := <-records
			if recorded.path != tt.protocol.EndpointSegment() {
				t.Errorf("上游路径 = %q，期望 %q", recorded.path, tt.protocol.EndpointSegment())
			}
			if recorded.model != "up-model" {
				t.Errorf("上游模型名 = %q，期望 up-model", recorded.model)
			}
			if got := recorded.headers.Get(tt.wantHeader); got != tt.wantValue {
				t.Errorf("%s = %q，期望 %q", tt.wantHeader, got, tt.wantValue)
			}
			if tt.protocol == domain.ProtocolAnthropicMessages {
				if got := recorded.headers.Get("anthropic-version"); got != "2023-06-01" {
					t.Errorf("anthropic-version = %q，期望 2023-06-01", got)
				}
				if got := recorded.headers.Get("Authorization"); got != "" {
					t.Errorf("anthropic 不应带 Authorization，实际 %q", got)
				}
			} else if got := recorded.headers.Get("x-api-key"); got != "" {
				t.Errorf("openai 不应带 x-api-key，实际 %q", got)
			}
		})
	}
}

// TestGatewayHealthz 断言健康检查不鉴权且固定 200。
func TestGatewayHealthz(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{}).handler)
	defer server.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+healthzPath, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求健康检查失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", resp.StatusCode)
	}
}

// TestGatewayAuthFailureIsJSON 覆盖鉴权失败一律回 JSON 401。
func TestGatewayAuthFailureIsJSON(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	validBody := `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`
	tests := []struct {
		name          string
		authorization string
	}{
		{name: "缺少 Authorization 头"},
		{name: "非法方案", authorization: "Basic abc"},
		{name: "未知密钥", authorization: authSchemePrefix + "wrong-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(), tt.authorization, validBody)
			assertJSONError(t, result, http.StatusUnauthorized)
		})
	}
}

// TestGatewayMethodNotAllowedIsJSON 覆盖已注册路径上的非 POST 方法回 JSON 405。
func TestGatewayMethodNotAllowedIsJSON(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+domain.ProtocolOpenAIChat.EndpointPath(), nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set(authorizationHeader, authSchemePrefix+testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("发送请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	assertJSONError(t, httpResult{
		status:      resp.StatusCode,
		contentType: resp.Header.Get("Content-Type"),
		body:        body,
	}, http.StatusMethodNotAllowed)
}

// TestGatewayNoRouteIsJSON 覆盖无候选渠道时回 JSON 404。
func TestGatewayNoRouteIsJSON(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	assertJSONError(t, result, http.StatusNotFound)
}

// TestGatewayUpstreamUnreachableIsJSON 覆盖上游不可达时回 JSON 502。
func TestGatewayUpstreamUnreachableIsJSON(t *testing.T) {
	// 先起一个上游再关掉：地址保持有效，连接被拒。
	deadUpstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadUpstream.Close()

	st := &fakeGatewayStore{
		auth: activeAuth(),
		routes: []store.RouteCandidate{{
			ChannelID: 10, BaseURL: deadUpstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
		}},
		credentials: []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
	}
	server := httptest.NewServer(newTestGateway(t, st).handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	assertJSONError(t, result, http.StatusBadGateway)
}

// TestGatewayUnregisteredPathIsJSON 覆盖未注册路径回 JSON 404 而不是标准库的纯文本。
func TestGatewayUnregisteredPathIsJSON(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	result := doPost(t, server.URL+"/v1/unknown", authSchemePrefix+testAPIKey, "{}")
	assertJSONError(t, result, http.StatusNotFound)
}

// TestRunServerGracefulShutdown 覆盖 ctx 取消后 runServer 优雅关闭并返回。
//
// 用临时端口而不是固定端口：并发跑测试或本机已有服务占用端口时不会互相干扰。
func TestRunServerGracefulShutdown(t *testing.T) {
	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听临时端口失败：%v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(healthz), ReadHeaderTimeout: readHeaderTimeout}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, server, ln) }()

	waitForOK(t, "http://"+ln.Addr().String()+healthzPath)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("优雅关闭返回错误：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServer 在取消后未退出")
	}
}

// waitForOK 轮询 url 直到返回 200 或超时。
func waitForOK(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("构造请求失败：%v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 %s 就绪超时", url)
}
