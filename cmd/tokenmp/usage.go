package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// defaultUsageWriteTimeout 是未配置写用量流水的耗时上限时的默认值。
//
// 写流水发生在请求终态之后：客户端断开会让请求上下文被取消，而这次转发已经在
// 上游产生过用量。因此先摘掉取消信号再写，否则断连就把流水丢掉；
// 本超时用来兜住「数据库挂死导致协程悬空」。
const defaultUsageWriteTimeout = 5 * time.Second

// 本文件把流水线交出的用量事实写成 billing_usage 流水。
//
// 归属（商家、账户）取自鉴权上下文，不来自请求体：用量必须记在密钥所属的账户上，
// 否则一个账户的请求会记到另一个账户的流水里。流水线只交出事由渠道、履约模型与用量。

// logFunc 写一条结构化日志，签名与 slog 的包级函数一致。
type logFunc func(msg string, args ...any)

// storeUsageRecorder 是 domain.UsageRecorder 的存储实现。
type storeUsageRecorder struct {
	store gatewayStore
	logf  logFunc
	// writeTimeout 是单次写用量流水的耗时上限；非正时取 defaultUsageWriteTimeout。
	writeTimeout time.Duration
}

// 编译期断言：装配层的记账实现满足流水线的用量记录接口。
var _ domain.UsageRecorder = (*storeUsageRecorder)(nil)

// newUsageRecorder 构造记账实现；writeTimeout 非正时取默认值，logf 为 nil 时用 slog 的默认 logger。
func newUsageRecorder(st gatewayStore, writeTimeout time.Duration, logf logFunc) *storeUsageRecorder {
	if logf == nil {
		logf = slog.Warn
	}
	if writeTimeout <= 0 {
		writeTimeout = defaultUsageWriteTimeout
	}
	return &storeUsageRecorder{store: st, logf: logf, writeTimeout: writeTimeout}
}

// RecordUsage 把一次转发的用量写进 billing_usage。
//
// 没有对应 Metric 的分量（当前只有服务端工具调用次数）记日志后丢弃：丢一个没有计价
// 口径的分量，代价远小于丢掉一整行已经发生的转发流水。
// 落库失败返回错误，同时记日志；流水线忽略返回值，不影响已写给客户端的响应。
func (r *storeUsageRecorder) RecordUsage(ctx context.Context, rec domain.UsageRecord) error {
	id, ok := identityFromContext(ctx)
	if !ok {
		// 未鉴权不应走到这里；按无归属处理而不写一行 account_id=0 的脏流水。
		return domain.NewError(domain.CodeInternal, "缺少鉴权上下文，无法归属用量")
	}
	metrics, dropped := billing.UsageFromDomain(rec.Usage)
	for _, name := range dropped {
		r.logf("用量分量没有对应的计费指标，已丢弃",
			"component", name, "request_id", rec.RequestID)
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.writeTimeout)
	defer cancel()
	if _, err := r.store.InsertUsage(writeCtx, store.UsageRow{
		MerchantID: id.merchantID,
		AccountID:  id.accountID,
		ChannelID:  rec.ChannelID,
		Model:      rec.Model,
		Usage:      metrics,
	}); err != nil {
		r.logf("写入 billing_usage 失败", "request_id", rec.RequestID, "error", err)
		return err
	}
	return nil
}
