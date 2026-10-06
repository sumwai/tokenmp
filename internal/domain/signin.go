package domain

import (
	"net/http"
	"strings"
)

// SigninHeader 是渠道在 config 里声明的登录态信标头映射。
//
// 头名与三种取值全部来自渠道配置，不在代码里硬编码任何厂商取值：新厂商接入 = 写一段配置。
// 三种取值对应三种语义动作，判定只在本类型内实现一处，
// 上游客户端据此决定「本次响应下凭据该不该进入冷却、要不要解除冷却」。
type SigninHeader struct {
	// Name 是承载登录态的信标头名。为空表示渠道未声明信标头。
	Name string
	// Expired 是「登录态已失效」的取值。
	Expired string
	// Kept 是「登录态保持有效」的取值。
	Kept string
	// Renewed 是「登录态已续期」的取值。
	Renewed string
}

// SigninVerdict 是信标头给出的一次登录态结论。
type SigninVerdict int

const (
	// SigninUnknown 表示未声明信标头，或本次响应未命中任何已声明的取值。
	SigninUnknown SigninVerdict = iota
	// SigninExpired 表示上游声明登录态已失效。
	SigninExpired
	// SigninKept 表示上游声明登录态仍然有效。
	SigninKept
	// SigninRenewed 表示上游声明登录态已续期。
	SigninRenewed
)

// Configured 报告渠道是否声明了可用于判定的信标头。
//
// 只认头名：取值齐不齐由 Verdict 按未命中的口径处理，未声明的取值不会命中任何响应。
func (h SigninHeader) Configured() bool {
	return strings.TrimSpace(h.Name) != ""
}

// Verdict 读 header 里的信标头取值并给出结论。
//
// 取值比较忽略大小写与首尾空白：厂商取值大小写不统一，且该判定不涉安全。
// 未声明信标头、响应未带该头、或取值不在已声明的三种之内时返回 SigninUnknown，
// 由调用方回退到状态码启发式，行为与未引入信标头时完全一致。
//
// 三种取值在配置里重复时按 Expired、Kept、Renewed 的顺序取第一个命中：
// 失效结论最保守，重复配置下宁可冷却也不放过。
func (h SigninHeader) Verdict(header http.Header) SigninVerdict {
	if !h.Configured() {
		return SigninUnknown
	}
	value := strings.TrimSpace(header.Get(h.Name))
	if value == "" {
		return SigninUnknown
	}
	switch {
	case h.Expired != "" && strings.EqualFold(value, strings.TrimSpace(h.Expired)):
		return SigninExpired
	case h.Kept != "" && strings.EqualFold(value, strings.TrimSpace(h.Kept)):
		return SigninKept
	case h.Renewed != "" && strings.EqualFold(value, strings.TrimSpace(h.Renewed)):
		return SigninRenewed
	default:
		return SigninUnknown
	}
}

// Strip 从 header 里移除信标头。
//
// 信标头承载上游内部语义，不应随响应外泄给客户端；在消费掉取值之后立即剥除，
// 使后续任何读取该响应头的路径都拿不到它。未声明或响应未带该头时为空操作。
func (h SigninHeader) Strip(header http.Header) {
	if header == nil || !h.Configured() {
		return
	}
	header.Del(h.Name)
}
