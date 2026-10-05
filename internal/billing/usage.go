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
//	cache_write_tokens  → cache_write_token（上游只报单一缓存写数量的形态）
//	reasoning_tokens    → reasoning_token（output_token 的子项，仅事实记录）
//
// 只返回非零分量：零值分量写进 JSON 不携带信息，还会让流水行的形状随上游是否给出
// 子项漂移。空集合序列化为 {}，仍是一行合法的用量事实。
//
// 第二个返回值是无法映射的非零分量名（当前只有 server_tool_uses）：它不落库，
// 由调用方记日志后丢弃。之所以既不报错也不落库：丢掉一次已经发生的转发的整行流水，
// 比丢一个没有计价口径的分量代价大得多。
func UsageFromDomain(u domain.Usage) (map[Metric]int, []string) {
	metrics := make(map[Metric]int)
	add := func(metric Metric, qty int) {
		if qty != 0 {
			metrics[metric] = qty
		}
	}
	add(MetricInputToken, u.InputTokens)
	add(MetricOutputToken, u.OutputTokens)
	add(MetricCacheReadToken, u.CacheReadTokens)
	add(MetricCacheWriteToken, u.CacheWriteTokens)
	add(MetricReasoningToken, u.ReasoningTokens)

	var dropped []string
	if u.ServerToolUses != 0 {
		dropped = append(dropped, "server_tool_uses")
	}
	return metrics, dropped
}
