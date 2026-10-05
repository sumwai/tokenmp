package main

import (
	"io"
	"log/slog"

	"github.com/sumwai/tokenmp/internal/transport"
)

// accessLogger 是 transport.AccessLogger 的结构化实现：每请求一行 JSON 写到指定写出目标。
//
// 落点在装配层：入口层只产出与协议无关的访问记录，日志格式与去向在这里决定。
// 只输出记录里显式列出的字段，凭据、API key 与 Authorization 请求头从未进入记录，
// 因此也不会出现在日志里。
type accessLogger struct {
	logger *slog.Logger
}

// 编译期断言：本实现满足入口层的访问日志接口。
var _ transport.AccessLogger = (*accessLogger)(nil)

// newJSONLogger 构造写到 w 的 JSON 行式结构化日志；访问日志与凭据轮换日志共用同一形态。
func newJSONLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil))
}

// newAccessLogger 构造写到 w 的 JSON 行式访问日志实现。
func newAccessLogger(w io.Writer) *accessLogger {
	return &accessLogger{logger: newJSONLogger(w)}
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
		"http_status", record.HTTPStatus,
		"duration_ms", record.DurationMS,
	)
}
