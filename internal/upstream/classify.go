// 本文件只剩「厂商声明如何覆盖凭据处置」这一件与厂商打交道的事。
//
// 分类（状态码 + 报文正则）与处置（类别到动作的策略表）都已收敛到 internal/failure；
// 此处只负责把渠道声明的信标头叠加上去。
package upstream

import (
	"net/http"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
)

// credentialActions 是凭据相关的动作，被信标头否认时整组摘掉。
//
// 摘「停用」与「换下一条」一起：厂商声明这个状态码不代表凭据有问题，那么既不该把本次
// 凭据停掉，也不该为它再换一条 key —— 后者会让一次与凭据无关的失败放大成对整组 key 的枚举。
const credentialActions = failure.ActionSuspendAccount | failure.ActionRetryNextAccount

// credentialOutcome 是「本次上游响应下凭据该如何处置」的判定结论。
type credentialOutcome struct {
	// actions 是本次失败的处置集合：由类别给出，再按渠道声明覆盖。
	actions failure.Action
	// class 是本次失败的分类，供日志按类聚合。
	class failure.Class
	// renewed 报告上游声明本次凭据登录态已续期（解除既有冷却）。
	renewed bool
}

// decideCredentialOutcome 是凭据处置判定的唯一出处。
//
// 判据分两层：先按状态码与报文分类（failure.ClassifyHTTP），再取该类别的策略表处置；
// 若渠道声明了信标头且取值命中，则覆盖**动作**。
//
// 覆盖的是动作而不是类别：信标头说的是「这个状态码不代表凭据有问题」，不是
// 「这次失败不算认证问题」。把类别也改掉会让尝试日志与排障失去线索，而类别本身
// 只影响处置的默认取值与日志，不参与控制流。
func decideCredentialOutcome(signin domain.SigninHeader, status int, header http.Header, body []byte) credentialOutcome {
	class := failure.ClassifyHTTP(status, body)
	switch signin.Verdict(header) {
	case domain.SigninExpired:
		// 厂商声明登录态失效：这是该厂商对凭据状态的直接断言，优先于由报文与状态码的
		// 间接推断。归类记为 auth 而非沿用推断值，避免出现「429 配 quota 却不按额度冷却」
		// 这类自相矛盾的日志：信标头命中时，推断本身已经不可信。
		return credentialOutcome{class: failure.ClassAuth, actions: failure.ActionsFor(failure.ClassAuth)}
	case domain.SigninKept, domain.SigninRenewed:
		// 两者都是厂商在说「凭据不是问题所在」：kept 是「状态码不代表凭据失效」，
		// renewed 是「登录态刚续期」。因此都摘掉整组凭据动作，类别保留。
		// renewed 另请解除既有冷却。
		return credentialOutcome{
			class:   class,
			actions: failure.ActionsFor(class).Without(credentialActions),
			renewed: signin.Verdict(header) == domain.SigninRenewed,
		}
	default:
		return credentialOutcome{class: class, actions: failure.ActionsFor(class)}
	}
}
