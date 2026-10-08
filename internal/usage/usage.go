// Package usage 把流水线交出的用量事实结算并写成 billing_usage 流水。
//
// 从 cmd 下沉到本包：归属补全、request 分量补写与结算失败回退属记账编排，
// 留在命令包只能经 HTTP 装配测试覆盖。
//
// 为什么独立成包而不是并入 internal/settlement：结算器是纯计费规则，不感知鉴权；
// 本包需要从请求上下文取归属（商家、账户、API key），并入 settlement 会让计费规则
// 反向依赖鉴权上下文，因此单列一个记账职责包。
//
// 归属取自鉴权上下文，不来自请求体：用量必须记在密钥所属的账户与 key 上，否则一个
// 账户的请求会记到另一个账户或另一把 key 的流水里。流水线只交出事由渠道、履约模型
// 与用量，它不感知鉴权，因此 key 维度在记账这一层补上。
//
// 结算与落库是同一件事：结算器在事务里写结算字段并扣账本，结算失败时退回占位口径
// 只落用量。这样「流水已写」与「已扣费」不会出现两套时序。
package usage

import (
	"context"
	"log/slog"
	"time"

	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
)

// defaultWriteTimeout 是未配置写用量流水的耗时上限时的默认值。
//
// 写流水发生在请求终态之后：客户端断开会让请求上下文被取消，而这次转发已经在
// 上游产生过用量。因此先摘掉取消信号再写，否则断连就把流水丢掉；
// 本超时用来兜住「数据库挂死导致协程悬空」。
const defaultWriteTimeout = 5 * time.Second

// Store 是记账落库需要的最小数据面。
type Store interface {
	InsertUsage(ctx context.Context, row store.UsageRow) (uint64, error)
}

// Settler 是结算入口，由 internal/settlement 的实现满足。
//
// 抽成接口是为了让记账路径不直接依赖结算包的具体实现：测试注入失败的替身即可验证
// 「结算失败退回占位流水」。
type Settler interface {
	Settle(ctx context.Context, in settlement.Input) error
}

// Recorder 是 domain.UsageRecorder 的存储实现。
type Recorder struct {
	store   Store
	settler Settler
	logf    func(msg string, args ...any)
	// writeTimeout 是单次写用量流水的耗时上限；非正时取 defaultWriteTimeout。
	writeTimeout time.Duration
}

// 编译期断言：记账实现满足流水线的用量记录接口。
var _ domain.UsageRecorder = (*Recorder)(nil)

// NewRecorder 构造记账实现；settler 为 nil 时只落占位流水（不结算），
// writeTimeout 非正时取默认值，logf 为 nil 时用 slog 的默认 logger。
func NewRecorder(st Store, settler Settler, writeTimeout time.Duration, logf func(msg string, args ...any)) *Recorder {
	if logf == nil {
		logf = slog.Warn
	}
	if writeTimeout <= 0 {
		writeTimeout = defaultWriteTimeout
	}
	return &Recorder{store: st, settler: settler, logf: logf, writeTimeout: writeTimeout}
}

// RecordUsage 把一次转发的用量结算并写进 billing_usage。
//
// 没有对应 Metric 的分量（当前只有服务端工具调用次数）记日志后丢弃：丢一个没有计价
// 口径的分量，代价远小于丢掉一整行已经发生的转发流水。
// 计算流程：先把内部统一用量映射为计费分量，再补上落库路径生成的 request=1，
// 然后交给结算器在事务里落库并扣账本；结算失败退回占位口径只落用量，并记结构化
// 错误日志——流水丢失比结算失败更糟。request 在这一层补写，结算与无定价占位两条
// 落库路径共用同一份分量，不会一条有 request、另一条没有。
// 落库失败返回错误，同时记日志；流水线忽略返回值，不影响已写给客户端的响应。
func (r *Recorder) RecordUsage(ctx context.Context, rec domain.UsageRecord) error {
	id, ok := access.IdentityFromContext(ctx)
	if !ok {
		// 未鉴权不应走到这里；按无归属处理而不写一行 account_id=0 的脏流水。
		return domain.NewError(domain.CodeInternal, "缺少鉴权上下文，无法归属用量")
	}
	metrics, dropped, conflicts := billing.UsageFromDomain(rec.Usage)
	// request 分量由落库路径生成，不来自适配器：一次请求一行流水，行数与该分量一致。
	// 在此处补写而非各写入路径自行处理，保证结算与占位两条路径的分量形状一致。
	metrics = billing.WithRequest(metrics)
	for _, name := range dropped {
		r.logf("用量分量没有对应的计费指标，已丢弃",
			"component", name, "request_id", rec.RequestID)
	}
	for _, conflict := range conflicts {
		r.logf("缓存写口径冲突，已按分档为准",
			"detail", conflict, "request_id", rec.RequestID)
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.writeTimeout)
	defer cancel()

	if r.settler != nil {
		err := r.settler.Settle(writeCtx, settlement.Input{
			RequestID:      rec.RequestID,
			MerchantID:     id.MerchantID,
			AccountID:      id.AccountID,
			ChannelID:      rec.ChannelID,
			APIKeyID:       id.APIKeyID,
			Model:          rec.Model,
			RequestedModel: rec.RequestedModel,
			Protocol:       string(rec.Protocol),
			CrossProtocol:  rec.CrossProtocol,
			Usage:          metrics,
		})
		if err == nil {
			return nil
		}
		r.logf("结算失败，按占位口径落流水", "request_id", rec.RequestID, "error", err)
	}

	if _, err := r.store.InsertUsage(writeCtx, store.UsageRow{
		MerchantID:     id.MerchantID,
		AccountID:      id.AccountID,
		ChannelID:      rec.ChannelID,
		APIKeyID:       id.APIKeyID,
		Model:          rec.Model,
		RequestedModel: rec.RequestedModel,
		Protocol:       string(rec.Protocol),
		CrossProtocol:  rec.CrossProtocol,
		Usage:          metrics,
	}); err != nil {
		r.logf("写入 billing_usage 失败", "request_id", rec.RequestID, "error", err)
		return err
	}
	return nil
}
