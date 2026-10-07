package failure

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

// TestPolicyTableInvariants 守护策略表的两条结构不变量。
//
// 这两条是「动作分组」的全部意义所在，靠人手维护必然漂移：
//
//   - 「重试」与「不重试」互斥且必居其一。两者同时出现自相矛盾；两个都没有则等于
//     没安排接下来做什么，消费方只能再定一套默认——那正是本包要消掉的东西。
//     （换凭据与换渠道**可以**同时出现：它们是升级顺序，不是二选一。）
//   - 停用时长只能配在停用动作上。给了时长却不带动作是没人读的死配置，
//     而带了动作不给时长是合法的：表示用调用方配置的默认冷却。
func TestPolicyTableInvariants(t *testing.T) {
	for class, policy := range policyTable {
		retryable := policy.Actions.Retryable()
		if surface := policy.Actions.Has(ActionSurface); retryable == surface {
			t.Errorf("%s 的动作必须「或可重试、或不重试」二居其一：%s", policy.Name, policy.Actions)
		}
		if suspends := policy.Actions.Has(ActionSuspendAccount); !suspends && policy.SuspendFor != 0 {
			t.Errorf("%s 没声明停用凭据，却给了停用时长 %v", policy.Name, policy.SuspendFor)
		}
		if policy.Name == "" {
			t.Errorf("类别 %d 的策略缺少分类名", class)
		}
	}
}

// TestClassifyHTTPRealBodies 守护按报文与状态码分类。
//
// 用例取自实际联调时上游真实返回的报文（含各厂商的措辞差异），而不是构造的假串：
// 分类器的价值全在「认得出没见过的措辞」，用自造报文覆盖不到这一点。
func TestClassifyHTTPRealBodies(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   Class
	}{
		// 余额耗尽：commandcode 用 400 + insufficient credits，OpenAI 系用 429 + insufficient_quota。
		{
			name:   "commandcode 余额不足 400",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"You have insufficient credits to make this request. Please purchase more credits to continue using the service."}}`,
			want:   ClassCredit,
		},
		{
			name:   "余额不足中文",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"账户余额不足，请充值后再试"}}`,
			want:   ClassCredit,
		},
		{
			name:   "欠费停服",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"Your account is overdue and has been suspended."}}`,
			want:   ClassCredit,
		},
		{
			// OpenAI 的 insufficient_quota 报文里带 billing，按「厂商在让你去付钱」归到余额。
			// 两类都会停用凭据，差别只在 15m 与 30m，按余额停更贴近事实。
			name:   "openai insufficient_quota 带 billing",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota"}}`,
			want:   ClassCredit,
		},
		// 额度用尽：与余额分开，两者的停用时长不同。
		{
			name:   "套餐限额",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"You have hit your weekly limit. limit reached|1791291355"}}`,
			want:   ClassQuota,
		},
		{
			name:   "套餐额度用尽",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"当前套餐配额已用尽，请升级套餐"}}`,
			want:   ClassQuota,
		},
		// 限流：必须排在额度规则之前，「Rate limit reached」里的 limit reached 会命中额度规则。
		{
			name:   "标准限流",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"Rate limit reached for requests"}}`,
			want:   ClassRateLimit,
		},
		{
			name:   "过载",
			status: http.StatusServiceUnavailable,
			body:   `{"error":{"message":"The engine is currently overloaded, please try again later"}}`,
			want:   ClassRateLimit,
		},
		{
			name:   "限流中文",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"当前并发数过高，已触发限流"}}`,
			want:   ClassRateLimit,
		},
		// 上下文超限：报文里的 maximum / limit 会被额度规则抢走，靠优先级排前守住。
		{
			name:   "上下文超限",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"This model's maximum context length is 128000 tokens, however you requested 200000 tokens.","type":"context_length_exceeded"}}`,
			want:   ClassContextLength,
		},
		{
			name:   "上下文超限另一措辞",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"prompt is too long: 210000 tokens > 200000 maximum"}}`,
			want:   ClassContextLength,
		},
		{
			name:   "上下文超限中文",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"输入内容超过上下文长度上限"}}`,
			want:   ClassContextLength,
		},
		// 模型不可用：opencode-go 的真实报文。
		{
			name:   "模型协议不支持",
			status: http.StatusBadRequest,
			body:   `{"type":"error","error":{"type":"ModelProtocolUnsupported","message":"Model does not support this protocol"}}`,
			want:   ClassModelUnavailable,
		},
		{
			name:   "模型不可用",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"server_error","message":"Upstream request failed: Model is unavailable."}}`,
			want:   ClassModelUnavailable,
		},
		{
			name:   "模型不存在中文",
			status: http.StatusNotFound,
			body:   `{"error":{"message":"模型不存在或未开通"}}`,
			want:   ClassModelUnavailable,
		},
		// 认证。
		{
			name:   "401 无报文",
			status: http.StatusUnauthorized,
			want:   ClassAuth,
		},
		{
			name:   "403 无报文",
			status: http.StatusForbidden,
			want:   ClassAuth,
		},
		{
			name:   "400 认证字面量",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_api_key"}}`,
			want:   ClassAuth,
		},
		{
			name:   "gemini 密钥无效",
			status: http.StatusBadRequest,
			body:   `{"error":{"status":"INVALID_ARGUMENT","message":"API key not valid. Please pass a valid API key."}}`,
			want:   ClassAuth,
		},
		// 请求级：认不出具体原因，但可以断定换渠道重试不会成功。
		{
			name:   "参数错误",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_request_error","message":"messages: field required"}}`,
			want:   ClassRequest,
		},
		{
			name:   "opencode 缺会话头",
			status: http.StatusBadRequest,
			body:   `{"type":"error","error":{"type":"MissingSessionID","message":"Request is missing x-opencode-session and cannot be routed efficiently."}}`,
			want:   ClassRequest,
		},
		// 上游故障：与请求级同属「认不出具体原因」，但必须换渠道重试且计入渠道健康度。
		{
			name:   "上游 500",
			status: http.StatusInternalServerError,
			body:   `{"error":{"message":"internal server error"}}`,
			want:   ClassUpstream,
		},
		// 超时与 5xx 的处置相同，但单独成类：对外的 504 只能由类别推出。
		{
			name:   "上游超时",
			status: http.StatusRequestTimeout,
			want:   ClassTimeout,
		},
		{
			// 5xx 不做凭据字面量扫描：上游自身故障的报文里偶然出现同名字面量不代表凭据有问题，
			// 据此换 key 只会把一次上游抖动放大成对整组 key 的枚举。
			name:   "5xx 带认证字面量仍归上游故障",
			status: http.StatusInternalServerError,
			body:   `{"error":{"type":"authentication_error"}}`,
			want:   ClassUpstream,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyHTTP(tc.status, []byte(tc.body)); got != tc.want {
				t.Fatalf("ClassifyHTTP = %s，期望 %s", ClassName(got), ClassName(tc.want))
			}
		})
	}
}

// TestClassifyCodeAndEnvelope 守护结构化类型名/机器码的归类，以及两条路径的兜底差异。
//
// 兜底不同是刻意的：非 2xx 里认不出的错误不能断言该怪谁（ClassOther，保守）；
// 而成功响应里出现错误信封本身就矛盾，按上游故障处理（可换渠道）。
func TestClassifyCodeAndEnvelope(t *testing.T) {
	if got := ClassifyCode("INVALID_REQUEST_ERROR"); got != ClassRequest {
		t.Errorf("大小写不敏感应命中，实际 %s", ClassName(got))
	}
	if got := ClassifyCode("quota_exceeded"); got != ClassQuota {
		t.Errorf("quota_exceeded = %s，期望 quota", ClassName(got))
	}
	if got := ClassifyCode("deadline_exceeded"); got != ClassTimeout {
		t.Errorf("deadline_exceeded = %s，期望 timeout", ClassName(got))
	}
	if got := ClassifyCode("something_new"); got != ClassOther {
		t.Errorf("认不出的机器码应归 Other，实际 %s", ClassName(got))
	}
	if got := ClassifyErrorEnvelope("something_new"); got != ClassUpstream {
		t.Errorf("成功响应里认不出的错误信封应归 upstream，实际 %s", ClassName(got))
	}
	if got := ClassifyErrorEnvelope("insufficient_credits"); got != ClassCredit {
		t.Errorf("错误信封里的已知取值仍应精确归类，实际 %s", ClassName(got))
	}
}

// TestRateLimitStatusOutranksCredentialWording 固定住优先级：429 就是限流，不因报文里
// 出现认证字面量而改判。
//
// 这种矛盾报文是真存在的（渠道过载时在报文里提到 key）。定这一侧的理由：429 是上游对
// **本次请求**的流量判决，而报文里的认证字面量可能是通用模板；且改判为认证会去停用一把
// 没坏的 key，代价比“少停一次”大。报文不与状态码矛盾时仍按报文归。
//
// 正文刻意选一个不命中任何报文规则的串（否则规则分支先返回，测不到状态码分支）。
func TestRateLimitStatusOutranksCredentialWording(t *testing.T) {
	body := []byte(`{"error":{"type":"authentication_error"}}`)
	if got := ClassifyCode("authentication_error"); got != ClassAuth {
		t.Fatalf("该字面量本身应归认证，实际 %s", ClassName(got))
	}
	if got := ClassifyHTTP(http.StatusTooManyRequests, body); got != ClassRateLimit {
		t.Fatalf("429 配认证字面量应归限流，实际 %s", ClassName(got))
	}
	// 400 上没有这层优先级：状态码不表态，报文说了算。
	if got := ClassifyHTTP(http.StatusBadRequest, body); got != ClassAuth {
		t.Fatalf("400 配认证字面量应归认证，实际 %s", ClassName(got))
	}
}

// TestSuspendForIsGatedByAction 固定住「停用时长由动作把关」。
//
// 渠道信标头可以把停用动作摘掉而保留类别：此时即使类别本身带停用时长，也不该报出来，
// 否则调用方会按被抹掉的类别去冷却一把厂商声明过“凭据没问题”的 key。
func TestSuspendForIsGatedByAction(t *testing.T) {
	masked := NewErrorWithActions(domain.CodeUpstreamRejected, "额度用尽", "", ClassQuota, ActionRetryNextRoute)
	if got := SuspendFor(masked); got != 0 {
		t.Fatalf("停用动作被摘掉后仍报出停用时长 %v，期望 0", got)
	}
	plain := NewError(domain.CodeUpstreamRejected, "额度用尽", "", ClassQuota)
	if got := SuspendFor(plain); got != 15*time.Minute {
		t.Fatalf("未覆盖时停用时长 = %v，期望 15 分钟", got)
	}
}

// TestCodeForClassKeepsTimeoutDistinct 守护「超时单独成类」的理由：
// 类别要能把对外的 504 与 502 区分开，否则客户端会把超时看成普通上游故障。
func TestCodeForClassKeepsTimeoutDistinct(t *testing.T) {
	cases := []struct {
		class Class
		want  domain.Code
	}{
		{ClassTimeout, domain.CodeUpstreamTimeout},
		{ClassUpstream, domain.CodeUpstreamUnavailable},
		{ClassRateLimit, domain.CodeUpstreamRateLimited},
		// 其余上游侧失败统一归「上游拒绝请求」：是否重试由动作集合决定，不再由错误码决定。
		{ClassRequest, domain.CodeUpstreamRejected},
		{ClassAuth, domain.CodeUpstreamRejected},
		{ClassContextLength, domain.CodeUpstreamRejected},
	}
	for _, tc := range cases {
		if got := CodeForClass(tc.class); got != tc.want {
			t.Errorf("CodeForClass(%s) = %s，期望 %s", ClassName(tc.class), got, tc.want)
		}
	}
	timedOut := NewError(CodeForClass(ClassTimeout), "上游超时", "", ClassTimeout)
	if got := domain.HTTPStatus(timedOut); got != http.StatusGatewayTimeout {
		t.Errorf("超时类别的错误应对外回 504，实际回 %d", got)
	}
}

// TestActionsForClassifiesDisposition 守护类别到动作的映射，逐个盯住几个关键取舍。
func TestActionsForClassifiesDisposition(t *testing.T) {
	cases := []struct {
		class Class
		want  Action
	}{
		// 额度与余额：停用 + 换 key + 换渠道。同一把 key 撑不起，另一条渠道可能撑得起。
		{ClassQuota, ActionSuspendAccount | ActionRetryNextAccount | ActionRetryNextRoute},
		{ClassCredit, ActionSuspendAccount | ActionRetryNextAccount | ActionRetryNextRoute},
		// 限流只换渠道：渠道仍存活，只是暂时忙，停用 key 是误判。
		{ClassRateLimit, ActionRetryNextRoute},
		// 上下文超限与模型不可用：换 key 换渠道都没用。
		{ClassContextLength, ActionSurface},
		{ClassModelUnavailable, ActionSurface},
		// 上游故障：换渠道 + 计入熔断，不停用凭据。超时与之处置相同，但另有 504 的分类码。
		{ClassUpstream, ActionRetryNextRoute | ActionCountBreaker},
		{ClassTimeout, ActionRetryNextRoute | ActionCountBreaker},
		{ClassOther, ActionSurface},
	}
	for _, tc := range cases {
		if got := ActionsFor(tc.class); got != tc.want {
			t.Errorf("%s 的动作 = %s，期望 %s", ClassName(tc.class), got, tc.want)
		}
	}
}

// TestActionNextStepAndString 守护集合的读法：下一步三选一，可读形态稳定。
func TestActionNextStepAndString(t *testing.T) {
	if got := Action(0).NextStep(); got != ActionSurface {
		t.Errorf("空集合的下一步 = %s，期望 surface", actionName(got))
	}
	// 同渠道最便宜，优先于换渠道。
	both := ActionRetryNextAccount | ActionRetryNextRoute
	if got := both.NextStep(); got != ActionRetryNextAccount {
		t.Errorf("同时可换凭据与换渠道时应先换凭据，实际 %s", actionName(got))
	}
	if got := both.String(); got != "retry_next_account|retry_next_route" {
		t.Errorf("可读形态 = %q", got)
	}
	if got := Action(0).String(); got != "none" {
		t.Errorf("空集合的可读形态 = %q，期望 none", got)
	}
}

// TestActionsOfAndClassOf 守护消费方的读取入口：未带分类的错误返回零值/Other，不 panic。
func TestActionsOfAndClassOf(t *testing.T) {
	quota := NewError(domain.CodeUpstreamRejected, "额度用尽", "detail", ClassQuota)
	if got := ActionsOf(quota); !got.Has(ActionSuspendAccount) {
		t.Errorf("从错误里读出的动作 = %s，期望含 suspend_account", got)
	}
	if got := ClassOf(quota); got != ClassQuota {
		t.Errorf("从错误里读出的类别 = %s，期望 quota", ClassName(got))
	}
	if got := PolicyOf(ClassQuota).SuspendFor; got != 15*time.Minute {
		t.Errorf("额度停用时长 = %v，期望 15m", got)
	}

	// 包装链上任意一层声明即可读到：生产端还会再套 retry-after、凭据拒绝一类包装。
	wrapped := wrappedError{err: quota}
	if got := ActionsOf(wrapped); !got.Has(ActionRetryNextRoute) {
		t.Errorf("包装后读出的动作 = %s，期望含 retry_next_route", got)
	}

	if got := ActionsOf(errors.New("普通错误")); got != 0 {
		t.Errorf("未带分类时动作 = %s，期望零值", got)
	}
	if got := ClassOf(errors.New("普通错误")); got != ClassOther {
		t.Errorf("未带分类时类别 = %s，期望 other", ClassName(got))
	}
	if got := ActionsOf(nil); got != 0 {
		t.Errorf("nil 错误时动作 = %s，期望零值", got)
	}
}

// wrappedError 是一个不透明包装，用于验证能力沿包装链可达。
type wrappedError struct{ err error }

func (e wrappedError) Error() string { return "包装：" + e.err.Error() }

func (e wrappedError) Unwrap() error { return e.err }

// TestClassNameOfDistinguishesUnclassified 守护「未带分类」与「分类为 other」的区别。
//
// 前者是网关自身的错误（请求校验、平台故障），上游失败分类这根轴根本不适用；
// 后者是上游确实失败了但认不出成因。两者都记成 other 会让按类聚合的尝试日志多一个
// 含义不明的桶，而该字段是按「非空才写」处理的。
func TestClassNameOfDistinguishesUnclassified(t *testing.T) {
	if got := ClassNameOf(errors.New("网关自身的错误")); got != "" {
		t.Errorf("未带分类应返回空串，实际 %q", got)
	}
	if got := ClassNameOf(NewError(domain.CodeInternal, "平台故障", "", ClassOther)); got != "other" {
		t.Errorf("显式归为 other 应返回 other，实际 %q", got)
	}
	if _, ok := ClassOfKnown(errors.New("网关自身的错误")); ok {
		t.Error("未带分类时应报告 false")
	}
	if class, ok := ClassOfKnown(NewError(domain.CodeInternal, "平台故障", "", ClassOther)); !ok || class != ClassOther {
		t.Error("显式归为 other 时应报告 true 与 ClassOther")
	}
}

// TestFailureErrorKeepsDomainErrorReachable 守护内嵌统一错误不被 Unwrap 挡住。
//
// 没有 Unwrap 时 errors.As 按具体类型匹配会失败，AsError / HTTPStatus 一律拿不到东西 ——
// 那是面向客户端的错误体与状态码全线失灵。
func TestFailureErrorKeepsDomainErrorReachable(t *testing.T) {
	err := NewError(domain.CodeUpstreamUnavailable, "上游不可用", "", ClassUpstream)
	wrapped := wrappedError{err: err}
	if got := domain.AsError(wrapped); got == nil || got.Code != domain.CodeUpstreamUnavailable {
		t.Fatalf("统一错误应可穿透包装链，实际 %v", got)
	}
	if got := domain.HTTPStatus(wrapped); got == 0 {
		t.Fatal("HTTPStatus 应能从统一错误里取到状态码")
	}
}
