package failure

import (
	"errors"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

// Error 是带上游失败分类的统一错误。
//
// 它同时是「诊断结果」与「处置建议」的载体：消费方只读 Actions()，不必知道分类是怎么来的，
// 也就不必各自维护一份「什么错误该重试」的判据。
//
// 内嵌 *domain.Error 看似更自然，但做不到：内嵌字段名就是 Error，会把晋升上来的
// Error() 方法挡住，*Error 于是不满足 error 接口。所以用具名字段 + 显式转发。
type Error struct {
	base  *domain.Error
	class Class
	// actions 与 override 实现对类别处置的覆盖；override 为假时按 class 取策略表值。
	//
	// 不能拿 actions 的零值当「未覆盖」：覆盖后恰好为空集（如认证类失败被厂商声明
	// 「凭据没问题」而摘掉整组凭据动作）是合法取值，它与「未覆盖」必须分得开。
	actions  Action
	override bool
}

// NewError 构造一个带分类的统一错误，处置取自类别的策略表。
// detail 只进服务端日志，不进客户端错误体。
func NewError(code domain.Code, message, detail string, class Class) *Error {
	err := domain.NewError(code, message)
	if detail != "" {
		err = err.WithDetail(detail)
	}
	return &Error{base: err, class: class}
}

// NewErrorWithActions 构造一个带分类且显式指定处置的统一错误。
//
// 用于厂商声明与类别推断相左的场合：类别仍记录事实，处置改按声明给出
// （如渠道信标头说「这个状态码不代表凭据有问题」，则摘掉整组凭据动作）。
func NewErrorWithActions(code domain.Code, message, detail string, class Class, actions Action) *Error {
	err := NewError(code, message, detail, class)
	err.actions = actions
	err.override = true
	return err
}

// Error 实现 error 接口，转发统一错误的面向排障文案。
func (e *Error) Error() string { return e.base.Error() }

// Unwrap 返回内嵌的统一错误，使 errors.As 能继续向下匹配。
//
// 必需而不是多余：errors.As 按具体类型匹配，没有它则
// domain.AsError / HTTPStatus / Retryable 一律拿不到东西，面向客户端的错误体与状态码全线失灵。
func (e *Error) Unwrap() error { return e.base }

// Domain 返回内嵌的统一错误，供需要直接读取其字段的调用方使用。
func (e *Error) Domain() *domain.Error { return e.base }

// WithCause 记录导致本次失败的下层错误，供日志与 errors.Is 追根。
func (e *Error) WithCause(cause error) *Error {
	e.base = e.base.WithCause(cause)
	return e
}

// Class 返回本次失败的类别。
func (e *Error) Class() Class { return e.class }

// Actions 返回本次失败的处置集合：显式指定过则用之，否则取自类别的策略表。
func (e *Error) Actions() Action {
	if e.override {
		return e.actions
	}
	return ActionsFor(e.class)
}

// SuspendFor 返回错误建议的凭据停用时长；不该停用或未带分类时返回 0。
//
// 由动作把关、由类别给数值：渠道信标头可以摘掉停用动作（此时类别仍是 quota），
// 那么停用就该被取消；两者分开才能表达「认得出是额度问题，但这把 key 不必停」。
func SuspendFor(err error) time.Duration {
	if !ActionsOf(err).Has(ActionSuspendAccount) {
		return 0
	}
	return PolicyOf(ClassOf(err)).SuspendFor
}

// CodeForClass 返回一个类别在面向客户端错误体里的错误码。
//
// 分类与错误码是两根轴：分类驱动处置（换凭据、换渠道、停用），错误码驱动对外的状态码
// 与错误体形态（504 / 502 / 503）。需要把两边对上的地方可能会分两次做，
// 但能对上是因为类别本身分得够细（见 ClassTimeout），不是靠在调用方另行判状态码。
func CodeForClass(class Class) domain.Code {
	switch class {
	case ClassRateLimit:
		return domain.CodeUpstreamRateLimited
	case ClassTimeout:
		return domain.CodeUpstreamTimeout
	case ClassUpstream:
		return domain.CodeUpstreamUnavailable
	default:
		// 其余上游侧失败都归「上游拒绝请求」：换渠道重试仍可能失败，
		// 是否重试由动作集合决定，不再由错误码决定。
		return domain.CodeUpstreamRejected
	}
}

// ActionCarrier 是错误可选实现的能力：声明本次失败的处置集合。
//
// 声明为消费者侧契约（同 pipeline 的 retryAfterHint）：生产者是拿到上游响应的一方，
// 本包只提供结构匹配的读取入口。
type ActionCarrier interface {
	Actions() Action
}

// ActionsOf 从错误里读出动作集合；未带分类的错误返回零值。
//
// 零值表示「无意见」，不是一个动作。消费方要各自给出默认：
//
//	决定下一步的用 NextStep()，它对零值回答 ActionSurface（回客户端永远可执行）；
//	只问「要不要做某事」的直接 Has(...)，零值下为假。
//
// 刻意不在这里给一个笼统的默认集合：那会把「没意见」与「明确不重试」混为一谈，
// 而后者是需要被断言的。
func ActionsOf(err error) Action {
	var carrier ActionCarrier
	if !errors.As(err, &carrier) {
		return 0
	}
	return carrier.Actions()
}

// ClassCarrier 是错误可选实现的能力：声明本次失败的诊断类别。
type ClassCarrier interface {
	Class() Class
}

// ClassOf 从错误里读出类别；未带分类时返回 ClassOther。只用于观测与排障，不参与控制流。
func ClassOf(err error) Class {
	var carrier ClassCarrier
	if !errors.As(err, &carrier) {
		return ClassOther
	}
	return carrier.Class()
}

// ClassNameOf 从错误里读出分类名，供写日志；未带分类时返回 ClassOther 的名字。
func ClassNameOf(err error) string { return ClassName(ClassOf(err)) }
