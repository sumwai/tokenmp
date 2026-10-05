package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math/rand"
	"strconv"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件实现选路的两件事：同优先级候选的加权随机首选，以及同协议段与跨协议段的拼链。
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

// routeChain 把两级候选拼成本次请求的唯一回退链：同协议候选成段在前，跨协议候选补段在后。
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
func routeChain(client domain.Protocol, sameProtocol, crossProtocol []store.RouteCandidate, intN func(int) int) []domain.Route {
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
		ChannelID:            candidate.ChannelID,
		UpstreamID:           strconv.FormatUint(candidate.ChannelID, 10),
		Protocol:             protocol,
		UpstreamModel:        candidate.UpstreamModel,
		BaseURL:              endpointURL(candidate.BaseURL, protocol),
		CredentialRef:        candidate.CredGroup,
		RequestOverrides:     sanitizeRequestOverrides(candidate),
		RateLimitQPS:         candidate.RateLimitQPS,
		RateLimitConcurrency: candidate.RateLimitConcurrency,
	}
}
