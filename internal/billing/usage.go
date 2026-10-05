package billing

import "github.com/sumwai/tokenmp/internal/domain"

// 本文件把内部统一用量转换为 billing_usage.usage 的键值分量。
//
// 转换放在 billing 边界而不是 domain：domain 是最底层、不得导入任何其它 internal 包，
// 而分量名（Metric 白名单）定义在本包，由本包承担映射才能保证「白名单是唯一出处」。

// UsageFromDomain 把一次调用的内部统一用量转换为按白名单表示的计费分量。
//
// 映射口径：
//
//	input_tokens        → input_token
//	output_tokens       → output_token
//	cache_read_tokens   → cache_read_token
//	cache_write_tokens  → cache_write_token（上游只报单一缓存写数量的不分档形态）
//	cache_write_5m      → cache_write_5m（上游能按 TTL 分档时的 5 分钟档）
//	cache_write_1h      → cache_write_1h（上游能按 TTL 分档时的 1 小时档）
//	reasoning_tokens    → reasoning_token（output_token 的子项，仅事实记录）
//
// 缓存写的两套口径互斥：分档字段任一非零时只落两档分量、不分档分量不落；分档全零而
// 不分档非零时落 cache_write_token。两套同时非零属异常，以分档为准并在 conflicts 里
// 报告，由调用方记日志。之所以按分档优先：分档承载更细的计价口径，丢了它就没有定价依据。
//
// 只返回非零分量：零值分量写进 JSON 不携带信息，还会让流水行的形状随上游是否给出
// 子项漂移。空集合序列化为 {}，仍是一行合法的用量事实。
//
// 第二个返回值 unmapped 是无法映射的非零分量名（当前只有 server_tool_uses）：它不落库，
// 由调用方记日志后丢弃。之所以既不报错也不落库：丢掉一次已经发生的转发的整行流水，
// 比丢一个没有计价口径的分量代价大得多。
//
// 第三个返回值 conflicts 是互斥口径同时非零的描述，由调用方记日志；映射已按既定优先级取舍。
func UsageFromDomain(u domain.Usage) (metrics map[Metric]int, unmapped []string, conflicts []string) {
	metrics = make(map[Metric]int)
	add := func(metric Metric, qty int) {
		if qty != 0 {
			metrics[metric] = qty
		}
	}
	add(MetricInputToken, u.InputTokens)
	add(MetricOutputToken, u.OutputTokens)
	add(MetricCacheReadToken, u.CacheReadTokens)
	addCacheWrite(metrics, u, &conflicts)
	add(MetricReasoningToken, u.ReasoningTokens)

	if u.ServerToolUses != 0 {
		unmapped = append(unmapped, "server_tool_uses")
	}
	return metrics, unmapped, conflicts
}

// addCacheWrite 按互斥规则写入缓存写分量，并把异常并存的情况追加到 conflicts。
//
// 分档字段任一非零即为分档口径：两档各按非零写入，不分档字段即使非零也不落并记一条冲突。
// 分档全零时落不分档分量。
func addCacheWrite(metrics map[Metric]int, u domain.Usage, conflicts *[]string) {
	tiered := u.CacheWrite5mTokens != 0 || u.CacheWrite1hTokens != 0
	if !tiered {
		if u.CacheWriteTokens != 0 {
			metrics[MetricCacheWriteToken] = u.CacheWriteTokens
		}
		return
	}
	if u.CacheWriteTokens != 0 {
		*conflicts = append(*conflicts, "cache_write_token 与分档字段同时非零，已按分档为准")
	}
	if u.CacheWrite5mTokens != 0 {
		metrics[MetricCacheWrite5m] = u.CacheWrite5mTokens
	}
	if u.CacheWrite1hTokens != 0 {
		metrics[MetricCacheWrite1h] = u.CacheWrite1hTokens
	}
}
