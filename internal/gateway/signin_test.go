package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖上游信标头（signin_header）的端到端行为：
// 渠道 config 声明头名与取值，判定优先于状态码启发式，动作落到凭据冷却与调度上。
// 与 serve_test.go 的既有用例同一装配路径（内存存储替身 + 进程内假上游）。

// signinChannelConfig 是渠道 config 里信标头的声明；头名与取值全部来自配置。
func signinChannelConfig() []byte {
	return []byte(`{"signin_header":{"name":"x-login-state","values":{"expired":"expired","kept":"kept","renewed":"renewed"}}}`)
}

// signinSuccessBody 是一个可被 OpenAI Chat 适配器解码的成功响应体。
const signinSuccessBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`

// signinChatBody 是客户端请求体。
const signinChatBody = `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`

// signinTwoKeyStore 构造一个两把凭据、单一渠道的存储替身。
func signinTwoKeyStore(baseURL string) *fakeGatewayStore {
	return &fakeGatewayStore{
		auth: activeAuth(),
		routes: []store.RouteCandidate{{
			ChannelID: 10, BaseURL: baseURL, CredGroup: "group-a", UpstreamModel: "up-model",
			Config: signinChannelConfig(),
		}},
		credentials: []store.Credential{
			{Name: "first", Secret: []byte(`{"api_key":"sk-first"}`)},
			{Name: "second", Secret: []byte(`{"api_key":"sk-second"}`)},
		},
		wantMerchantID: 1,
	}
}

// keyRecorder 记录上游按序收到的凭据明文。
type keyRecorder struct {
	mu   sync.Mutex
	keys []string
}

func (r *keyRecorder) record(r2 *http.Request) {
	key := strings.TrimPrefix(r2.Header.Get("Authorization"), "Bearer ")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, key)
}

func (r *keyRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.keys...)
}

// TestGatewaySigninExpiredCoolsCredential 覆盖「expired 信标 → 凭据冷却并切换下一把」。
//
// 同时用 403（启发式本就会判为凭据类）与 502（启发式不会判为凭据类）两种状态码：
// 后者能证明信标优先于状态码结论 —— 只看 502 时重试同一渠道不会换凭据。
func TestGatewaySigninExpiredCoolsCredential(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			recorder := &keyRecorder{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder.record(r)
				w.Header().Set("Content-Type", jsonContentType)
				if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != "sk-second" {
					w.Header().Set("x-login-state", "expired")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error"}}`)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, signinSuccessBody)
			}))
			defer upstream.Close()

			server := httptest.NewServer(newTestGateway(t, signinTwoKeyStore(upstream.URL)).handler)
			defer server.Close()

			result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(), authSchemePrefix+testAPIKey, signinChatBody)
			if result.status != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
			}
			if keys := recorder.snapshot(); len(keys) != 2 || keys[0] != "sk-first" || keys[1] != "sk-second" {
				t.Fatalf("上游收到的 key = %v，期望 [sk-first sk-second]", keys)
			}
		})
	}
}

// TestGatewaySigninKeptDoesNotCoolCredential 覆盖「kept 信标 → 即使 401 也不冷却」。
//
// 每次请求都应只调一次上游（不因 401 换凭据），且凭据按轮换顺序依次使用：
// 第三把又回到第一把，说明第一把从未进入冷却。
func TestGatewaySigninKeptDoesNotCoolCredential(t *testing.T) {
	recorder := &keyRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", jsonContentType)
		w.Header().Set("x-login-state", "kept")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"type":"authentication_error"}}`)
	}))
	defer upstream.Close()

	server := httptest.NewServer(newTestGateway(t, signinTwoKeyStore(upstream.URL)).handler)
	defer server.Close()

	endpoint := server.URL + domain.ProtocolOpenAIChat.EndpointPath()
	want := []string{"sk-first", "sk-second", "sk-first"}
	for i := range want {
		result := doPost(t, endpoint, authSchemePrefix+testAPIKey, signinChatBody)
		if result.status == http.StatusOK {
			t.Fatalf("第 %d 次请求：上游 401 时客户端不应拿到成功响应", i+1)
		}
		// 信标头承载上游内部语义，不得随响应外泄给客户端。
		if got := result.headers.Get("x-login-state"); got != "" {
			t.Errorf("第 %d 次请求：客户端不应看到信标头，实际 %q", i+1, got)
		}
		keys := recorder.snapshot()
		if len(keys) != i+1 {
			t.Fatalf("第 %d 次请求后上游调用数 = %d，期望 %d（kept 不得触发换凭据）", i+1, len(keys), i+1)
		}
		if keys[i] != want[i] {
			t.Fatalf("第 %d 次请求使用 %q，期望 %q", i+1, keys[i], want[i])
		}
	}
}

// TestGatewaySigninRenewedClearsCooling 覆盖「renewed → 冷却解除恢复调度」。
//
// 时序：
//  1. 两把凭据都返回 401（无信标）→ 两把都进入冷却；
//  2. 整组冷却时按 fail-open 取用，轮换游标使第二把先上，它返回 200 + renewed → 解除第二把的冷却；
//  3. 下一次请求第一把仍在冷却、第二把已恢复 → 仍用第二把。
//
// 若 renewed 未解除冷却，第 3 步整组仍在冷却会 fail-open，游标使第一把先上。
func TestGatewaySigninRenewedClearsCooling(t *testing.T) {
	recorder := &keyRecorder{}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", jsonContentType)
		switch calls.Add(1) {
		case 1, 2:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"authentication_error"}}`)
		case 3:
			w.Header().Set("x-login-state", "renewed")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, signinSuccessBody)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, signinSuccessBody)
		}
	}))
	defer upstream.Close()

	server := httptest.NewServer(newTestGateway(t, signinTwoKeyStore(upstream.URL)).handler)
	defer server.Close()

	endpoint := server.URL + domain.ProtocolOpenAIChat.EndpointPath()
	// 第 1 次：两把都被拒。
	if result := doPost(t, endpoint, authSchemePrefix+testAPIKey, signinChatBody); result.status == http.StatusOK {
		t.Fatal("两把凭据都被拒时客户端不应拿到成功响应")
	}
	// 第 2 次：fail-open 取用第二把，响应带 renewed。
	if result := doPost(t, endpoint, authSchemePrefix+testAPIKey, signinChatBody); result.status != http.StatusOK {
		t.Fatalf("fail-open 尝试应成功，实际状态码 %d，响应体 %s", result.status, result.body)
	}
	// 第 3 次：第二把的冷却已解除，调度恢复，仍用第二把。
	if result := doPost(t, endpoint, authSchemePrefix+testAPIKey, signinChatBody); result.status != http.StatusOK {
		t.Fatalf("续期后调度应恢复，实际状态码 %d，响应体 %s", result.status, result.body)
	}

	want := []string{"sk-first", "sk-second", "sk-second", "sk-second"}
	if keys := recorder.snapshot(); len(keys) != len(want) {
		t.Fatalf("上游收到的 key = %v，期望 %v", keys, want)
	} else {
		for i := range want {
			if keys[i] != want[i] {
				t.Fatalf("上游收到的 key = %v，期望 %v", keys, want)
			}
		}
	}
}
