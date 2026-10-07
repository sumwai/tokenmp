package upstream

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/anthropic"
	"github.com/sumwai/tokenmp/internal/adapters/gemini"
	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/adapters/openairesponses"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
)

// 本文件用**模拟上游**逐类复现上游失败，断言的是「分类 + 处置」这一对，
// 而不是只看错误码：重构要保证的是同一份报文在流式与非流式、帧式与非帧式下得到同一待遇。
//
// 每条用例都额外跑一遍内部一致性断言（见 checkConsistency）：它们把「类别 → 动作 → 错误码」
// 三者的关系写死，使「某条改了但另一条忘了改」这类漂移在任意一条用例上都会暴露。

// mockScript 是假上游对一次请求的应答脚本。
type mockScript struct {
	status  int
	body    string
	headers http.Header
	// sse 非空时按 Server-Sent Events 逐项写出；每项是 data 段原文。
	sse []string
	// sseEvent 非空时给每一帧加同样的 event 名。
	sseEvent string
	// delay 让本次应答先等待一段时间，用于触发上游超时。
	delay time.Duration
}

// mockCall 是一次被记录下来的上游请求。
type mockCall struct {
	path    string
	auth    string
	body    string
	headers http.Header
}

// mockUpstream 是按脚本应答的假上游。
//
// 脚本按调用次数依次取用，越界后重复最后一条：一次请求可能触发同渠道换凭据与跨渠道回退，
// 用一个脚本列表就能表达「第一次这样答、之后那样答」。
type mockUpstream struct {
	server *httptest.Server

	mu      sync.Mutex
	scripts []mockScript
	calls   []mockCall
}

// newMockUpstream 起一个按脚本应答的假上游。
func newMockUpstream(t *testing.T, scripts ...mockScript) *mockUpstream {
	t.Helper()
	upstream := &mockUpstream{scripts: scripts}
	upstream.server = httptest.NewServer(http.HandlerFunc(upstream.handle))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// handle 应答一次请求，并把请求记录成可断言的事实。
func (m *mockUpstream) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := readAllLimited(r)
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{
		path:    r.URL.Path,
		auth:    r.Header.Get("Authorization"),
		body:    body,
		headers: r.Header.Clone(),
	})
	index := len(m.calls) - 1
	if index >= len(m.scripts) {
		index = len(m.scripts) - 1
	}
	script := m.scripts[index]
	m.mu.Unlock()

	if script.delay > 0 {
		select {
		case <-time.After(script.delay):
		case <-r.Context().Done():
			return
		}
	}
	for name, values := range script.headers {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	if len(script.sse) > 0 {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(script.status)
		flusher, _ := w.(http.Flusher)
		for _, frame := range script.sse {
			if script.sseEvent != "" {
				_, _ = fmt.Fprintf(w, "event: %s\n", script.sseEvent)
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
			if flusher != nil {
				flusher.Flush()
			}
		}
		return
	}
	w.WriteHeader(script.status)
	_, _ = w.Write([]byte(script.body))
}

// readAllLimited 读出请求体；假上游只关心凭据与路径，只读前 1MB 足够。
func readAllLimited(r *http.Request) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// URL 返回假上游根地址。
func (m *mockUpstream) URL() string { return m.server.URL }

// callCount 返回收到的请求数。
func (m *mockUpstream) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// auths 已移：请求头原样列表无用武之地，断言凭据请用 keys。

// mockAdapters 按协议返回适配器，覆盖四方言。
func mockAdapters(protocol domain.Protocol) (domain.Adapter, error) {
	switch protocol {
	case domain.ProtocolOpenAIChat:
		return openaichat.New(), nil
	case domain.ProtocolOpenAIResponses:
		return openairesponses.New(), nil
	case domain.ProtocolAnthropicMessages:
		return anthropic.New(), nil
	case domain.ProtocolGeminiGenerate:
		return gemini.New(), nil
	default:
		return nil, fmt.Errorf("未知协议 %s", protocol)
	}
}

// newMockClient 构造指向假上游的客户端。
func newMockClient(t *testing.T, opts Options) *Client {
	t.Helper()
	opts.Headers = stubHeaders{}
	opts.Adapters = mockAdapters
	if opts.DefaultTimeout <= 0 {
		opts.DefaultTimeout = 5 * time.Second
	}
	client, err := New(opts)
	if err != nil {
		t.Fatalf("构造上游客户端失败：%v", err)
	}
	return client
}

// mockRoute 造一条指向假上游的路由。
func mockRoute(protocol domain.Protocol, baseURL string) domain.Route {
	return domain.Route{
		UpstreamID:    "mock",
		ChannelID:     1,
		Protocol:      protocol,
		UpstreamModel: "mock-model",
		BaseURL:       baseURL,
		Vendor:        "mock",
	}
}

// collectSink 收集流式分片，用于让 Stream 走完整路径。
type collectSink struct{ chunks []domain.Chunk }

func (s *collectSink) Send(_ context.Context, chunk domain.Chunk) error {
	s.chunks = append(s.chunks, chunk)
	return nil
}

// expectation 是一条用例对「分类 + 处置」的完整期望。
type expectation struct {
	class      failure.Class
	code       domain.Code
	httpStatus int
	actions    failure.Action
	suspendFor time.Duration
}

// checkConsistency 断言「类别 → 动作 → 错误码」三者的关系，逐条固定在测试里。
//
// 这些关系是消费方依赖的全部内容（谁能重试、谁停用凭据、谁计入熔断），
// 而它们分散在策略表、CodeForClass 与错误码表三处：任何一处单独改动都会在此暴露。
func checkConsistency(t *testing.T, class failure.Class, err error) {
	t.Helper()
	actions := failure.ActionsOf(err)
	if actions == 0 {
		t.Fatalf("上游失败必须带类别与动作，实际为零值：期望类别 %s，错误类型 %T，错误文本 %q",
			failure.ClassName(class), err, err.Error())
	}
	if actions.Retryable() == actions.Has(failure.ActionSurface) {
		t.Fatalf("%s 的动作集必须「或可重试、或明确不重试」二居其一：%s",
			failure.ClassName(class), actions)
	}
	if actions.Has(failure.ActionSuspendAccount) {
		if !actions.Has(failure.ActionRetryNextAccount) {
			t.Fatalf("%s 停用凭据却不换下一条：坏凭据会被反复选中", failure.ClassName(class))
		}
	}
	// 计入熔断的必须同时允许换渠道：计入熔断却不换渠道，等于把渠道判死却无处可退。
	if actions.Has(failure.ActionCountBreaker) && !actions.Has(failure.ActionRetryNextRoute) {
		t.Fatalf("%s 计入熔断却不换渠道：%s", failure.ClassName(class), actions)
	}
	if failure.ClassOf(err) != class {
		t.Fatalf("错误的类别 = %s，期望 %s", failure.ClassName(failure.ClassOf(err)), failure.ClassName(class))
	}
	if failure.SuspendFor(err) != policySuspendFor(t, class) {
		t.Fatalf("%s 的停用时长 = %v，期望 %v", failure.ClassName(class),
			failure.SuspendFor(err), policySuspendFor(t, class))
	}
}

// policySuspendFor 从策略表取该类别的停用时长，避免在用例里复述数字。
func policySuspendFor(_ *testing.T, class failure.Class) time.Duration {
	if !failure.ActionsFor(class).Has(failure.ActionSuspendAccount) {
		return 0
	}
	// 直接构造一个该类型的错误来取策略值：策略表本身不导出，SuspendFor 是它的读法。
	return failure.SuspendFor(failure.NewError(domain.CodeUpstreamRejected, "x", "", class))
}

// TestFailureClassMatrix 逐类复现上游失败，断言类别、动作、错误码与状态码。
//
// 覆盖面刻意超出「已知会出现的报文」：空体、非 JSON、二进制、超长体、边界状态码，
// 用来验证未列出的情况不会漏进无声的默认分支。
func TestFailureClassMatrix(t *testing.T) {
	cases := []struct {
		name   string
		script mockScript
		want   expectation
	}{
		// ---- 请求级：认得出或认不出，都不该重试 ----
		{
			name:   "400 参数错误",
			script: mockScript{status: 400, body: `{"error":{"type":"invalid_request_error","message":"messages: field required"}}`},
			want:   expectation{failure.ClassRequest, domain.CodeUpstreamRejected, 502, failure.ActionSurface, 0},
		},
		{
			name:   "400 认不出的报文",
			script: mockScript{status: 400, body: `{"detail":"something we never saw before"}`},
			want:   expectation{failure.ClassRequest, domain.CodeUpstreamRejected, 502, failure.ActionSurface, 0},
		},
		{
			name:   "404 资源不存在",
			script: mockScript{status: 404, body: `{"error":{"type":"not_found_error"}}`},
			want:   expectation{failure.ClassRequest, domain.CodeUpstreamRejected, 502, failure.ActionSurface, 0},
		},
		{
			name:   "400 空体",
			script: mockScript{status: 400},
			want:   expectation{failure.ClassRequest, domain.CodeUpstreamRejected, 502, failure.ActionSurface, 0},
		},
		{
			name:   "400 非 JSON 报文",
			script: mockScript{status: 400, body: `<html>Bad Request</html>`},
			want:   expectation{failure.ClassRequest, domain.CodeUpstreamRejected, 502, failure.ActionSurface, 0},
		},
		{
			name:   "400 二进制报文",
			script: mockScript{status: 400, body: "\x00\x01\x02\xff\xfe binary garbage"},
			want:   expectation{failure.ClassRequest, domain.CodeUpstreamRejected, 502, failure.ActionSurface, 0},
		},
		// ---- 上下文超限与模型不可用：换 key 与换渠道都没用 ----
		{
			name:   "400 上下文超限",
			script: mockScript{status: 400, body: `{"error":{"message":"This model's maximum context length is 128000 tokens","type":"context_length_exceeded"}}`},
			want:   expectation{failure.ClassContextLength, domain.CodeUpstreamRejected, 502, failure.ActionSurface, 0},
		},
		{
			name:   "400 模型不可用",
			script: mockScript{status: 400, body: `{"error":{"type":"server_error","message":"Upstream request failed: Model is unavailable."}}`},
			want:   expectation{failure.ClassModelUnavailable, domain.CodeUpstreamRejected, 502, failure.ActionSurface, 0},
		},
		// ---- 认证：停用凭据并换下一条，不停用时长由调用方默认冷却兜底 ----
		{
			name:   "401 空体",
			script: mockScript{status: 401},
			want:   expectation{failure.ClassAuth, domain.CodeUpstreamRejected, 502, failure.ActionSuspendAccount | failure.ActionRetryNextAccount, 0},
		},
		{
			name:   "403 带报文",
			script: mockScript{status: 403, body: `{"error":{"type":"permission_error"}}`},
			want:   expectation{failure.ClassAuth, domain.CodeUpstreamRejected, 502, failure.ActionSuspendAccount | failure.ActionRetryNextAccount, 0},
		},
		{
			name:   "400 密钥无效字面量",
			script: mockScript{status: 400, body: `{"error":{"message":"API key not valid. Please pass a valid API key."}}`},
			want:   expectation{failure.ClassAuth, domain.CodeUpstreamRejected, 502, failure.ActionSuspendAccount | failure.ActionRetryNextAccount, 0},
		},
		// ---- 额度与余额：停用 + 换凭据 + 换渠道，停用时长按类别给 ----
		{
			name:   "400 余额不足",
			script: mockScript{status: 400, body: `{"error":{"message":"You have insufficient credits to make this request."}}`},
			want:   expectation{failure.ClassCredit, domain.CodeUpstreamRejected, 502, failure.ActionSuspendAccount | failure.ActionRetryNextAccount | failure.ActionRetryNextRoute, 30 * time.Minute},
		},
		{
			// 类别按报文归为余额，错误码却按状态码 429 给：非 2xx 的错误码取自状态码，
			// 不看类别。这处不对称由 TestUpstreamCodeFollowsStatusNotClass 单独固定。
			name:   "429 insufficient_quota 带 billing 归余额",
			script: mockScript{status: 429, body: `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota"}}`},
			want:   expectation{failure.ClassCredit, domain.CodeUpstreamRateLimited, 503, failure.ActionSuspendAccount | failure.ActionRetryNextAccount | failure.ActionRetryNextRoute, 30 * time.Minute},
		},
		{
			name:   "400 套餐额度用尽",
			script: mockScript{status: 400, body: `{"error":{"message":"当前套餐配额已用尽，请升级套餐"}}`},
			want:   expectation{failure.ClassQuota, domain.CodeUpstreamRejected, 502, failure.ActionSuspendAccount | failure.ActionRetryNextAccount | failure.ActionRetryNextRoute, 15 * time.Minute},
		},
		// ---- 限流：只换渠道，不停用凭据，也不计入熔断 ----
		{
			name:   "429 限流",
			script: mockScript{status: 429, body: `{"error":{"message":"Rate limit reached for requests"}}`},
			want:   expectation{failure.ClassRateLimit, domain.CodeUpstreamRateLimited, 503, failure.ActionRetryNextRoute, 0},
		},
		{
			// 状态码与报文矛盾时以状态码为准，且不得因此停用一把没坏的 key。
			name:   "429 带认证字面量仍归限流",
			script: mockScript{status: 429, body: `{"error":{"type":"authentication_error"}}`},
			want:   expectation{failure.ClassRateLimit, domain.CodeUpstreamRateLimited, 503, failure.ActionRetryNextRoute, 0},
		},
		{
			// 过载归限流，但状态码是 5xx，对外错误码因此是 upstream_unavailable 而不是
			// upstream_rate_limited：同一处不对称，见 TestUpstreamCodeFollowsStatusNotClass。
			name:   "503 过载归限流",
			script: mockScript{status: 503, body: `{"error":{"message":"The engine is currently overloaded"}}`},
			want:   expectation{failure.ClassRateLimit, domain.CodeUpstreamUnavailable, 502, failure.ActionRetryNextRoute, 0},
		},
		// ---- 上游故障与超时：换渠道 + 计入熔断 ----
		{
			name:   "500 空体",
			script: mockScript{status: 500},
			want:   expectation{failure.ClassUpstream, domain.CodeUpstreamUnavailable, 502, failure.ActionRetryNextRoute | failure.ActionCountBreaker, 0},
		},
		{
			name:   "502 上游故障",
			script: mockScript{status: 502, body: `{"error":{"message":"bad gateway"}}`},
			want:   expectation{failure.ClassUpstream, domain.CodeUpstreamUnavailable, 502, failure.ActionRetryNextRoute | failure.ActionCountBreaker, 0},
		},
		{
			name:   "599 边界上游故障",
			script: mockScript{status: 599, body: `{"error":{"message":"weird"}}`},
			want:   expectation{failure.ClassUpstream, domain.CodeUpstreamUnavailable, 502, failure.ActionRetryNextRoute | failure.ActionCountBreaker, 0},
		},
		{
			name:   "500 带认证字面量仍归上游故障",
			script: mockScript{status: 500, body: `{"error":{"type":"authentication_error"}}`},
			want:   expectation{failure.ClassUpstream, domain.CodeUpstreamUnavailable, 502, failure.ActionRetryNextRoute | failure.ActionCountBreaker, 0},
		},
		{
			name:   "408 上游超时",
			script: mockScript{status: 408, body: `{"error":{"message":"request timeout"}}`},
			want:   expectation{failure.ClassTimeout, domain.CodeUpstreamTimeout, 504, failure.ActionRetryNextRoute | failure.ActionCountBreaker, 0},
		},
		// ---- 2xx：报文不可用时按上游故障处理（可换渠道），不按请求级终止 ----
		{
			name:   "200 非 JSON 报文",
			script: mockScript{status: 200, body: `<html>not a completion</html>`},
			want:   expectation{failure.ClassUpstream, domain.CodeUpstreamUnavailable, 502, failure.ActionRetryNextRoute | failure.ActionCountBreaker, 0},
		},
		{
			name:   "200 空体",
			script: mockScript{status: 200},
			want:   expectation{failure.ClassUpstream, domain.CodeUpstreamUnavailable, 502, failure.ActionRetryNextRoute | failure.ActionCountBreaker, 0},
		},
		{
			name:   "200 错误信封",
			script: mockScript{status: 200, body: `{"error":{"type":"server_error","message":"internal trouble"}}`},
			want:   expectation{failure.ClassUpstream, domain.CodeUpstreamUnavailable, 502, failure.ActionRetryNextRoute | failure.ActionCountBreaker, 0},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newMockUpstream(t, tc.script)
			client := newMockClient(t, Options{})
			route := mockRoute(domain.ProtocolOpenAIChat, upstream.URL())

			_, err := client.Complete(context.Background(), route, &domain.Request{Model: "mock-model"}, []byte(`{"model":"mock-model"}`))
			if err == nil {
				t.Fatal("期望失败，实际成功")
			}
			checkConsistency(t, tc.want.class, err)
			if got := failure.ActionsOf(err); got != tc.want.actions {
				t.Fatalf("动作集 = %s，期望 %s", got, tc.want.actions)
			}
			got := domain.AsError(err)
			if got == nil || got.Code != tc.want.code {
				t.Fatalf("错误码 = %v，期望 %s", got, tc.want.code)
			}
			if got := upstream.callCount(); got != 1 {
				t.Fatalf("上游调用次数 = %d，期望 1（客户端自身不重试）", got)
			}
			if status := domain.HTTPStatus(err); status != tc.want.httpStatus {
				t.Fatalf("对外状态码 = %d，期望 %d", status, tc.want.httpStatus)
			}
			if got := failure.SuspendFor(err); got != tc.want.suspendFor {
				t.Fatalf("凭据停用时长 = %v，期望 %v", got, tc.want.suspendFor)
			}
			// 非 2xx 的类别必须与分类器对同一份报文的结论一致：转发层若自行另判，在此暴露。
			// 这里没有配信标头，而信标头是唯一会覆盖分类的输入，所以该等式必须无条件成立。
			//
			// 2xx 不适用：那一路是「本该成功的响应给不出可用报文」，故意不按分类器
			// （它对 200 只能答 other），而按上游故障处理。
			if tc.script.status >= 400 {
				if want := failure.ClassifyHTTP(tc.script.status, []byte(tc.script.body)); want != failure.ClassOf(err) {
					t.Fatalf("错误的类别 %s 与分类器结论 %s 不一致",
						failure.ClassName(failure.ClassOf(err)), failure.ClassName(want))
				}
			}
		})
	}
}

// TestStreamErrorFrameMatrix 用模拟上游的 SSE 帧验证流式错误帧的分类。
//
// 与 TestFailureClassMatrix 的同一类报文必须得到同一类别：这是「同一份上游报文在流式与
// 非流式下待遇一致」这条承诺的落点。
func TestStreamErrorFrameMatrix(t *testing.T) {
	cases := []struct {
		name     string
		protocol domain.Protocol
		script   mockScript
		want     failure.Class
	}{
		{
			name:     "openai_chat 错误帧 参数错误",
			protocol: domain.ProtocolOpenAIChat,
			script:   mockScript{status: 200, sse: []string{`{"error":{"type":"invalid_request_error","message":"bad"}}`}},
			want:     failure.ClassRequest,
		},
		{
			name:     "openai_chat 错误帧 额度耗尽",
			protocol: domain.ProtocolOpenAIChat,
			script:   mockScript{status: 200, sse: []string{`{"error":{"type":"insufficient_quota","message":"quota"}}`}},
			want:     failure.ClassQuota,
		},
		{
			name:     "openai_chat 错误帧 余额不足",
			protocol: domain.ProtocolOpenAIChat,
			script:   mockScript{status: 200, sse: []string{`{"error":{"code":"insufficient_credits","message":"no credits"}}`}},
			want:     failure.ClassCredit,
		},
		{
			name:     "openai_chat 错误帧 认证失败",
			protocol: domain.ProtocolOpenAIChat,
			script:   mockScript{status: 200, sse: []string{`{"error":{"type":"authentication_error","message":"bad key"}}`}},
			want:     failure.ClassAuth,
		},
		{
			// 认不出的取值：错误信封出现在「本该成功的响应」里，按上游故障处理（可换渠道）。
			name:     "openai_chat 错误帧 未知类型归上游故障",
			protocol: domain.ProtocolOpenAIChat,
			script:   mockScript{status: 200, sse: []string{`{"error":{"type":"brand_new_error_we_never_saw"}}`}},
			want:     failure.ClassUpstream,
		},
		{
			name:     "anthropic 错误事件 认证失败",
			protocol: domain.ProtocolAnthropicMessages,
			script:   mockScript{status: 200, sseEvent: "error", sse: []string{`{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`}},
			want:     failure.ClassAuth,
		},
		{
			name:     "anthropic 错误事件 未知类型归上游故障",
			protocol: domain.ProtocolAnthropicMessages,
			script:   mockScript{status: 200, sseEvent: "error", sse: []string{`{"type":"error","error":{"type":"something_new"}}`}},
			want:     failure.ClassUpstream,
		},
		{
			name:     "openai_responses 失败帧 参数错误",
			protocol: domain.ProtocolOpenAIResponses,
			script:   mockScript{status: 200, sse: []string{`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`}},
			want:     failure.ClassRequest,
		},
		{
			name:     "openai_responses 失败帧 额度耗尽",
			protocol: domain.ProtocolOpenAIResponses,
			script:   mockScript{status: 200, sse: []string{`{"type":"error","code":"insufficient_quota"}`}},
			want:     failure.ClassQuota,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newMockUpstream(t, tc.script)
			client := newMockClient(t, Options{})
			route := mockRoute(tc.protocol, upstream.URL())
			sink := &collectSink{}

			err := client.Stream(context.Background(), route, &domain.Request{Model: "mock-model"},
				[]byte(`{"model":"mock-model","stream":true}`), sink)
			if err == nil {
				t.Fatal("期望流式错误，实际成功")
			}
			checkConsistency(t, tc.want, err)
			if got := failure.ClassOf(err); got != tc.want {
				t.Fatalf("类别 = %s，期望 %s", failure.ClassName(got), failure.ClassName(tc.want))
			}
		})
	}
}

// TestTransportFailureMatrix 用不可达地址与超时验证传输层失败的分类。
//
// 这三条分支不经过状态码分级，全靠构造时显式给出类别 —— 漏标的后果是熔断计数静默失效，
// 所以每条都要单独钉住。
func TestTransportFailureMatrix(t *testing.T) {
	t.Run("连接被拒归上游故障并计入熔断", func(t *testing.T) {
		// 取一个刚被关闭的端口：监听后立即关闭，端口在测试期间不会被复用。
		var listenConfig net.ListenConfig
		listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("取空闲端口失败：%v", err)
		}
		address := listener.Addr().String()
		if err = listener.Close(); err != nil {
			t.Fatalf("关闭监听失败：%v", err)
		}

		client := newMockClient(t, Options{})
		route := mockRoute(domain.ProtocolOpenAIChat, "http://"+address)
		_, callErr := client.Complete(context.Background(), route, &domain.Request{Model: "mock-model"}, []byte(`{}`))
		if callErr == nil {
			t.Fatal("连接被拒时应报错")
		}
		checkConsistency(t, failure.ClassUpstream, callErr)
		if !failure.ActionsOf(callErr).Has(failure.ActionCountBreaker) {
			t.Fatal("连接失败应计入渠道熔断")
		}
	})

	t.Run("客户单超时归超时并计入熔断", func(t *testing.T) {
		upstream := newMockUpstream(t, mockScript{status: 200, body: `{}`, delay: 2 * time.Second})
		client := newMockClient(t, Options{})
		route := mockRoute(domain.ProtocolOpenAIChat, upstream.URL())
		route.Timeout = 150 * time.Millisecond

		_, callErr := client.Complete(context.Background(), route, &domain.Request{Model: "mock-model"}, []byte(`{}`))
		if callErr == nil {
			t.Fatal("上游超时应报错")
		}
		checkConsistency(t, failure.ClassTimeout, callErr)
		if code := domain.AsError(callErr).Code; code != domain.CodeUpstreamTimeout {
			t.Fatalf("错误码 = %s，期望 %s", code, domain.CodeUpstreamTimeout)
		}
		if status := domain.HTTPStatus(callErr); status != 504 {
			t.Fatalf("对外状态码 = %d，期望 504", status)
		}
	})

	t.Run("调用方取消不带类别", func(t *testing.T) {
		upstream := newMockUpstream(t, mockScript{status: 200, body: `{}`, delay: 2 * time.Second})
		client := newMockClient(t, Options{})
		route := mockRoute(domain.ProtocolOpenAIChat, upstream.URL())

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		_, callErr := client.Complete(ctx, route, &domain.Request{Model: "mock-model"}, []byte(`{}`))
		cancel()
		if callErr == nil {
			t.Fatal("上下文取消应报错")
		}
		if actions := failure.ActionsOf(callErr); actions != 0 {
			t.Fatalf("调用方取消不该带动作集，实际 %s", actions)
		}
		if failure.ActionsOf(callErr).Has(failure.ActionCountBreaker) {
			t.Fatal("调用方取消不该计入渠道熔断")
		}
	})
}

// TestUpstreamCodeFollowsStatusNotClass 固定住一处**有意的不对称**。
//
// 非 2xx 的对外错误码取自 HTTP 状态码（只看状态码，不看类别），而流式错误帧与 2xx 错误信封
// 的错误码取自类别。于是同一个类别在两条路径上给出不同错误码：
//
//	余额耗尽 + 429  →  upstream_rate_limited / 503（状态码路径）
//	余额耗尽 + 帧   →  upstream_rejected    / 502（类别路径）
//
// 后果是客户端看到的提示可能与诊断不符：账号欠费时回 503 会鼓励客户端稍后重试，而重试不会
// 自行好转。网关自身的处置不受影响——重试、停用凭据与熔断都由动作集决定，与错误码无关。
//
// 本用例不是认可这处不对称，而是把它写成可执行的记录：若要改成「一律按类别推」，
// 改 CodeForClass 的调用点与 docs/compatibility.md 的错误码表，并让本用例失败。
func TestUpstreamCodeFollowsStatusNotClass(t *testing.T) {
	// 报文里同时出现 credits 与 billing，分类规则稳定地归为余额耗尽（信用类）。
	//nolint:gosec // G101：这是上游错误报文里的字面量，不是凭据。
	const creditBody = `{"error":{"message":"You have insufficient credits; check billing details.","type":"insufficient_credits"}}`

	t.Run("状态码路径按状态码给码", func(t *testing.T) {
		upstream := newMockUpstream(t, mockScript{status: 429, body: creditBody})
		client := newMockClient(t, Options{})
		_, err := client.Complete(context.Background(), mockRoute(domain.ProtocolOpenAIChat, upstream.URL()),
			&domain.Request{Model: "mock-model"}, []byte(`{}`))
		if err == nil {
			t.Fatal("期望失败")
		}
		if got := failure.ClassOf(err); got != failure.ClassCredit {
			t.Fatalf("类别 = %s，期望 credit", failure.ClassName(got))
		}
		if got := domain.AsError(err).Code; got != domain.CodeUpstreamRateLimited {
			t.Fatalf("错误码 = %s，期望随状态码 429 给 %s", got, domain.CodeUpstreamRateLimited)
		}
	})

	t.Run("帧路径按类别给码", func(t *testing.T) {
		upstream := newMockUpstream(t, mockScript{status: 200, sse: []string{creditBody}})
		client := newMockClient(t, Options{})
		err := client.Stream(context.Background(), mockRoute(domain.ProtocolOpenAIChat, upstream.URL()),
			&domain.Request{Model: "mock-model"}, []byte(`{}`), &collectSink{})
		if err == nil {
			t.Fatal("期望失败")
		}
		if got := failure.ClassOf(err); got != failure.ClassCredit {
			t.Fatalf("类别 = %s，期望 credit", failure.ClassName(got))
		}
		if got := domain.AsError(err).Code; got != domain.CodeUpstreamRejected {
			t.Fatalf("错误码 = %s，期望按类别给 %s", got, domain.CodeUpstreamRejected)
		}
	})
}

// TestStreamErrorFrameKeepsClassAndActions 守护流式错误帧的类别与动作不被剥掉。
//
// 适配器给出的错误是 failure.Error，它包着一层 *domain.Error。转发层若用 domain.AsError
// 把它取出来再上抛，errors.As 会穿透 Unwrap 丢掉外层，类别与动作集随之消失：流式错误帧
// 于是既不换渠道重试，也不停用出问题的凭据。这条路径唯一的可观察后果是「什么都不做」，
// 因此必须有断言直接盯住动作集。
func TestStreamErrorFrameKeepsClassAndActions(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		class failure.Class
	}{
		{
			name:  "认证失败应能停用凭据并换下一条",
			frame: `{"error":{"type":"authentication_error","message":"bad key"}}`,
			class: failure.ClassAuth,
		},
		{
			name:  "余额耗尽应能停用凭据并换渠道",
			frame: `{"error":{"type":"insufficient_credits","message":"no credits"}}`,
			class: failure.ClassCredit,
		},
		{
			name:  "上游故障应能换渠道并计入熔断",
			frame: `{"error":{"type":"brand_new_error_we_never_saw"}}`,
			class: failure.ClassUpstream,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newMockUpstream(t, mockScript{status: 200, sse: []string{tc.frame}})
			client := newMockClient(t, Options{})
			err := client.Stream(context.Background(), mockRoute(domain.ProtocolOpenAIChat, upstream.URL()),
				&domain.Request{Model: "mock-model"}, []byte(`{}`), &collectSink{})
			if err == nil {
				t.Fatal("期望失败")
			}
			actions := failure.ActionsOf(err)
			if actions == 0 {
				t.Fatal("流式错误帧的动作集为零值：类别被剥掉了")
			}
			if got := failure.ClassOf(err); got != tc.class {
				t.Fatalf("类别 = %s，期望 %s", failure.ClassName(got), failure.ClassName(tc.class))
			}
			if !actions.Retryable() {
				t.Fatalf("动作集 %s 应允许换渠道重试", actions)
			}
		})
	}
}

// TestPatternScansFullBodyRegardlessOfTruncation 固定住「分类扫全量、插件只看截断片段」这条不对称。
//
// 分类正则跑在完整报文上，而对插件暴露的正文是截断的（另有上限）。关键词落在截断点之后时，
// 插件在 facts.body 里看不到任何线索，却可能去「纠正」一个基于全量报文得出的正确类别。
func TestPatternScansFullBodyRegardlessOfTruncation(t *testing.T) {
	padding := strings.Repeat("x", 64*1024)
	script := mockScript{status: 400, body: `{"error":{"message":"` + padding + ` insufficient credits"}}`}
	upstream := newMockUpstream(t, script)
	client := newMockClient(t, Options{})
	route := mockRoute(domain.ProtocolOpenAIChat, upstream.URL())

	_, err := client.Complete(context.Background(), route, &domain.Request{Model: "mock-model"}, []byte(`{}`))
	if err == nil {
		t.Fatal("期望失败")
	}
	if got := failure.ClassOf(err); got != failure.ClassCredit {
		t.Fatalf("关键词在 64KB 之后仍应被扫到，实际类别 %s", failure.ClassName(got))
	}
}
