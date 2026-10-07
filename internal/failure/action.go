// Package failure 是上游失败处理的唯一出处：诊断（Class）、处置（Policy）与动作（Action）。
//
// 为什么单独成包：失败处理原先散在五处消费、四套词汇里 ——
//
//	凭据轮换   读 domain.CredentialRejected + CredentialCooldownOf
//	候选回退   读 domain.Retryable
//	熔断计数   读 domain.Error.Code
//	分类日志   读 domain.FailureClassOf
//	终态判定   由以上都不成立推出
//
// 同一份上游报文，换个入口就会得到不同待遇，加一条厂商措辞要改多处。现在消费方只有
// 一个入口：ActionsOf(err)。
//
// 本包依赖 domain 只为构造统一错误（Error 是全网关的错误载体），domain 不反向依赖本包。
package failure

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Class 是上游失败的诊断类别。
//
// 只有诊断、不含处置：类别回答「这是什么失败」，动作回答「接下来做什么」。
// 两者分开是为了让「加一种厂商措辞」与「改一次处置」互不牵动。
type Class int

const (
	// ClassOther 是未能归类的失败。处置取最保守的一档：回客户端，不放大成重试。
	ClassOther Class = iota
	// ClassRequest 是请求级错误（参数、端点、模型名写法），换渠道重试无用。
	ClassRequest
	// ClassAuth 是凭据或权限不被接受。
	ClassAuth
	// ClassQuota 是账号的套餐额度、窗口额度或 plan 限额用尽。
	ClassQuota
	// ClassCredit 是账号余额或授信耗尽（欠费、停服）。
	ClassCredit
	// ClassRateLimit 是限流或上游过载。
	ClassRateLimit
	// ClassContextLength 是上下文超出模型窗口。
	ClassContextLength
	// ClassModelUnavailable 是模型不存在或不可用。
	ClassModelUnavailable
	// ClassUpstream 是上游自身故障：5xx 与连接失败。
	//
	// 它与 ClassOther 分开是必要的：两者都「认不出具体原因」，但一个必须换渠道重试，
	// 另一个不该放大成重试。合成一类就只能靠状态码二次判断，那可是四套词汇的起点。
	ClassUpstream
	// ClassTimeout 是上游超时：网络超时、上游 408、流式空闲超时。
	//
	// 处置与 ClassUpstream 完全相同，单独成类只为一件事：超时对应 504、其余上游故障
	// 对应 502，把两者合并会让客户端收到错误的分类码，而错误码只能从类别推导。
	// 它也确实是不同的诊断事实，`failure_class=timeout` 在日志里有单独价值。
	ClassTimeout
)

// 类别名是日志取值，稳定不变，供按类聚合。
const (
	classNameOther            = "other"
	classNameRequest          = "request"
	classNameAuth             = "auth"
	classNameQuota            = "quota"
	classNameCredit           = "credit"
	classNameRateLimit        = "rate_limit"
	classNameContextLength    = "context_length"
	classNameModelUnavailable = "model_unavailable"
	classNameUpstream         = "upstream"
	classNameTimeout          = "timeout"
)

// Action 是对本次失败的一项处置，同时也是多项处置的集合：单个取值是一项动作，
// 位或起来就是一个处置集合。
//
// 用一个类型而不是两个（Action 与 ActionSet）：两者语义同一，分开只会到处要类型转换，
// 且容易写出「集合里塞了另一个集合」这种无意义的取值。
//
// 分两组：
//
//	重试   换凭据 / 换渠道。两者可同时出现，语义是**升级顺序**：先把同渠道的
//	       下一条凭据试完，都撑不住再换渠道。它们与 ActionSurface 互斥。
//	副作用 停用凭据 / 计入熔断，可自由叠加。
//
// ActionSurface 不是「三选一的第三项」，而是「不重试」的显式声明：
// 让它与重试动作互斥，而不是靠「没有重试动作」隐含表达，才能被断言。
type Action uint16

const (
	// ActionRetryNextAccount 在同一条渠道内换下一条凭据重试。
	//
	// 它不蕴含停用：换 key 与「那把 key 有问题」是两件事。按请求计的 429 会让换 key
	// 有意义，但不应把 key 停掉——所以 ActionSuspendAccount 是独立的副作用。
	ActionRetryNextAccount Action = 1 << iota
	// ActionRetryNextRoute 换下一条候选渠道重试（带退避）。
	//
	// 与 ActionRetryNextAccount 同时出现时表示升级顺序：先把同渠道的凭据试完。
	ActionRetryNextRoute
	// ActionSurface 不再重试，把错误交给客户端。
	//
	// 它与两个重试动作互斥；未带分类的错误在 NextStep() 下也落到它。
	ActionSurface
	// ActionSuspendAccount 停用本次凭据一段时间，时长由类别的策略给出。
	ActionSuspendAccount
	// ActionCountBreaker 把本次失败计入渠道熔断的连续失败计数。
	//
	// 它是副作用而不是下一步：熔断判的是渠道健康度，与「这次换谁重试」无关。
	ActionCountBreaker
)

// retryActions 是两个重试动作，按「先便宜后费事」排列。
//
// 换凭据只需在组内推进游标，换渠道要重新选路并退避，所以前者优先。
var retryActions = []Action{ActionRetryNextAccount, ActionRetryNextRoute}

// Has 报告集合是否含某个动作。
func (a Action) Has(action Action) bool { return a&action != 0 }

// Retryable 报告集合是否允许重试（换凭据或换渠道）。
//
// 消费方问「能不能重试」时用这个，不要用 NextStep() != ActionSurface：
// 后者的可读性差，且把「下一步是哪个」与「能不能下一步」两件事混在一起。
func (a Action) Retryable() bool { return a.Has(ActionRetryNextAccount) || a.Has(ActionRetryNextRoute) }

// Without 返回去掉若干动作后的集合。
//
// 用于「类别没错、但某组动作被上游显式否认」这类覆盖：渠道声明的信标头说
// 「这个状态码不代表凭据有问题」时，摘掉整组凭据动作而保留类别。
func (a Action) Without(actions Action) Action { return a &^ actions }

// NextStep 返回集合里优先尝试的重试动作；不可重试时返回 ActionSurface。
//
// 它回答的是「先试哪个」：集合里可能同时允许换凭据与换渠道（升级顺序），
// 此处给出便宜的那一个，用尽后的升级由消费方按 Has 自行推进。
func (a Action) NextStep() Action {
	for _, action := range retryActions {
		if a.Has(action) {
			return action
		}
	}
	return ActionSurface
}

// String 返回集合的可读形态，供日志与测试断言使用。
func (a Action) String() string {
	if a == 0 {
		return "none"
	}
	all := append(append([]Action{}, retryActions...), ActionSurface, ActionSuspendAccount, ActionCountBreaker)
	names := make([]string, 0, len(all))
	for _, action := range all {
		if a.Has(action) {
			names = append(names, actionName(action))
		}
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

// actionName 返回动作的可读名。
func actionName(action Action) string {
	switch action {
	case ActionRetryNextAccount:
		return "retry_next_account"
	case ActionRetryNextRoute:
		return "retry_next_route"
	case ActionSurface:
		return "surface"
	case ActionSuspendAccount:
		return "suspend_account"
	case ActionCountBreaker:
		return "count_breaker"
	default:
		return fmt.Sprintf("action(%d)", uint16(action))
	}
}

// Policy 是一个类别的处置。
type Policy struct {
	// Name 是写进日志的分类名。
	Name string
	// Actions 是该类别的处置集合：或允许重试（换凭据 / 换渠道，可同时），
	// 或显式声明不重试（ActionSurface）；副作用按需叠加。
	Actions Action
	// SuspendFor 在 Actions 含 ActionSuspendAccount 时给出停用时长。
	//
	// 为零表示用调用方配置的默认冷却（TOKENMP_UPSTREAM_CREDENTIAL_COOLDOWN）：
	// 认证类失败不另立数值，它有现成的运营旋钮，分类不该把它接管。
	SuspendFor time.Duration
}

// 停用时长取自 Magpie 的同类取值，口径是「这类问题通常多久能恢复」：
// 额度等窗口滚动或运营加额，余额等充值，两者都比一次限流久得多。
const (
	// quotaSuspend 是套餐额度用尽后的凭据停用时长。
	quotaSuspend = 15 * time.Minute
	// creditSuspend 是余额耗尽后的凭据停用时长；比额度久，因为它要等真金白银到账。
	creditSuspend = 30 * time.Minute
)

// policyTable 是处置的唯一出处。
//
// 额度与余额既要换凭据又要换渠道：同一把 key 撑不起，但同模型的另一条渠道可能撑得起，
// 所以两者同时出现，语义是升级顺序（先换 key，试完再换渠道）。
// 限流只换渠道不停用：渠道仍存活，只是暂时忙。
var policyTable = map[Class]Policy{
	ClassOther:   {Name: classNameOther, Actions: ActionSurface},
	ClassRequest: {Name: classNameRequest, Actions: ActionSurface},
	ClassAuth: {
		Name:    classNameAuth,
		Actions: ActionSuspendAccount | ActionRetryNextAccount,
	},
	ClassQuota: {
		Name:       classNameQuota,
		Actions:    ActionSuspendAccount | ActionRetryNextAccount | ActionRetryNextRoute,
		SuspendFor: quotaSuspend,
	},
	ClassCredit: {
		Name:       classNameCredit,
		Actions:    ActionSuspendAccount | ActionRetryNextAccount | ActionRetryNextRoute,
		SuspendFor: creditSuspend,
	},
	ClassRateLimit: {Name: classNameRateLimit, Actions: ActionRetryNextRoute},
	// 上下文超限与模型不可用换 key 与换渠道都没用：报文本身或请求的模型不对。
	ClassContextLength:    {Name: classNameContextLength, Actions: ActionSurface},
	ClassModelUnavailable: {Name: classNameModelUnavailable, Actions: ActionSurface},
	ClassUpstream: {
		Name:    classNameUpstream,
		Actions: ActionRetryNextRoute | ActionCountBreaker,
	},
	ClassTimeout: {
		Name:    classNameTimeout,
		Actions: ActionRetryNextRoute | ActionCountBreaker,
	},
}

// PolicyOf 取一个类别的处置；未登记的类别按 ClassOther 处理。
func PolicyOf(class Class) Policy {
	if policy, ok := policyTable[class]; ok {
		return policy
	}
	return policyTable[ClassOther]
}

// ActionsFor 返回该类别对应的动作集合。
func ActionsFor(class Class) Action { return PolicyOf(class).Actions }

// ClassName 返回类别的分类名。
func ClassName(class Class) string { return PolicyOf(class).Name }
