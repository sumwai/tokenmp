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
//
// statusOverloaded 是非标准状态码 529：Cloudflare 定义为「站点过载」。
//
// 它按定义是「忙」而不是「坏」，因此必须与 5xx 分开：当成上游故障会误计入渠道健康度，
// 使一条只是暂时过载的渠道被熔断。
const statusOverloaded = 529

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
			`余额不足|余额耗尽|账户余额|欠费|请充值|账[户号].{0,4}(停用|冻结)`)},
	{ClassRateLimit, regexp.MustCompile(
		`(?i)rate.?limit|too many requests|overloaded|` +
			`限流|请求过于频繁|请求过多|请求数过多|访问过于频繁|请求频率.{0,6}超|并发.{0,6}超`)},
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
	// 上游超时：单独成类，它对应 504 而非 502。
	"deadline_exceeded": ClassTimeout,
	"timeout":           ClassTimeout,
	// 请求本身与上游能力不匹配。
	"context_length_exceeded": ClassContextLength,
	"model_not_found":         ClassModelUnavailable,
}

// ClassifyHTTP 归类一次非 2xx 上游响应。
//
// 报文优先、状态码兜底：报文能区分同一状态码下的不同含义（429 既可能是限流，也可能是
// 余额耗尽），状态码只在报文没给出线索时用来认认证类与上游故障。
func ClassifyHTTP(status int, body []byte) Class {
	if class := matchPatterns(body); class != ClassOther {
		return class
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ClassAuth
	}
	if status == http.StatusTooManyRequests || status == statusOverloaded {
		return ClassRateLimit
	}
	if hasCredentialToken(status, body) {
		return ClassAuth
	}
	switch {
	case status == http.StatusRequestTimeout:
		return ClassTimeout
	case status >= http.StatusInternalServerError:
		// 5xx 是上游侧故障：换渠道可能恢复，且该计入渠道健康度。
		return ClassUpstream
	case status >= http.StatusBadRequest:
		// 认不出的 4xx：多半是请求本身的问题，换渠道重试只会放大上游压力。
		return ClassRequest
	default:
		return ClassOther
	}
}

// ClassifyWording 在自由文本上跑词表（上游消息、类型名或机器码），未命中时返回 ClassOther。
//
// 给适配器在结构化字段认不出时兜底：部分渠道只用 message 表达原因（限流与余额类尤多），
// 而帧路径原先只看结构化字段，于是同一句措辞在非 2xx 里归限流、在流式帧里退化成上游故障
// 并计入渠道健康度。有结构化字段时仍应优先用它——那比措辞可靠。
func ClassifyWording(text string) Class {
	return matchPatterns([]byte(strings.ToLower(strings.TrimSpace(text))))
}

// wordingScanHead 与 wordingScanTail 是措辞的扫描上限，单位字节。
//
// 分类原先跑在完整响应体上，而上游响应体上限是 32MB。实测代价：
//
//	 1KiB  1.0 ms
//	 1MiB  0.94 s
//	32MiB 30.6 s
//
// 即约 1.1 MB/s —— 少数带嵌套重复的正则在长串上退化得厉重，而不是报文本身慢。
// 错误信封实际只有几百字节到几 KB，扫全量换来的只是「关键词埋在几十 MB 之后」这种病态
// 情形，代价却落在**每一次失败转发**的关键路径上：一条坏上游能靠大报文把 CPU 吃满。
//
// 改为只扫首部一段与尾部一段：首部覆盖正常错误报文，尾部覆盖把结论写在报文末尾的上游
// （部分渠道先回显请求再报错）。超限时调用方会给出一条可见信号，
// 使「可能没扫到」不被静默吞掉，也确实需要时还能用插件钩子补规则。
const (
	wordingScanHead = 16 << 10
	wordingScanTail = 4 << 10
)

// WordingScanLimited 报告该长度的报文不会被完整扫到，供调用方记录可观测信号。
func WordingScanLimited(bodyLength int) bool {
	return bodyLength > wordingScanHead+wordingScanTail
}

// scanTargets 返回措辞规则要扫的片段；报文未超限时就是它自己。
func scanTargets(body []byte) [][]byte {
	if !WordingScanLimited(len(body)) {
		return [][]byte{body}
	}
	return [][]byte{body[:wordingScanHead], body[len(body)-wordingScanTail:]}
}

// matchPatterns 在报文或结构化取值上跑词表，未命中时返回 ClassOther。
//
// 两条路径共用同一份词表：非 2xx 把它跑在报文上，结构化取值把它当变体兜底。
// 分开写会让两处慢慢漂开，而漂开的后果是同一件事实在流式与非流式下得到不同处置。
func matchPatterns(text []byte) Class {
	for _, target := range scanTargets(text) {
		for _, rule := range classRules {
			if rule.pattern.Match(target) {
				return rule.class
			}
		}
	}
	return ClassOther
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
		// 精确取值未命中时退回报文词表：结构化取值常是词表词汇的变体
		// （overloaded_error 对 overloaded、rate_limit_error 对 rate limit），
		// 而词表已经积累了各厂商的措辞。不退回会退化成「认不出」，
		// 从而把一件「渠道还活着」的事记成渠道故障。
		if class := matchPatterns([]byte(normalized)); class != ClassOther {
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
