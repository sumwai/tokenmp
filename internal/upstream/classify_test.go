package upstream

import (
	"net/http"
	"testing"
)

// TestClassifyUpstreamFailure 守护失败分类表。
//
// 用例取自实际联调时上游真实返回的报文（含各厂商的措辞差异），而不是构造的假串：
// 分类器的价值全在「认得出没见过的措辞」，用自造报文覆盖不到这一点。
func TestClassifyUpstreamFailure(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   upstreamFailure
	}{
		// 余额耗尽：commandcode 用 400 + insufficient credits，OpenAI 系用 429 + insufficient_quota。
		{
			name:   "commandcode 余额不足 400",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"You have insufficient credits to make this request. Please purchase more credits to continue using the service."}}`,
			want:   failureCredit,
		},
		{
			name:   "余额不足中文",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"账户余额不足，请充值后再试"}}`,
			want:   failureCredit,
		},
		{
			name:   "欠费停服",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"Your account is overdue and has been suspended."}}`,
			want:   failureCredit,
		},
		// 额度用尽：与余额分开，两者的冷却时长不同。
		{
			// OpenAI 的 insufficient_quota 报文里带 billing，按「厂商在让你去付钱」归到余额。
			// 两类都会停用凭据，差别只在 15m 与 30m，按余额停更贴近事实。
			name:   "openai insufficient_quota 归余额",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota"}}`,
			want:   failureCredit,
		},
		{
			name:   "套餐限额",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"You have hit your weekly limit. limit reached|1791291355"}}`,
			want:   failureQuota,
		},
		{
			name:   "配额中文",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"当前套餐配额已用尽，请升级套餐"}}`,
			want:   failureQuota,
		},
		// 限流。
		{
			name:   "标准限流",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"Rate limit reached for requests"}}`,
			want:   failureRateLimit,
		},
		{
			name:   "过载",
			status: http.StatusServiceUnavailable,
			body:   `{"error":{"message":"The engine is currently overloaded, please try again later"}}`,
			want:   failureRateLimit,
		},
		{
			name:   "限流中文",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"当前并发数过高，已触发限流"}}`,
			want:   failureRateLimit,
		},
		// 上下文超限：报文里的 maximum / limit 会被额度规则抢走，靠优先级排前守住。
		{
			name:   "上下文超限",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"This model's maximum context length is 128000 tokens, however you requested 200000 tokens.","type":"context_length_exceeded"}}`,
			want:   failureContextLength,
		},
		{
			name:   "上下文超限另一措辞",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"prompt is too long: 210000 tokens > 200000 maximum"}}`,
			want:   failureContextLength,
		},
		{
			name:   "上下文超限中文",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"输入内容超过上下文长度上限"}}`,
			want:   failureContextLength,
		},
		// 模型不可用：opencode-go 的两种真实报文。
		{
			name:   "模型协议不支持",
			status: http.StatusBadRequest,
			body:   `{"type":"error","error":{"type":"ModelProtocolUnsupported","message":"Model does not support this protocol"}}`,
			want:   failureModelUnavailable,
		},
		{
			name:   "模型不可用",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"server_error","message":"Upstream request failed: Model is unavailable."}}`,
			want:   failureModelUnavailable,
		},
		{
			name:   "模型不存在中文",
			status: http.StatusNotFound,
			body:   `{"error":{"message":"模型不存在或未开通"}}`,
			want:   failureModelUnavailable,
		},
		// 认证。
		{
			name:   "401 无报文",
			status: http.StatusUnauthorized,
			want:   failureAuth,
		},
		{
			name:   "403 无报文",
			status: http.StatusForbidden,
			want:   failureAuth,
		},
		{
			name:   "400 认证字面量",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_api_key"}}`,
			want:   failureAuth,
		},
		{
			name:   "gemini 密钥无效",
			status: http.StatusBadRequest,
			body:   `{"error":{"status":"INVALID_ARGUMENT","message":"API key not valid. Please pass a valid API key."}}`,
			want:   failureAuth,
		},
		// 未归类：参数错误与上游自身故障都不能触发换凭据。
		{
			name:   "参数错误",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_request_error","message":"messages: field required"}}`,
			want:   failureOther,
		},
		{
			name:   "上游 500",
			status: http.StatusInternalServerError,
			body:   `{"error":{"message":"internal server error"}}`,
			want:   failureOther,
		},
		{
			name:   "opencode 缺会话头",
			status: http.StatusBadRequest,
			body:   `{"type":"error","error":{"type":"MissingSessionID","message":"Request is missing x-opencode-session and cannot be routed efficiently."}}`,
			want:   failureOther,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyUpstreamFailure(tc.status, []byte(tc.body)); got != tc.want {
				t.Fatalf("classifyUpstreamFailure(%d) = %s，期望 %s",
					tc.status, failurePolicyOf(got).name, failurePolicyOf(tc.want).name)
			}
		})
	}
}

// TestFailureRejectsCredential 守护「哪些失败该换凭据」。
//
// 换凭据的判据宁窄勿宽：把参数类错误也算作拒绝，会让一次客户端错误放大成对整组 key 的枚举。
func TestFailureRejectsCredential(t *testing.T) {
	rejecting := []upstreamFailure{failureAuth, failureQuota, failureCredit}
	for _, f := range rejecting {
		if !failureRejectsCredential(f) {
			t.Errorf("%s 应当换凭据", failurePolicyOf(f).name)
		}
	}
	keeping := []upstreamFailure{
		failureOther, failureRateLimit, failureContextLength, failureModelUnavailable,
	}
	for _, f := range keeping {
		if failureRejectsCredential(f) {
			t.Errorf("%s 不该换凭据", failurePolicyOf(f).name)
		}
	}
}

// TestFailurePolicies 守护冷却分级：额度与余额是账号级事实，停得比一次限流久。
func TestFailurePolicies(t *testing.T) {
	if got := failurePolicyOf(failureQuota).cooldown; got != quotaCooldown {
		t.Errorf("额度用尽的冷却 = %v，期望 %v", got, quotaCooldown)
	}
	if got := failurePolicyOf(failureCredit).cooldown; got != creditCooldown {
		t.Errorf("余额耗尽的冷却 = %v，期望 %v", got, creditCooldown)
	}
	// 余额比额度停得久：前者要等真金白银到账，后者等窗口滚动。
	if creditCooldown <= quotaCooldown {
		t.Errorf("余额冷却 %v 应长于额度冷却 %v", creditCooldown, quotaCooldown)
	}
	// 认证失败不另立数值：由调用方配置的默认冷却兜底。
	if got := failurePolicyOf(failureAuth).cooldown; got != 0 {
		t.Errorf("认证失败的冷却 = %v，期望 0（表示用调用方默认值）", got)
	}
	// 不换凭据的几类不该带冷却，带了会掩盖「这里本来就不停用」的取舍。
	for _, f := range []upstreamFailure{failureOther, failureRateLimit, failureContextLength, failureModelUnavailable} {
		if got := failurePolicyOf(f).cooldown; got != 0 {
			t.Errorf("%s 的冷却 = %v，期望 0", failurePolicyOf(f).name, got)
		}
	}
}

// TestClassifyIgnoresCredentialTokensOnServerError 守护 5xx 不做凭据字面量扫描。
//
// 上游自身故障的报文里偶然出现 authentication_error 不代表凭据有问题，
// 据此换 key 只会把一次上游抖动放大成对整组 key 的枚举。
func TestClassifyIgnoresCredentialTokensOnServerError(t *testing.T) {
	body := []byte(`{"error":{"type":"authentication_error"}}`)
	if got := classifyUpstreamFailure(http.StatusInternalServerError, body); got != failureOther {
		t.Fatalf("500 带认证字面量 = %s，期望 other", failurePolicyOf(got).name)
	}
	if got := classifyUpstreamFailure(http.StatusBadRequest, body); got != failureAuth {
		t.Fatalf("400 带认证字面量 = %s，期望 auth", failurePolicyOf(got).name)
	}
}
