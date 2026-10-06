// 本文件是「上游失败分类」的唯一出处。
//
// 做法借鉴 Magpie 的 internal/gateway/classify.go：判据是「状态码 + 报文正则」一起看，
// 而不是只看状态码。
//
// 只看状态码不够，因为同一个码在不同上游下含义不同：余额耗尽可能报成 429 配
// insufficient_quota，只看 429 会把它当成限流退避重试 —— 而那条路径无论重试多少次
// 都不会成功，真正该做的是换一条凭据。反过来，403 既可能是 key 无效，也可能是账号被
// 停用：两者都要换 key，但被停用的那把要停得更久。
//
// 分类只回答「这是什么失败」；处置由 failurePolicyOf 给出，调用方据此决定是否停用
// 凭据、停用多久。新增一类失败只加一个枚举、一条策略与一条规则，不改调用方。
package upstream

import (
	"net/http"
	"regexp"
	"time"
)

// upstreamFailure 是上游失败的种类。
type upstreamFailure int

const (
	// failureOther 未能归类：不改变既有处置。
	failureOther upstreamFailure = iota
	// failureAuth 凭据或权限不被接受。
	failureAuth
	// failureQuota 账号的套餐额度、窗口额度或 plan 限额用尽。
	failureQuota
	// failureCredit 账号余额或授信耗尽（欠费、停服）。
	failureCredit
	// failureRateLimit 限流或上游过载。
	failureRateLimit
	// failureContextLength 上下文超出模型窗口。
	failureContextLength
	// failureModelUnavailable 模型不存在或不可用。
	failureModelUnavailable
)

// failurePolicy 是一个失败种类的处置。
type failurePolicy struct {
	// name 是写进日志的分类名，取值稳定，供按类聚合与排障。
	name string
	// cooldown 是「本次失败后把该凭据停用多久」的建议；0 表示用调用方配置的默认冷却。
	//
	// 额度与余额是账号级事实：那把 key 短期内不会自行恢复，停用比反复重试省事。
	// 认证失败沿用既有默认冷却，不在分类里另立数值。
	cooldown time.Duration
}

// 冷却时长取自 Magpie 的同类取值，口径是「这类问题通常多久能恢复」：
// 额度等窗口滚动或运营加额，余额等充值，两者都比一次限流久得多。
const (
	// quotaCooldown 是套餐额度用尽后的凭据停用时长。
	quotaCooldown = 15 * time.Minute
	// creditCooldown 是余额耗尽后的凭据停用时长；比额度久，因为它要等真金白银到账。
	creditCooldown = 30 * time.Minute
)

var failurePolicies = map[upstreamFailure]failurePolicy{
	failureOther:            {name: "other"},
	failureAuth:             {name: "auth"},
	failureQuota:            {name: "quota", cooldown: quotaCooldown},
	failureCredit:           {name: "credit", cooldown: creditCooldown},
	failureRateLimit:        {name: "rate_limit"},
	failureContextLength:    {name: "context_length"},
	failureModelUnavailable: {name: "model_unavailable"},
}

// failurePolicyOf 取一个失败种类的处置；未登记的种类按 failureOther 处理。
func failurePolicyOf(f upstreamFailure) failurePolicy {
	if policy, ok := failurePolicies[f]; ok {
		return policy
	}
	return failurePolicies[failureOther]
}

// failureRejectsCredential 报告该失败是否说明「本次凭据不可用」，应当换下一条。
//
// 额度与余额是账号级事实，换 key 是唯一出路；而限流、上下文超限与模型不可用换 key
// 也不会有用。把后者也算作拒绝，只会让一次参数类错误放大成对整组 key 的枚举 ——
// 轮换器单次尝试最多试 4 条，但那是防御上限，不该被当成常规路径。
func failureRejectsCredential(f upstreamFailure) bool {
	switch f {
	case failureAuth, failureQuota, failureCredit:
		return true
	default:
		return false
	}
}

// failureRule 是按报文归类的规则。
type failureRule struct {
	failure upstreamFailure
	pattern *regexp.Regexp
}

// failurePatterns 按优先级排列：越具体的规则越靠前。
//
// 三条顺序都有真实报文依据，换一下就会误分类：
//
//   - 上下文超限排在额度与限流之前：它报文里的 maximum / limit / exceeded 会被后面的
//     宽规则抢走，把一次参数问题认成账号问题，误停一整组 key。
//   - 限流排在额度之前：「Rate limit reached」里的 limit reached 命中额度规则，
//     而它显然不是额度用尽。
//   - 余额排在额度之前：「余额不足」与「额度用尽」的冷却时长不同，而前者报文里常同时
//     出现 quota / billing 一类词。厂商在让你去付钱就按余额停，不按窗口滚动停。
var failurePatterns = []failureRule{
	{failureContextLength, regexp.MustCompile(
		`(?i)context_length_exceeded|prompt is too long|input is too long|` +
			`(exceeds?|exceeded|over|beyond)( the)?( model'?s?)?( maximum)? context|` +
			`context (length|limit|window) (exceeded|is exceeded)|maximum context|` +
			`上下文长度|超出.{0,8}上下文|超过.{0,8}上下文`)},
	{failureCredit, regexp.MustCompile(
		`(?i)insufficient.?(balance|credit|fund)|balance|credit|billing|payment|arrear|overdue|suspended|` +
			`余额不足|余额耗尽|账户余额|欠费|请充值|账户.{0,4}(停用|冻结)`)},
	{failureRateLimit, regexp.MustCompile(
		`(?i)rate.?limit|too many requests|overloaded|` +
			`限流|请求过于频繁|请求频率.{0,6}超|并发.{0,6}超`)},
	{failureQuota, regexp.MustCompile(
		`(?i)quota|usage.?limit|limit.?reached|hit your .*limit|limit.{0,24}resets|exceeded.*(plan|limit)|` +
			`配额|额度.{0,6}(用尽|不足|已用完)|用量上限|已达.{0,6}(上限|限额)`)},
	{failureModelUnavailable, regexp.MustCompile(
		`(?i)model.{0,80}(not (supported|accessible|available|found|enabled|allowed)|unsupported|unavailable|does ?n[o']t exist|unknown|invalid)|` +
			`(no such|unknown|invalid|unsupported) model|model_not_found|` +
			`模型.{0,10}(不存在|不可用|不支持|未开通)`)},
}

// classifyUpstreamFailure 判定一次非 2xx 上游响应的失败种类。
//
// 报文优先、状态码兜底：报文能区分同一状态码下的不同含义（429 既可能是限流，也可能是
// 余额耗尽），状态码只在报文没给出线索时用来认认证类失败。
//
// 已知取舍：报文规则里有 balance / credit 一类宽词，理论上存在误判（例如某个参数校验
// 错误恰好提到同名参数）。照搬 Magpie 的宽匹配是为了覆盖各厂商五花八门的措辞，代价由
// 轮换器的「单次尝试最多试 4 条」上限兜住，不会放大成对整组 key 的枚举。
func classifyUpstreamFailure(status int, body []byte) upstreamFailure {
	for _, rule := range failurePatterns {
		if rule.pattern.Match(body) {
			return rule.failure
		}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return failureAuth
	}
	if status == http.StatusTooManyRequests {
		return failureRateLimit
	}
	if hasCredentialFailureToken(status, body) {
		return failureAuth
	}
	return failureOther
}
