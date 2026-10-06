package domain

import "context"

// EventResult 是一次逐事件钩子的结论。
//
// 三个字段表达同一件事的三个结论，消费方不必再自行推导：
// 丢弃、改写、未改动。未改动单独标出，是为了让同协议透传路径仍能写出上游原始帧，
// 不给未改动的帧付出重新编码的代价。
type EventResult struct {
	// Chunk 是改写后的分片；Drop 为 true 时无意义。
	Chunk Chunk
	// Drop 表示中间件丢弃该分片。
	Drop bool
	// Unchanged 表示没有任何中间件改写该分片。
	Unchanged bool
}

// StreamMiddleware 在流式分片写回客户端之前逐分片介入。
//
// 消费者是流水线的流式下沉目标；生产者是装配层注入的中间件插件层。
// 只对内容分片（文本、工具调用、推理增量）调用：用量、结束原因与流结束承担
// 计费与控制语义，不属逐事件改写的范围，不由本端口改写。
//
// 实现必须自行处理超时与错误：失败时返回未改动结论，使转发按上游原样继续。
// 本端口是转发的旁路，返回值不得携带错误，消费方也无处上报失败。
type StreamMiddleware interface {
	// OnEvent 返回对单个流式分片的处置结论。
	OnEvent(ctx context.Context, req *Request, chunk Chunk) EventResult
}

// ResponseMiddleware 在非流式响应体写回客户端之前介入。
//
// 与 StreamMiddleware 同一机制：生产者是装配层注入的中间件插件层，
// 消费者是流水线的非流式写出分支。
//
// 实现必须自行处理超时与错误：失败时返回入参字节，使转发按上游原样继续。
type ResponseMiddleware interface {
	// OnResponse 返回要写回客户端的响应体字节。
	OnResponse(ctx context.Context, req *Request, body []byte) []byte
}
