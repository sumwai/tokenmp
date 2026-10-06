// Package route 实现选路的两件事：同优先级候选的加权随机首选，以及同协议段与跨协议段的拼链。
//
// 从 cmd 下沉到本包：加权与分层是纯规则，不依赖数据库与 HTTP，单独成包后可用表驱动
// 单测覆盖，命令包只做装配。端点地址拼接（endpointURL）与选路结果映射（routeOf）
// 同属选路口径，一并放在这里。
//
// 随机源以 intN 注入：默认为 math/rand，测试注入确定性序列后即可断言「选中了哪一条」，
// 而不必对分布做统计性断言。
package route

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// defaultIntN 是未注入随机源时使用的默认实现：返回 [0, n) 内的均匀随机整数。
//
// 负重选路不涉安全，无需密码学随机源；用 math/rand 避免为一次选路引入熵池开销。
func defaultIntN(n int) int {
	//nolint:gosec // G404：加权随机只在候选渠道间分配流量，不用于任何安全用途。
	return rand.Intn(n)
}

// pickWeightedIndex 在权重表上按权重加权随机返回一个下标。
//
// 权重不大于 0 的条目按 1 处理；因此全部权重非正时每条权重都为 1，结果退化为均匀随机。
// intN 返回 [0, n) 内的整数，n 为权重总和且必为正；取值越界时钳制回合法区间，
// 使注入的测试随机源不必额外保证边界。
func pickWeightedIndex(weights []int, intN func(int) int) int {
	if len(weights) == 0 {
		return 0
	}
	total := 0
	for _, weight := range weights {
		total += effectiveWeight(weight)
	}
	if intN == nil {
		intN = defaultIntN
	}
	roll := intN(total)
	if roll < 0 {
		roll = 0
	}
	if roll >= total {
		roll = total - 1
	}
	for i, weight := range weights {
		next := roll - effectiveWeight(weight)
		if next < 0 {
			return i
		}
		roll = next
	}
	return len(weights) - 1
}

// effectiveWeight 把非正权重收敛为 1。
func effectiveWeight(weight int) int {
	if weight < 1 {
		return 1
	}
	return weight
}

// sanitizeRequestOverrides 校验渠道级请求覆盖项是 JSON 对象，非法时记日志并丢弃。
//
// 覆盖项来自库表的 JSON 列，可能是人工写坏的文本。丢弃而不是让改写链报错，
// 是为了一项可选的调参不中断一次本可完成的转发；形状在此处一次收敛，
// 改写链因此可以假定拿到的覆盖项是对象。空值与 JSON null 视为未配置。
func sanitizeRequestOverrides(c store.RouteCandidate) json.RawMessage {
	trimmed := bytes.TrimSpace(c.RequestOverrides)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &patch); err != nil || patch == nil {
		slog.Warn("渠道请求覆盖项不是合法 JSON 对象，已跳过",
			"channel_id", c.ChannelID, "error", err)
		return nil
	}
	return json.RawMessage(trimmed)
}

// orderCandidatesByWeight 把候选按同优先级组重排。
//
// 入参候选已按 priority 降序、同优先级内按渠道 id 稳定排序。本函数只在每组内部调整：
// 加权随机选中一条提到该组首位作为首选，组内其余候选保持原有相对顺序留在后面供回退；
// 组与组之间的先后顺序不变。单条候选的组不做任何改动。
func orderCandidatesByWeight(candidates []store.RouteCandidate, intN func(int) int) {
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && candidates[end].Priority == candidates[start].Priority {
			end++
		}
		if end-start > 1 {
			weights := make([]int, end-start)
			for i := range weights {
				weights[i] = candidates[start+i].Weight
			}
			picked := start + pickWeightedIndex(weights, intN)
			if picked != start {
				chosen := candidates[picked]
				// 把 start..picked-1 整体后移一位，再把选中项放到组首。
				copy(candidates[start+1:picked+1], candidates[start:picked])
				candidates[start] = chosen
			}
		}
		start = end
	}
}

// RouteChain 把两级候选拼成本次请求的唯一回退链：同协议候选成段在前，跨协议候选补段在后。
//
// 分层与降级顺序只有本函数一处实现：
//
//  1. 各段内部按优先级组加权随机定首选；段与段之间的先后不参与随机；
//  2. 跨协议段去掉已出现在同协议段的渠道 —— 不限协议查询必然包含同协议行，
//     不去重会让同一条渠道在同一请求里被重试两次；
//  3. 跨协议段只保留两侧协议都能经统一内部格式重建的候选：渠道方言来自库表，
//     可能是本版本不认识的取值，跳过该行而不是让整批候选在流水线里报错。
//
// 同协议候选始终排在跨协议候选之前：同协议透传的保真度与延迟优于重建，
// 低优先级的同协议候选也先于高优先级的跨协议候选，只在同协议缺位或耗尽时才降到跨协议。
// 空表入参合法：两段都为空时返回空链，由流水线回「没有可用渠道」。
func RouteChain(client domain.Protocol, sameProtocol, crossProtocol []store.RouteCandidate, intN func(int) int) []domain.Route {
	sameSegment := append([]store.RouteCandidate(nil), sameProtocol...)
	orderCandidatesByWeight(sameSegment, intN)
	crossSegment := append([]store.RouteCandidate(nil), crossProtocol...)
	orderCandidatesByWeight(crossSegment, intN)
	routes := make([]domain.Route, 0, len(sameSegment)+len(crossSegment))
	served := make(map[uint64]struct{}, len(sameSegment))
	for _, candidate := range sameSegment {
		served[candidate.ChannelID] = struct{}{}
		routes = append(routes, routeOf(candidate, client))
	}
	for _, candidate := range crossSegment {
		if _, duplicated := served[candidate.ChannelID]; duplicated {
			continue
		}
		upstream := domain.Protocol(candidate.ChannelType)
		if !crossProtocolRebuildable(client, upstream) {
			continue
		}
		routes = append(routes, routeOf(candidate, upstream))
	}
	return routes
}

// crossProtocolRebuildable 报告客户端协议与上游协议能否经统一内部格式互相重建。
//
// 两侧都要可重建：只有一侧支持时，重建所需的字段在一侧缺位，转换会产出残缺请求或响应。
func crossProtocolRebuildable(client, upstream domain.Protocol) bool {
	return client.CrossProtocolRebuildable() && upstream.CrossProtocolRebuildable()
}

// routeOf 把一条候选渠道映射为选路结果；protocol 是本次转发实际使用的上游协议。
//
// 端点地址按本次的上游协议拼接（而非客户端协议）：跨协议补段时上游说的是另一种方言，
// 端点段必须跟着上游协议走。
func routeOf(candidate store.RouteCandidate, protocol domain.Protocol) domain.Route {
	return domain.Route{
		ChannelID:             candidate.ChannelID,
		UpstreamID:            strconv.FormatUint(candidate.ChannelID, 10),
		Protocol:              protocol,
		UpstreamModel:         candidate.UpstreamModel,
		BaseURL:               endpointURL(candidate.BaseURL, protocol),
		CredentialRef:         candidate.CredGroup,
		RequestOverrides:      sanitizeRequestOverrides(candidate),
		SigninHeader:          parseSigninHeader(candidate.Config),
		OAuthProfile:          ParseOAuthProfile(candidate.Config),
		RateLimitQPS:          candidate.RateLimitQPS,
		RateLimitConcurrency:  candidate.RateLimitConcurrency,
		CredentialHeaderStyle: parseCredentialStyle(candidate.Config),
		Headers:               parseHeaders(candidate.Config),
	}
}

// channelConfig 是 upstream_channel.config 里本网关认识的键。
//
// config 的结构随协议方言与厂商演进，不为此改表；不认识的键照常忽略。
type channelConfig struct {
	// SigninHeader 声明本渠道的登录态信标头映射。
	SigninHeader signinHeaderConfig `json:"signin_header"`
	// CredentialStyle 覆盖本渠道凭据的注入形态，取值与 domain.CredentialHeaderStyle 一致。
	// 典型取值：query 表示改用 ?key=<凭据> 查询参数形态。
	CredentialStyle string `json:"credential_style"`
	// OAuth 声明本渠道的 OAuth 端点画像，供订阅型凭据登录与续期使用。
	OAuth oauthProfileConfig `json:"oauth"`
	// Headers 声明随每次上游请求固定发出的静态请求头，供强制要求自定义头的上游使用。
	Headers map[string]string `json:"headers"`
}

// parseHeaders 从渠道 config JSON 里读静态请求头。
//
// 读取口径与 parseCredentialStyle 一致：config 是人工写入的 JSON 列，写坏的文本、
// 缺键或类型不符都只让本渠道没有额外请求头，不得让整次选路失败。两类头名丢弃：去空白
// 后为空的（发出去只会被上游忽略），以及由网关自身占用的（见
// domain.IsReservedUpstreamHeader，写入入口会拒绝，这里是兼底）。
func parseHeaders(raw []byte) http.Header {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var cfg channelConfig
	if err := json.Unmarshal(trimmed, &cfg); err != nil {
		return nil
	}
	var headers http.Header
	for name, value := range cfg.Headers {
		name = strings.TrimSpace(name)
		if name == "" || domain.IsReservedUpstreamHeader(name) {
			continue
		}
		if headers == nil {
			headers = make(http.Header, len(cfg.Headers))
		}
		headers[http.CanonicalHeaderKey(name)] = []string{value}
	}
	return headers
}

// WithClientHeaderOverrides 返回一份新的选路结果：渠道静态头的取值换成客户端同名头的取值。
//
// 语义是「有则透传、无则兜底」：客户端带了声明过的头名就用客户端的值，没带就保留
// 渠道 config 的取值。遍历范围只有渠道自己声明的头名 —— 声明名即操作者授权的透传
// 范围，客户端带的其它头一律不进入上游请求。
//
// 凭据头的优先级不在这里处理：客户端值同样写进 Route.Headers，随后由凭据提供者按
// 「凭据头优先于 route.Headers」的既有规则合并，所以客户端顶替不了上游凭据。
func WithClientHeaderOverrides(routes []domain.Route, client http.Header) []domain.Route {
	if len(client) == 0 {
		return routes
	}
	overridden := make([]domain.Route, 0, len(routes))
	for _, route := range routes {
		overridden = append(overridden, withClientHeaderOverride(route, client))
	}
	return overridden
}

// withClientHeaderOverride 处理单条选路结果：没有声明静态头时原样返回。
func withClientHeaderOverride(route domain.Route, client http.Header) domain.Route {
	if len(route.Headers) == 0 {
		return route
	}
	merged := make(http.Header, len(route.Headers))
	for name, values := range route.Headers {
		if fromClient := client.Values(name); len(fromClient) > 0 {
			merged[name] = append([]string(nil), fromClient...)
			continue
		}
		merged[name] = values
	}
	route.Headers = merged
	return route
}

// oauthProfileConfig 是 config 里 oauth 的结构。
type oauthProfileConfig struct {
	AuthorizeURL string `json:"authorize_url"`
	TokenURL     string `json:"token_url"`
	DeviceURL    string `json:"device_url"`
	ClientID     string `json:"client_id"`
	Scope        string `json:"scope"`
}

// ParseOAuthProfile 从渠道 config JSON 里读 OAuth 端点画像。
//
// 读取口径与 parseSigninHeader 一致：config 是人工写入的 JSON 列，写坏的文本、
// 缺键或类型不符都只让本渠道退化为「未声明画像」，不得让整次选路失败。
func ParseOAuthProfile(raw []byte) domain.OAuthProfile {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return domain.OAuthProfile{}
	}
	var cfg channelConfig
	if err := json.Unmarshal(trimmed, &cfg); err != nil {
		return domain.OAuthProfile{}
	}
	profile := domain.OAuthProfile{
		AuthorizeURL: strings.TrimSpace(cfg.OAuth.AuthorizeURL),
		TokenURL:     strings.TrimSpace(cfg.OAuth.TokenURL),
		DeviceURL:    strings.TrimSpace(cfg.OAuth.DeviceURL),
		ClientID:     strings.TrimSpace(cfg.OAuth.ClientID),
		Scope:        strings.TrimSpace(cfg.OAuth.Scope),
	}
	if !profile.Configured() {
		return domain.OAuthProfile{}
	}
	return profile
}

// signinHeaderConfig 是 config 里 signin_header 的结构。
type signinHeaderConfig struct {
	Name   string             `json:"name"`
	Values signinHeaderValues `json:"values"`
}

// signinHeaderValues 是信标头的三种取值，取值本身也由配置给出。
type signinHeaderValues struct {
	Expired string `json:"expired"`
	Kept    string `json:"kept"`
	Renewed string `json:"renewed"`
}

// parseSigninHeader 从渠道 config JSON 里读登录态信标头映射。
//
// 读取必须宽容：config 是人工写入的 JSON 列，写坏的文本、缺键或类型不符都只让本渠道
// 回退到状态码启发式，不得让整次选路失败。未声明头名时返回零值，与未配置等价。
func parseSigninHeader(raw []byte) domain.SigninHeader {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return domain.SigninHeader{}
	}
	var cfg channelConfig
	if err := json.Unmarshal(trimmed, &cfg); err != nil {
		return domain.SigninHeader{}
	}
	header := domain.SigninHeader{
		Name:    strings.TrimSpace(cfg.SigninHeader.Name),
		Expired: cfg.SigninHeader.Values.Expired,
		Kept:    cfg.SigninHeader.Values.Kept,
		Renewed: cfg.SigninHeader.Values.Renewed,
	}
	if !header.Configured() {
		return domain.SigninHeader{}
	}
	return header
}

// parseCredentialStyle 从渠道 config JSON 里读凭据注入形态。
//
// 读取必须宽容：config 是人工写入的 JSON 列，写坏的文本、未知取值都只让本渠道回退到
// 按协议现状注入，不得让整次选路失败。未知取值不猜：猜错会把凭据发到错误的请求头里。
func parseCredentialStyle(raw []byte) domain.CredentialHeaderStyle {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return domain.CredentialHeaderAuto
	}
	var cfg channelConfig
	if err := json.Unmarshal(trimmed, &cfg); err != nil {
		return domain.CredentialHeaderAuto
	}
	style := domain.CredentialHeaderStyle(cfg.CredentialStyle)
	if !style.Valid() {
		return domain.CredentialHeaderAuto
	}
	return style
}

// endpointURL 把库里存的根地址与协议端点段拼成完整上游地址。
//
// 库里只存到端点段之前（如 https://host/api/v3）：同一台主机上的不同协议端点因此共享一段根地址，
// 端点段由本次协议决定。拼接去掉根地址末尾的斜杠，避免出现 //chat/completions 这样的双斜杠。
func endpointURL(base string, protocol domain.Protocol) string {
	root := strings.TrimRight(strings.TrimSpace(base), "/")
	if root == "" {
		return ""
	}
	segment := protocol.EndpointSegment()
	if segment == "" {
		// 端点段为空的协议（Gemini）由适配器在调用上游时按本次请求拼端点
		// （/models/{model}:generateContent 含模型名），此处只回根地址。
		return root
	}
	return root + segment
}
