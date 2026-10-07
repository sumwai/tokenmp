package failure

import (
	"errors"

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
}

// NewError 构造一个带分类的统一错误。detail 只进服务端日志，不进客户端错误体。
func NewError(code domain.Code, message, detail string, class Class) *Error {
	err := domain.NewError(code, message)
	if detail != "" {
		err = err.WithDetail(detail)
	}
	return &Error{base: err, class: class}
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

// Class 返回本次失败的类别。
func (e *Error) Class() Class { return e.class }

// Actions 返回本次失败的动作集合。
func (e *Error) Actions() Action { return ActionsFor(e.class) }

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
