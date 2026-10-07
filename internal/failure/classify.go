package failure

import (
	"net/http"
	"regexp"
	"strings"
)

// 本文件是分类判据的唯一出处：状态码 + 报文正则一起看，而不是只看状态码。
//
// 只看状态码不够，因为同一个码在不同上游下含义不同：余额耗尽可能报成 429 配
// insufficient_quota，只看 429 会把它当成限流退避重试 —— 而那条路径无论重试多少次
// 都不会成功，真正该做的是换一条凭据。反过来，403 既可能是 key 无效，也可能是账号被
// 停用：两者都要换 key，但被停用的那把要停得更久。

// credentialTokens 是上游报文里明确表示「凭据或权限不被接受」的标记。
//
// 只登记认证与权限两类字面量：请求参数类错误（invalid_request_error、not_found_error）
// 刻意不在列，否则「拿不准的 4xx」会触发换凭据，把一次参数错误放大成对整组 key 的枚举。
var credentialTokens = []string{
	"authentication_error",
	"invalid_api_key",
	"invalid_authentication",
	"permission_error",
	"insufficient_permissions",
	"invalid api key",
	"incorrect api key",
	// Gemini 的错误体用 gRPC 状态与文案表达凭据失败：状态 UNAUTHENTICATED / PERMISSION_DENIED
	// 与下面的 API_KEY_INVALID、"API key not valid" 是它常见的两种取值。
	"unauthenticated",
	"permission_denied",
	"api_key_invalid",
	"api key not valid",
}

// classRule 是按报文归类的规则。
type classRule struct {
	class   Class
	pattern *regexp.Regexp
}

// classRules 按优先级排列：越具体的规则越靠前。
//
// 这里的顺序都有真实报文依据，换一下就会误分类：
//
//   - 上下文超限排在额度与限流之前：「This model's maximum context length is …」里的
//     maximum / limit / exceeded 会被后面的宽规则抢走，把参数问题认成账号问题，误停整组 key。
//   - 限流排在额度之前：「Rate limit reached」里的 limit reached 命中额度规则，
//     而它显然不是额度用尽。
//   - 余额排在额度之前：「余额不足」与「额度用尽」的停用时长不同，而前者报文里常同时
//     出现 quota / billing 一类词。厂商在让你去付钱就按余额停，不按窗口滚动停。
var classRules = []classRule{
	{ClassContextLength, regexp.MustCompile(
		`(?i)context_length_exceeded|prompt is too long|input is too long|` +
			`(exceeds?|exceeded|over|beyond)( the)?( model'?s?)?( maximum)? context|` +
			`context (length|limit|window) (exceeded|is exceeded)|maximum context|` +
			`上下文长度|超出.{0,8}上下文|超过.{0,8}上下文`)},
	{ClassCredit, regexp.MustCompile(
		`(?i)insufficient.?(balance|credit|fund)|balance|credit|billing|payment|arrear|overdue|suspended|` +
			`余额不足|余额耗尽|账户余额|欠费|请充值|账户.{0,4}(停用|冻结)`)},
	{ClassRateLimit, regexp.MustCompile(
		`(?i)rate.?limit|too many requests|overloaded|` +
			`限流|请求过于频繁|请求频率.{0,6}超|并发.{0,6}超`)},
	{ClassQuota, regexp.MustCompile(
		`(?i)quota|usage.?limit|limit.?reached|hit your .*limit|limit.{0,24}resets|exceeded.*(plan|limit)|` +
			`配额|额度.{0,6}(用尽|不足|已用完)|用量上限|已达.{0,6}(上限|限额)`)},
	{ClassModelUnavailable, regexp.MustCompile(
		`(?i)model.{0,80}(not (supported|accessible|available|found|enabled|allowed)|unsupported|unavailable|does ?n[o']t exist|unknown|invalid)|` +
			`(no such|unknown|invalid|unsupported) model|model_not_found|` +
			`模型.{0,10}(不存在|不可用|不支持|未开通)`)},
}

// codeRules 按上游给出的类型名或机器码归类。
//
// 适配器在流式错误帧上能读到结构化的 type / code，比扫报文更准，优先用它。
// 认不出的一律交给调用方决定的兜底（见 ClassifyCode 与 ClassifyErrorEnvelope）。
var codeRules = map[string]Class{
	// 请求级：换渠道重试无用，也不该停用凭据。
	"invalid_request_error": ClassRequest,
	"not_found_error":       ClassRequest,
	"invalid_parameter":     ClassRequest,
	// 凭据类。
	"authentication_error": ClassAuth,
	"permission_error":     ClassAuth,
	"invalid_api_key":      ClassAuth,
	"unauthenticated":      ClassAuth,
	// 额度与余额：账号级事实，换 key 是唯一出路。
	"insufficient_quota":   ClassQuota,
	"quota_exceeded":       ClassQuota,
	"insufficient_credits": ClassCredit,
	"insufficient_funds":   ClassCredit,
	// 瞬时故障：换渠道可能成功，但不停用凭据。
	"rate_limit_exceeded": ClassRateLimit,
	"overloaded":          ClassRateLimit,
	// 请求本身与上游能力不匹配。
	"context_length_exceeded": ClassContextLength,
	"model_not_found":         ClassModelUnavailable,
}

// ClassifyHTTP 归类一次非 2xx 上游响应。
//
// 报文优先、状态码兜底：报文能区分同一状态码下的不同含义（429 既可能是限流，也可能是
// 余额耗尽），状态码只在报文没给出线索时用来认认证类与上游故障。
func ClassifyHTTP(status int, body []byte) Class {
	for _, rule := range classRules {
		if rule.pattern.Match(body) {
			return rule.class
		}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ClassAuth
	}
	if status == http.StatusTooManyRequests {
		return ClassRateLimit
	}
	if hasCredentialToken(status, body) {
		return ClassAuth
	}
	switch {
	case status == http.StatusRequestTimeout, status >= http.StatusInternalServerError:
		// 超时与 5xx 都是上游侧故障：换渠道可能恢复，且该计入渠道健康度。
		return ClassUpstream
	case status >= http.StatusBadRequest:
		// 认不出的 4xx：多半是请求本身的问题，换渠道重试只会放大上游压力。
		return ClassRequest
	default:
		return ClassOther
	}
}

// ClassifyCode 按上游给出的类型名或机器码归类，认不出时返回 ClassOther。
//
// 用于非 2xx 里带结构化字段的场合：认不出说明既不能断言是请求问题、也不能断言是上游故障，
// 交给调用方按状态码兜底。
func ClassifyCode(values ...string) Class {
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if class, ok := codeRules[normalized]; ok {
			return class
		}
	}
	return ClassOther
}

// ClassifyErrorEnvelope 归类一个**出现在成功响应里**的错误信封。
//
// 与非 2xx 的差别在兜底：上游在 2xx 下发错误信封，本身就是上游侧故障（报文形态与状态码
// 互相矛盾），认不出的取值按 ClassUpstream 处理 —— 换渠道可恢复。
// 非 2xx 里认不出的错误不能这么判，那份保守留给 ClassifyCode。
func ClassifyErrorEnvelope(values ...string) Class {
	if class := ClassifyCode(values...); class != ClassOther {
		return class
	}
	return ClassUpstream
}

// hasCredentialToken 报告响应体是否出现明确的认证/权限失败字面量。
//
// 只在 4xx 上扫描：5xx 是上游侧故障，报文里偶然出现同名字面量不代表凭据有问题。
func hasCredentialToken(status int, body []byte) bool {
	if status < http.StatusBadRequest || status >= http.StatusInternalServerError {
		return false
	}
	lower := strings.ToLower(string(body))
	for _, token := range credentialTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}
