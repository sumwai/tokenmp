package admin

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// 本文件收敛管理面的公共输入校验。
//
// 解析层已经做过一遍校验（缺参、枚举），服务层再校验一次是有意的：服务层可被
// 其它调用方直接使用，不能假设调用方一定来自 CLI；同时它让 fake store 能断言
// 「非法输入没有触达存储层」。
//
// 校验失败带 ErrInvalidInput 标记：调用方据此把「配错了」报成用法错误 / 400，
// 而不是与真正的服务端故障混进同一个分支。错误文案原样面向操作者，不带标记本身。

// ErrInvalidInput 标记调用方输入不合法。
var ErrInvalidInput = errors.New("admin: 输入不合法")

// invalidInputError 是输入校验失败的错误；消息就是给操作者看的那句话。
type invalidInputError struct{ message string }

// Error 返回原样文案。
func (e *invalidInputError) Error() string { return e.message }

// Is 让 errors.Is(err, ErrInvalidInput) 命中本类型。
func (e *invalidInputError) Is(target error) bool { return target == ErrInvalidInput }

// invalidf 构造一条带标记的输入校验错误。
func invalidf(format string, args ...any) error {
	return &invalidInputError{message: fmt.Sprintf(format, args...)}
}

// requireString 校验必填字符串非空。
func requireString(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalidf("admin: %s 不能为空", field)
	}
	return nil
}

// requireID 校验必填主键非零。
func requireID(field string, id uint64) error {
	if id == 0 {
		return invalidf("admin: %s 不能为 0", field)
	}
	return nil
}

// requireDecimal 校验字符串是可解析的定点小数。
//
// 金额与数量用字符串承载（同 store 的口径），进入派生计算前必须先确认可解析，
// 否则错误会推迟到乘法或写库时才暴露。
func requireDecimal(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalidf("admin: %s 不能为空", field)
	}
	if _, err := parseDecimal(value); err != nil {
		return invalidf("admin: %s 不是合法数字 %q", field, value)
	}
	return nil
}

// errNotPositive 是拆分数量非正时拼进文案的片段。
const errNotPositive = "数量必须为正数"

// ErrNotFound 标记目标行不存在。
//
// 与 ErrInvalidInput 分开：参数写错要改字段，目标不存在要重新取清单，两者的下一步不同，
// 调用方靠标记翻成 400 还是 404。
var ErrNotFound = errors.New("admin: 目标不存在")

// notFoundError 是目标不存在的错误；消息就是给操作者看的那句话。
type notFoundError struct{ message string }

// Error 返回原样文案。
func (e *notFoundError) Error() string { return e.message }

// Is 让 errors.Is(err, ErrNotFound) 命中本类型。
func (e *notFoundError) Is(target error) bool { return target == ErrNotFound }

// notFoundf 构造一条带标记的「不存在」错误。
func notFoundf(format string, args ...any) error {
	return &notFoundError{message: fmt.Sprintf(format, args...)}
}

// requireRow 把一次按主键读取的结果折成两类错误：无匹配时用 missing 报错，其余原样返回。
//
// 引用校验传 ErrInvalidInput 类的错误（请求体里的引用不存在属参数错误），
// 动作目标校验传 ErrNotFound 类的错误（目标不存在属 404）。
func requireRow(err error, missing error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return missing
	}
	return err
}
