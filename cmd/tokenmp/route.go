package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math/rand"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件实现同优先级候选的加权随机首选。
//
// 随机源以 intN 注入：默认为 math/rand，测试注入确定性序列后即可断言「选中了哪一条」，
// 而不必对分布做统计性断言。

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
