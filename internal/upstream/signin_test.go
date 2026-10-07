package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
)

// signinTestHeader 是用例共用的信标头配置：头名与取值全部来自配置。
func signinTestHeader() domain.SigninHeader {
	return domain.SigninHeader{Name: "x-login-state", Expired: "expired", Kept: "kept", Renewed: "renewed"}
}

// TestDecideCredentialOutcomeMatrix 覆盖判定优先级矩阵：信标 × 状态码，含未声明配置的回退。
//
// 判据：命中信标头时以信标为准（expired 停用并换凭据、kept 摘掉整组凭据动作、
// renewed 请求解除冷却），未声明信标头或取值未命中时回退到报文与状态码分类。
//
// 信标头覆盖的是**动作**而非类别：它说的是「这个状态码不代表凭据有问题」，
// 不是「这次失败不算认证问题」，所以 kept 情形下类别仍保留（见用例断言）。
func TestDecideCredentialOutcomeMatrix(t *testing.T) {
	const authBody = `{"error":{"type":"authentication_error"}}`
	const requestBody = `{"error":{"type":"invalid_request_error"}}`

	const (
		// 认证类：停用本次凭据并在同渠道内换下一条。额度与余额还会多一个换渠道动作。
		authActions = failure.ActionSuspendAccount | failure.ActionRetryNextAccount
		// 上游自身故障：换渠道并计入熔断，不停用凭据。
		upstreamActions = failure.ActionRetryNextRoute | failure.ActionCountBreaker
		// 请求级：不重试。
		surface = failure.ActionSurface
		// kept 覆盖后：整组凭据动作被摘掉，剩下的就是空集。
		keptActions = failure.Action(0)
	)

	tests := []struct {
		name   string
		signin domain.SigninHeader
		value  string
		status int
		body   string
		want   credentialOutcome
	}{
		// 未声明配置：全部回退到报文与状态码分类。
		{name: "无配置 401", status: http.StatusUnauthorized, want: credentialOutcome{class: failure.ClassAuth, actions: authActions}},
		{name: "无配置 403", status: http.StatusForbidden, want: credentialOutcome{class: failure.ClassAuth, actions: authActions}},
		{name: "无配置 400 认证字面量", status: http.StatusBadRequest, body: authBody, want: credentialOutcome{class: failure.ClassAuth, actions: authActions}},
		{name: "无配置 400 参数错误", status: http.StatusBadRequest, body: requestBody, want: credentialOutcome{class: failure.ClassRequest, actions: surface}},
		{name: "无配置 429", status: http.StatusTooManyRequests, want: credentialOutcome{class: failure.ClassRateLimit, actions: failure.ActionRetryNextRoute}},
		// 500 与其它 5xx 不进体扫描：上游自身故障里碰到同名字面量不代表凭据有问题。
		{name: "无配置 500 认证字面量", status: http.StatusInternalServerError, body: authBody, want: credentialOutcome{class: failure.ClassUpstream, actions: upstreamActions}},
		// 已声明配置但取值未命中：同样回退。
		{name: "取值未命中 401 回退", signin: signinTestHeader(), value: "other", status: http.StatusUnauthorized, want: credentialOutcome{class: failure.ClassAuth, actions: authActions}},
		{name: "取值未命中 200 回退", signin: signinTestHeader(), value: "other", status: http.StatusOK, want: credentialOutcome{class: failure.ClassOther, actions: surface}},
		{name: "响应未带信标头 401 回退", signin: signinTestHeader(), status: http.StatusUnauthorized, want: credentialOutcome{class: failure.ClassAuth, actions: authActions}},
		// expired：厂商对凭据状态的直接断言，优先于由报文与状态码的间接推断，
		// 因此无论状态码是什么都归为认证类并按认证处置。
		{name: "expired 200 也停用", signin: signinTestHeader(), value: "expired", status: http.StatusOK, want: credentialOutcome{class: failure.ClassAuth, actions: authActions}},
		{name: "expired 401", signin: signinTestHeader(), value: "expired", status: http.StatusUnauthorized, want: credentialOutcome{class: failure.ClassAuth, actions: authActions}},
		{name: "expired 502 仍停用", signin: signinTestHeader(), value: "expired", status: http.StatusBadGateway, want: credentialOutcome{class: failure.ClassAuth, actions: authActions}},
		// 这一行是「覆盖动作、不覆盖类别」的代价所在：本可归为限流（可重试且不停用），
		// 但厂商声明了登录态失效，就以厂商为准。
		{name: "expired 429 仍停用", signin: signinTestHeader(), value: "expired", status: http.StatusTooManyRequests, want: credentialOutcome{class: failure.ClassAuth, actions: authActions}},
		// kept：状态码另有含义的厂商不被误冷却，但类别如实保留供排障。
		{name: "kept 401 不停用", signin: signinTestHeader(), value: "kept", status: http.StatusUnauthorized, want: credentialOutcome{class: failure.ClassAuth, actions: keptActions}},
		{name: "kept 403 不停用", signin: signinTestHeader(), value: "kept", status: http.StatusForbidden, want: credentialOutcome{class: failure.ClassAuth, actions: keptActions}},
		{name: "kept 400 认证字面量也不停用", signin: signinTestHeader(), value: "kept", status: http.StatusBadRequest, body: authBody, want: credentialOutcome{class: failure.ClassAuth, actions: keptActions}},
		// kept 只摘凭据动作，其余动作不受影响：请求级失败本来就无动作可摘。
		{name: "kept 400 参数错误", signin: signinTestHeader(), value: "kept", status: http.StatusBadRequest, body: requestBody, want: credentialOutcome{class: failure.ClassRequest, actions: surface}},
		{name: "kept 500 仍计入熔断", signin: signinTestHeader(), value: "kept", status: http.StatusInternalServerError, want: credentialOutcome{class: failure.ClassUpstream, actions: upstreamActions}},
		// renewed：与 kept 同样摘掉凭据动作（厂商在说凭据不是问题所在），另请求解除冷却。
		{name: "renewed 200", signin: signinTestHeader(), value: "renewed", status: http.StatusOK, want: credentialOutcome{class: failure.ClassOther, actions: surface, renewed: true}},
		{name: "renewed 401", signin: signinTestHeader(), value: "renewed", status: http.StatusUnauthorized, want: credentialOutcome{class: failure.ClassAuth, actions: keptActions, renewed: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			if tt.value != "" {
				header.Set("x-login-state", tt.value)
			}
			if got := decideCredentialOutcome(tt.signin, tt.status, header, []byte(tt.body)); got != tt.want {
				t.Fatalf("decideCredentialOutcome = %+v，期望 %+v", got, tt.want)
			}
		})
	}
}

// TestClassifyHTTPStatusMarksCredentialRenewal 断言 renewed 结论附加凭据续期能力，
// 且不附带凭据类失败能力（否则会触发换凭据）。
func TestClassifyHTTPStatusMarksCredentialRenewal(t *testing.T) {
	err := classifyHTTPStatus(credentialOutcome{renewed: true}, http.StatusUnauthorized, http.Header{}, nil)
	if !domain.CredentialRenewed(err) {
		t.Fatal("renewed 结论应附加凭据续期能力")
	}
	if failure.ActionsOf(err).Has(failure.ActionRetryNextAccount) {
		t.Error("renewed 结论不应附带换凭据动作")
	}
	if got := domain.AsError(err).Code; got != domain.CodeUpstreamRejected {
		t.Errorf("错误码 = %q，期望 %q", got, domain.CodeUpstreamRejected)
	}
}

// completeWithSignin 向一个按脚本设置响应头与状态码的假上游发一次非流式调用。
func completeWithSignin(t *testing.T, signin domain.SigninHeader, status int, responseHeader http.Header) (*domain.UpstreamResult, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, values := range responseHeader {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"type":"x"}}`))
	}))
	t.Cleanup(server.Close)

	client, err := New(Options{Headers: stubHeaders{}, Adapters: openAIChatAdapters})
	if err != nil {
		t.Fatalf("构造上游客户端失败: %v", err)
	}
	route := domain.Route{
		UpstreamID:   "u1",
		Protocol:     domain.ProtocolOpenAIChat,
		BaseURL:      server.URL,
		SigninHeader: signin,
	}
	return client.Complete(context.Background(), route, &domain.Request{Model: "m"}, []byte(`{"model":"m"}`))
}

// TestCompleteHonorsSigninHeader 断言转发路径按信标头判定凭据处置，信标优先于状态码。
func TestCompleteHonorsSigninHeader(t *testing.T) {
	tests := []struct {
		name         string
		value        string
		status       int
		wantRejected bool
		wantRenewed  bool
	}{
		{name: "expired 覆盖 429", value: "expired", status: http.StatusTooManyRequests, wantRejected: true},
		{name: "kept 覆盖 401", value: "kept", status: http.StatusUnauthorized, wantRejected: false},
		{name: "renewed 覆盖 401", value: "renewed", status: http.StatusUnauthorized, wantRenewed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			header.Set("x-login-state", tt.value)
			_, callErr := completeWithSignin(t, signinTestHeader(), tt.status, header)
			if callErr == nil {
				t.Fatal("非 2xx 上游响应应报错")
			}
			if got := failure.ActionsOf(callErr).Has(failure.ActionRetryNextAccount); got != tt.wantRejected {
				t.Errorf("换凭据动作 = %v，期望 %v", got, tt.wantRejected)
			}
			if got := domain.CredentialRenewed(callErr); got != tt.wantRenewed {
				t.Errorf("CredentialRenewed = %v，期望 %v", got, tt.wantRenewed)
			}
		})
	}
}

// TestCompleteSignalsRenewalOnSuccess 断言 2xx 响应里的 renewed 信标经结果回流。
func TestCompleteSignalsRenewalOnSuccess(t *testing.T) {
	const successBody = `{"id":"chatcmpl-1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-login-state", "renewed")
		_, _ = w.Write([]byte(successBody))
	}))
	t.Cleanup(server.Close)

	client, err := New(Options{Headers: stubHeaders{}, Adapters: openAIChatAdapters})
	if err != nil {
		t.Fatalf("构造上游客户端失败: %v", err)
	}
	route := domain.Route{
		UpstreamID:   "u1",
		Protocol:     domain.ProtocolOpenAIChat,
		BaseURL:      server.URL,
		SigninHeader: signinTestHeader(),
	}
	result, callErr := client.Complete(context.Background(), route, &domain.Request{Model: "m"}, []byte(`{"model":"m"}`))
	if callErr != nil {
		t.Fatalf("上游返回 2xx 时不应报错：%v", callErr)
	}
	if !result.CredentialRenewed {
		t.Error("2xx 响应带 renewed 信标时结果应标记凭据续期")
	}
}

// TestCompleteIgnoresSigninHeaderWhenUnconfigured 断言未声明信标头时行为与既有审定完全一致：
// 带未知信标头的 401 仍按凭据类失败处理。
func TestCompleteIgnoresSigninHeaderWhenUnconfigured(t *testing.T) {
	header := http.Header{}
	header.Set("x-login-state", "kept")
	_, callErr := completeWithSignin(t, domain.SigninHeader{}, http.StatusUnauthorized, header)
	if callErr == nil {
		t.Fatal("401 应报错")
	}
	if !failure.ActionsOf(callErr).Has(failure.ActionRetryNextAccount) {
		t.Error("未声明信标头时 401 仍应按凭据类失败处理")
	}
}
