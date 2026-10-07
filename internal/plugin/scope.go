package plugin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Calcium-Ion/moejs"

	"github.com/sumwai/tokenmp/internal/domain"
)

// 本文件实现中间件作用域：模块声明它只对哪些模型 / 方言 / 厂商生效，由宿主在 Go 侧判定，
// 越界则完全不进入 JS 运行时。
//
// 为什么判定必须在宿主侧：钩子（尤其 onEvent）在流式热路径上逐分片调用，把判定写进 JS 里
// 等于「这次边界跨越已经发生了」。声明式作用域省掉的是整次调用，不是一个分支。
//
// 作用域只吃名字，不吃主键：渠道主键是环境事实（重建库就换号），同一份插件在不同库里
// 会作用到不同渠道，文件里也看不出数字是谁。可用的名字是模型名、方言取值与厂商标签。

// scopeSpec 是一个中间件声明的作用域。零值表示不限制任何轴。
//
// 三个轴都是集合：缺失或为空的轴不参与判定。多轴取与、同轴取或。
type scopeSpec struct {
	models    map[string]bool
	protocols map[string]bool
	vendors   map[string]bool
}

// allows 报告本次事实是否落在作用域内。
//
// vendorKnown 为假时（选路之前）厂商轴不参与判定：那一刻还不知道会走哪条渠道，
// 不能因为声明了 vendors 就把请求体改写一并跳过。
func (s scopeSpec) allows(model, protocol, vendor string, vendorKnown bool) bool {
	if !matchesAxis(s.models, model) {
		return false
	}
	if !matchesAxis(s.protocols, protocol) {
		return false
	}
	if vendorKnown && !matchesAxis(s.vendors, vendor) {
		return false
	}
	return true
}

// matchesAxis 判定单个轴：轴为空表示不限制该轴，否则取值须在集合内。
func matchesAxis(axis map[string]bool, value string) bool {
	if len(axis) == 0 {
		return true
	}
	return axis[strings.TrimSpace(value)]
}

// declared 返回声明过的取值，按轴分组。
//
// 只用于启动期的漂移检查：声明了却在当前配置下一个都匹配不上的名字，应当立刻报出来，
// 否则运营改了厂商标签之后插件会静默失效。
func (s scopeSpec) declared() map[string][]string {
	out := map[string][]string{}
	for axis, set := range map[string]map[string]bool{
		AxisModels:    s.models,
		AxisProtocols: s.protocols,
		AxisVendors:   s.vendors,
	} {
		if len(set) == 0 {
			continue
		}
		values := make([]string, 0, len(set))
		for value := range set {
			values = append(values, value)
		}
		// 排序只为让告警文本稳定，便于比对与去重。
		sort.Strings(values)
		out[axis] = values
	}
	return out
}

// readScope 读出模块导出的 scope 作用域。
//
// 未导出是合法情形（不限制），第二个返回值为真；形状非法时返回非 nil 错误，
// 由装配方按「不限制」处理并留痕 —— 作用域写坏不该让插件静默失效，
// 而静默失效正是作用域要避免的失败形态。
func readScope(runtime *moejs.Runtime) (scopeSpec, error) {
	value, ok := runtime.Export(exportScope)
	if !ok {
		return scopeSpec{}, nil
	}
	raw, err := runtime.ToGo(value)
	if err != nil {
		return scopeSpec{}, fmt.Errorf("scope 取值失败：%w", err)
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return scopeSpec{}, fmt.Errorf("scope 必须是对象")
	}
	if unknown := unknownScopeAxes(fields); len(unknown) > 0 {
		// 轴名拼错原先会被忽略，而「不限制」是该包对无法解析的作用域的处置，
		// 于是拼错轴名的后果是**范围变大** —— 与作用域要收窄的初衷正好相反。
		return scopeSpec{}, fmt.Errorf("scope 含无法识别的轴：%s；可用轴为 %s、%s、%s",
			strings.Join(unknown, "、"), AxisModels, AxisProtocols, AxisVendors)
	}
	spec := scopeSpec{}
	if spec.models, err = readAxis(fields, AxisModels); err != nil {
		return scopeSpec{}, err
	}
	if spec.protocols, err = readAxis(fields, AxisProtocols); err != nil {
		return scopeSpec{}, err
	}
	if spec.vendors, err = readAxis(fields, AxisVendors); err != nil {
		return scopeSpec{}, err
	}
	return spec, nil
}

// unknownScopeAxes 返回无法识别的轴名，按字典序排列。
func unknownScopeAxes(fields map[string]any) []string {
	known := map[string]bool{AxisModels: true, AxisProtocols: true, AxisVendors: true}
	unknown := make([]string, 0, len(fields))
	for name := range fields {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// readAxis 读出一个轴；缺失或为 null 表示不限制，非字符串数组属形状非法。
func readAxis(fields map[string]any, axis string) (map[string]bool, error) {
	raw, ok := fields[axis]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("scope.%s 必须是数组", axis)
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("scope.%s 的元素必须是字符串", axis)
		}
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	return set, nil
}

// scopeFacts 取出本次调用可用于判定的事实：模型与方言来自请求，厂商来自选路。
//
// 选路之前厂商未知，此时第二个返回值为假，作用域里的厂商轴不参与判定。
func scopeFacts(ctx context.Context, req *domain.Request) (model, protocol, vendor string, vendorKnown bool) {
	if req != nil {
		model, protocol = req.Model, string(req.Protocol)
	}
	facts, ok := domain.RouteFactsFromContext(ctx)
	if !ok {
		return model, protocol, "", false
	}
	return model, protocol, facts.Vendor, true
}
