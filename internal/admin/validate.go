package admin

import (
	"errors"
	"fmt"
	"strings"
)

// 本文件收敛管理面的公共输入校验。
//
// 解析层已经做过一遍校验（缺参、枚举），服务层再校验一次是有意的：服务层可被
// 其它调用方直接使用，不能假设调用方一定来自 CLI；同时它让 fake store 能断言
// 「非法输入没有触达存储层」。

// requireString 校验必填字符串非空。
func requireString(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("admin: %s 不能为空", field)
	}
	return nil
}

// requireID 校验必填主键非零。
func requireID(field string, id uint64) error {
	if id == 0 {
		return fmt.Errorf("admin: %s 不能为 0", field)
	}
	return nil
}

// requireDecimal 校验字符串是可解析的定点小数。
//
// 金额与数量用字符串承载（同 store 的口径），进入派生计算前必须先确认可解析，
// 否则错误会推迟到乘法或写库时才暴露。
func requireDecimal(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("admin: %s 不能为空", field)
	}
	if _, err := parseDecimal(value); err != nil {
		return fmt.Errorf("admin: %s 不是合法数字 %q", field, value)
	}
	return nil
}

// errNotPositive 报告拆分为零或负数。
var errNotPositive = errors.New("admin: 数量必须为正数")
