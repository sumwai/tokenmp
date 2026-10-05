package observability

import (
	"context"
	"log/slog"

	"github.com/sumwai/tokenmp/internal/domain"
)

// attemptObserver 把每次上游尝试写成一行 JSON，实现 domain.Observer。
//
// 与访问日志的分工：访问日志是请求级，一次请求一行，只保留最终履约（或最终失败）的那次尝试；
// 本实现是尝试级，重试、渠道回退与渠道内凭据轮换各占一行。两者用同一个 request_id 关联，
// 同一 request_id 下的多行合起来就是这次转发的时间线。
//
// 字段命名与脱敏口径对齐访问日志：渠道 id 与上游状态码取同名键。凭据、api key 与
// Authorization 头从未进入 AttemptRecord，因此也不会出现在这里。
type attemptObserver struct {
	logger *slog.Logger
}

// 编译期断言：本实现满足流水线的观测端口。
var _ domain.Observer = (*attemptObserver)(nil)

// NewAttemptObserver 构造尝试级日志实现；logger 为 nil 时等价于不记录。
func NewAttemptObserver(logger *slog.Logger) *attemptObserver {
	return &attemptObserver{logger: logger}
}

// RecordAttempt 写一条上游尝试日志，恒返回 nil。
//
// 观测是转发的旁路：实现内部的任何异常都不该变成转发结果的一部分，因此这里不返回错误，
// 调用方也无需分支处理。后续要接 Prometheus 一类的指标输出时，应新增一个实现
// domain.Observer 的聚合器与日志实现并行装配，而不是把指标状态塞进本类型；
// 端口的失败语义（记录失败不影响转发）已为这种并行实现留好空间。
//
// 级别按结果分档：成功 info、失败 warn、客户端取消 debug。取消记 debug 是有意的：
// 客户端主动断开是长流场景的常见结局，按 warn 记会把真正的故障淹没。
func (o *attemptObserver) RecordAttempt(ctx context.Context, rec domain.AttemptRecord) error {
	if o == nil || o.logger == nil {
		return nil
	}
	retried := rec.Attempt > 1
	args := []any{
		"request_id", rec.RequestID,
		"attempt", rec.Attempt,
		"retry", retried,
		"channel_id", rec.ChannelID,
		"channel_switched", rec.ChannelSwitched,
		// 同渠道内的重试只可能来自凭据轮换（换渠道会同时把 channel_switched 置真），
		// 故本取值由前两项推出，无需另带凭据标识——凭据明文不得进日志。
		"credential_switched", retried && !rec.ChannelSwitched,
		"protocol", string(rec.ClientProtocol),
		"upstream_protocol", string(rec.UpstreamProtocol),
		// 两侧协议字段已蕴含结论，显式给出是为排障时不必要求读者做推导。
		// 取值由生产端写入 AttemptRecord.CrossProtocol，与访问日志同名同义。
		"cross_protocol", rec.CrossProtocol,
		// 两个模型名都按原文回显：模型名是客户端可见标识，从不被当作地址处理，
		// 因此这里不过抹除出口。
		"requested_model", rec.RequestedModel,
		"upstream_model", rec.UpstreamModel,
		"outcome", string(rec.Outcome),
		"upstream_status", rec.UpstreamStatus,
		// duration_ms 是本次上游调用的耗时（起止时刻在上游调用边界采集），不含向客户端写出的耗时。
		"duration_ms", rec.EndedAt.Sub(rec.StartedAt).Milliseconds(),
	}
	args = append(args, usageLogArgs(rec.Usage)...)
	// 只在等满 1ms 时出字段：未配置限流与令牌即时可用是绝大多数情形，
	// 恒零字段只会淹没真正命中限流的那些尝试。
	if waitedMS := rec.RateLimitWait.Milliseconds(); waitedMS > 0 {
		args = append(args, "rate_limit_wait_ms", waitedMS)
	}
	// 成功尝试没有错误码与排障细节，空值不出字段，避免日志里全是无意义的空串。
	if rec.ErrorCode != "" {
		args = append(args, "error_code", rec.ErrorCode)
	}
	if rec.ErrorDetail != "" {
		args = append(args, "error_detail", rec.ErrorDetail)
	}
	if len(rec.RewrittenParts) > 0 {
		args = append(args, "rewritten_parts", rec.RewrittenParts)
	}
	o.logger.Log(ctx, attemptLogLevel(rec.Outcome), "upstream_attempt", args...)
	return nil
}

// usageLogArgs 把本次尝试取得的用量展开为日志字段。
//
// 未取得用量时只给 usage_source=unknown：此时各计数都是零值填充，照原样打出来会被误读成
// 「上游确实报了 0」，而真实情况是没有可用计数。已取得时连零值一起打，因为「上游报的就是 0」
// 同样是真实事实，不该被省略。
func usageLogArgs(usage domain.Usage) []any {
	if !usage.Known() {
		return []any{"usage_source", "unknown"}
	}
	args := []any{
		"usage_source", string(usage.Source),
		"input_tokens", usage.InputTokens,
		"output_tokens", usage.OutputTokens,
		"cache_read_tokens", usage.CacheReadTokens,
		"cache_write_tokens", usage.CacheWriteTokens,
		"cache_write_5m_tokens", usage.CacheWrite5mTokens,
		"cache_write_1h_tokens", usage.CacheWrite1hTokens,
		"reasoning_tokens", usage.ReasoningTokens,
	}
	// 服务端工具执行次数是独立于 token 的计费维度；未发生时不打该字段，
	// 避免每条日志都挂一个恒零字段。
	if usage.ServerToolUses > 0 {
		args = append(args, "server_tool_uses", usage.ServerToolUses)
	}
	return args
}

// attemptLogLevel 按尝试结果决定日志级别。
func attemptLogLevel(outcome domain.AttemptOutcome) slog.Level {
	switch outcome {
	case domain.AttemptOK, domain.AttemptSkipped:
		// 熔断跳过是网关主动不再打该渠道，属预期行为，按 info 记。
		return slog.LevelInfo
	case domain.AttemptCancelled:
		return slog.LevelDebug
	default:
		return slog.LevelWarn
	}
}
