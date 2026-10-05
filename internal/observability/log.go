// Package observability 实现转发链路的观测结果落点：请求级访问日志与尝试级尝试日志。
//
// 从 cmd 下沉到本包：入口层只产出与协议无关的访问记录、流水线只产出尝试记录，
// 字段拼装与日志格式属于观测职责，放在这里才能被直接单测，也避免命令包继续增长。
// 两个实现都只输出记录里显式列出的字段，凭据、API key 与 Authorization 头从未进入
// 记录，因此也不会出现在日志里。
package observability

import (
	"io"
	"log/slog"

	"github.com/sumwai/tokenmp/internal/transport"
)

// accessLogger 是 transport.AccessLogger 的结构化实现：每请求一行 JSON 写到指定写出目标。
//
// 落点在装配层：入口层只产出与协议无关的访问记录，日志格式与去向在这里决定。
type accessLogger struct {
	logger *slog.Logger
}

// 编译期断言：本实现满足入口层的访问日志接口。
var _ transport.AccessLogger = (*accessLogger)(nil)

// NewJSONLogger 构造写到 w 的 JSON 行式结构化日志；访问日志与凭据轮换日志共用同一形态。
func NewJSONLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil))
}

// NewAccessLogger 构造写到 w 的 JSON 行式访问日志实现。
func NewAccessLogger(w io.Writer) *accessLogger {
	return &accessLogger{logger: NewJSONLogger(w)}
}

// LogAccess 写一条请求日志。字段取访问记录里与协议无关的那些，
// 不拼接请求头或请求体，避免凭据从这条路径泄露。
func (l *accessLogger) LogAccess(record transport.AccessRecord) {
	if l == nil || l.logger == nil {
		return
	}
	l.logger.Info("request",
		"request_id", record.RequestID,
		"protocol", string(record.Protocol),
		"model", record.Model,
		"channel_id", record.ChannelID,
		"upstream_status", record.UpstreamStatus,
		// 标记本次请求最终是否走了跨协议重建：同协议缺位时的降级是可用性行为，
		// 在请求级日志里明示，不必从渠道 id 反查渠道方言。
		"cross_protocol", record.CrossProtocol,
		"http_status", record.HTTPStatus,
		"duration_ms", record.DurationMS,
	)
}
