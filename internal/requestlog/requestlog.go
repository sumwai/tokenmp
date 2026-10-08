// Package requestlog 把转发的观测事实落成请求记录（request_log 与尝试时间线）。
//
// 落点选择与另两条观测出路的分工：
//
//   - 访问日志（observability.accessLogger）是实时出口，一行 JSON 进标准输出；
//   - 尝试日志（observability.attemptObserver）同上，一行一次上游尝试；
//   - 本包是留存出口，落库后可由页面按账户回溯，明细超期后由按天聚合继续回答趋势。
//
// 职责边界：三个观测量分别来自流水线的尝试记录（domain.Observer）、记账路径的用量
// （domain.UsageRecorder）与入口层的访问记录（transport.AccessLogger），本包只做
// 组装与归属补全，不产生任何转发事实。
//
// 观测是转发的旁路：本包所有写入失败都只记日志，绝不改变对客户端的响应。
package requestlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/transport"
)

// 请求终态取值，与契约的枚举一致。
const (
	statusSuccess   = "success"
	statusFailed    = "failed"
	statusCancelled = "cancelled"
)

// defaultWriteTimeout 是单次落库的耗时上限。
//
// 写请求记录发生在请求终态之后：客户端断开会让请求上下文被取消，而这次转发已经在
// 上游发生过。因此先摘掉取消信号再写，本超时用来兜住「数据库挂死导致协程悬空」。
const defaultWriteTimeout = 5 * time.Second

// Store 是请求记录落库需要的最小数据面。
type Store interface {
	// InsertRequestAttempt 写一条尝试时间线记录。
	InsertRequestAttempt(ctx context.Context, a store.RequestAttempt) error
	// UpsertRequestUsage 写归属与用量；与终态写入互不覆盖。
	UpsertRequestUsage(ctx context.Context, u store.RequestUsage) error
	// UpsertRequestOutcome 写终态字段。
	UpsertRequestOutcome(ctx context.Context, o store.RequestOutcome) error
	// LastRequestAttempt 读最后一次尝试，用于补齐只出现在尝试记录里的字段；
	// 没有尝试时返回 sql.ErrNoRows。
	LastRequestAttempt(ctx context.Context, requestID string) (*store.RequestAttempt, error)
	// UpsertRequestStatsDaily 累加按天计数缓存。
	UpsertRequestStatsDaily(ctx context.Context, accountID uint64, day time.Time,
		model string, apiKeyID uint64, status string) error
}

// Options 是构造参数；Store 必填。
type Options struct {
	Store Store
	// Now 提供当前时刻，nil 时取系统时钟。
	Now func() time.Time
	// WriteTimeout 是单次落库的耗时上限；非正时取默认值。
	WriteTimeout time.Duration
	// Logf 是失败输出，nil 时取 slog.Warn。
	Logf func(msg string, args ...any)
}

// Recorder 是请求记录的落库实现，同时满足三个观测端口。
//
// 三个端口分别对应事实的三个来源，缺一个都会让记录不完整：尝试给出上游侧事实，
// 用量给出归属与计数，访问记录给出客户端侧事实与终态。
type Recorder struct {
	store        Store
	now          func() time.Time
	writeTimeout time.Duration
	logf         func(msg string, args ...any)
}

// 编译期断言：本实现满足三个观测端口。
var (
	_ domain.Observer        = (*Recorder)(nil)
	_ domain.UsageRecorder   = (*Recorder)(nil)
	_ transport.AccessLogger = (*Recorder)(nil)
)

// New 构造请求记录实现。
func New(opts Options) *Recorder {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultWriteTimeout
	}
	if opts.Logf == nil {
		opts.Logf = slog.Warn
	}
	return &Recorder{store: opts.Store, now: opts.Now, writeTimeout: opts.WriteTimeout, logf: opts.Logf}
}

// RecordAttempt 写一条尝试记录，实现 domain.Observer。
func (r *Recorder) RecordAttempt(ctx context.Context, rec domain.AttemptRecord) error {
	if r == nil || r.store == nil {
		return nil
	}
	if rec.RequestID == "" {
		return nil
	}
	attempt := store.RequestAttempt{
		RequestID:        rec.RequestID,
		Attempt:          rec.Attempt,
		Outcome:          string(rec.Outcome),
		UpstreamStatus:   rec.UpstreamStatus,
		FailureClass:     rec.FailureClass,
		ErrorCode:        rec.ErrorCode,
		ClientProtocol:   string(rec.ClientProtocol),
		UpstreamProtocol: string(rec.UpstreamProtocol),
		CrossProtocol:    rec.CrossProtocol,
		DurationMS:       rec.EndedAt.Sub(rec.StartedAt).Milliseconds(),
		RequestedModel:   rec.RequestedModel,
		UpstreamModel:    rec.UpstreamModel,
		RewrittenParts:   marshalParts(rec.RewrittenParts),
		CreatedAt:        rec.StartedAt,
	}
	writeCtx, cancel := r.writeContext(ctx)
	defer cancel()
	if err := r.store.InsertRequestAttempt(writeCtx, attempt); err != nil {
		r.logf("写请求尝试记录失败", "request_id", rec.RequestID, "attempt", rec.Attempt, "error", err)
		return err
	}
	return nil
}

// RecordUsage 写请求记录的归属与用量，实现 domain.UsageRecorder。
//
// 归属取自鉴权上下文：页面按账户读记录，缺了归属这一行谁都读不到。
func (r *Recorder) RecordUsage(ctx context.Context, rec domain.UsageRecord) error {
	if r == nil || r.store == nil {
		return nil
	}
	identity, ok := access.IdentityFromContext(ctx)
	if !ok {
		// 未鉴权不应走到这里；按无归属处理而不写一行 account_id=0 的脏记录。
		return domain.NewError(domain.CodeInternal, "缺少鉴权上下文，无法归属请求记录")
	}
	writeCtx, cancel := r.writeContext(ctx)
	defer cancel()
	usage := store.RequestUsage{
		RequestID:  rec.RequestID,
		MerchantID: identity.MerchantID,
		AccountID:  identity.AccountID,
		APIKeyID:   identity.APIKeyID,
		CreatedAt:  r.now(),
		Usage:      marshalUsage(rec.Usage),
	}
	if err := r.store.UpsertRequestUsage(writeCtx, usage); err != nil {
		r.logf("写请求记录用量失败", "request_id", rec.RequestID, "error", err)
		return err
	}
	return nil
}

// LogAccess 写请求记录的终态，实现 transport.AccessLogger。
//
// 未鉴权的请求不落记录：页面按账户读记录，一条没有归属的记录既读不到也聚合不到，
// 落库只会白占一行。
func (r *Recorder) LogAccess(ctx context.Context, record transport.AccessRecord) {
	if r == nil || r.store == nil {
		return
	}
	if record.RequestID == "" {
		return
	}
	identity, ok := access.IdentityFromContext(ctx)
	if !ok {
		return
	}
	status := requestStatus(ctx, record.HTTPStatus)
	// 上游侧事实（上游模型、上游协议、失败分类与改写标注）只出现在尝试记录里，
	// 终态的访问记录不带它们；读最后一次尝试补齐，比重放一遍选路便宜。
	upstreamModel, upstreamProtocol, failureClass, rewrittenParts := r.lastAttemptFacts(ctx, record.RequestID)

	outcome := store.RequestOutcome{
		RequestID:        record.RequestID,
		MerchantID:       identity.MerchantID,
		AccountID:        identity.AccountID,
		APIKeyID:         identity.APIKeyID,
		CreatedAt:        r.now(),
		Status:           status,
		HTTPStatus:       record.HTTPStatus,
		UpstreamStatus:   record.UpstreamStatus,
		FailureClass:     failureClass,
		ErrorCode:        record.ErrorCode,
		DurationMS:       record.DurationMS,
		RequestedModel:   record.Model,
		UpstreamModel:    upstreamModel,
		Protocol:         string(record.Protocol),
		UpstreamProtocol: upstreamProtocol,
		CrossProtocol:    record.CrossProtocol,
		Stream:           record.Stream,
		WrittenBytes:     int64(record.WrittenBytes),
		ClientIP:         record.RemoteAddr,
		UserAgent:        record.UserAgent,
		RewrittenParts:   rewrittenParts,
	}
	writeCtx, cancel := r.writeContext(ctx)
	defer cancel()
	if err := r.store.UpsertRequestOutcome(writeCtx, outcome); err != nil {
		r.logf("写请求记录终态失败", "request_id", record.RequestID, "error", err)
		return
	}
	// 计数缓存随终态累加：只有进入终态的请求才计入趋势，中断的请求既不在列表里
	// 也不该在图上出现。
	day := time.Date(outcome.CreatedAt.Year(), outcome.CreatedAt.Month(), outcome.CreatedAt.Day(),
		0, 0, 0, 0, outcome.CreatedAt.Location())
	if err := r.store.UpsertRequestStatsDaily(writeCtx, identity.AccountID, day,
		record.Model, identity.APIKeyID, status); err != nil {
		r.logf("累加请求计数失败", "request_id", record.RequestID, "error", err)
	}
}

// lastAttemptFacts 读最后一次尝试里只属于上游侧的事实；读不到时返回空值。
func (r *Recorder) lastAttemptFacts(ctx context.Context, requestID string) (upstreamModel, upstreamProtocol, failureClass string, rewrittenParts []byte) {
	writeCtx, cancel := r.writeContext(ctx)
	defer cancel()
	attempt, err := r.store.LastRequestAttempt(writeCtx, requestID)
	if err != nil {
		// 没有尝试是正常情形（请求在选路前就失败）；其余错误只影响补齐度，不影响终态落库。
		if !errors.Is(err, sql.ErrNoRows) {
			r.logf("读取最后一次尝试失败", "request_id", requestID, "error", err)
		}
		return "", "", "", nil
	}
	return attempt.UpstreamModel, attempt.UpstreamProtocol, attempt.FailureClass, attempt.RewrittenParts
}

// writeContext 摘掉取消信号并加上写超时：请求终态之后客户端断连不应丢掉已经发生的记录。
func (r *Recorder) writeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), r.writeTimeout)
}

// requestStatus 由客户端侧事实推导终态。
//
// 客户端断开是独立的终态：它与「网关返回了错误」不同，页面上一个是「客户端主动结束」，
// 一个是「调用失败」，两者下一步动作不同。连接断开时上下文已被取消，据此判定。
func requestStatus(ctx context.Context, httpStatus int) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return statusCancelled
	}
	if httpStatus >= 200 && httpStatus < 300 {
		return statusSuccess
	}
	return statusFailed
}

// marshalUsage 把用量序列化为落库 JSON；未取得用量时返回 nil（写 SQL NULL）。
//
// 未取得用量与「取得的全是 0」是两种事实：前者写 NULL，页面显示「未取得」；
// 后者写零值对象，页面显示 0。
func marshalUsage(usage domain.Usage) []byte {
	if !usage.Known() {
		return nil
	}
	payload, err := json.Marshal(usage)
	if err != nil {
		return nil
	}
	return payload
}

// marshalParts 把改写标注序列化为落库 JSON；无改写时返回 nil。
func marshalParts(parts domain.RewriteParts) []byte {
	if len(parts) == 0 {
		return nil
	}
	payload, err := json.Marshal(parts)
	if err != nil {
		return nil
	}
	return payload
}
