// Package quota 实现窗口型限额的判定口径：窗口起点计算、窗口组合合法性、
// 已用量比较与超限结论。
//
// 独立成包而不是塞进 store 或 cmd：窗口边界与超限判定都是纯规则，不依赖数据库，
// 单独成包后可用表驱动单测覆盖全部分支；store 只提供聚合查询，cmd 只做装配与
// HTTP 响应。全仓库只有本包实现窗口计算与超限比较，其它位置出现同义计算即为
// 重复实现。
//
// 判定沿用结算失败的宽容语义：读不到限额定义、聚合查询失败、窗口组合不可判定
// 都不阻断转发，只记结构化日志。限额执行不该成为转发链路上的新单点。
package quota

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// Limit 是一条窗口型限额定义。
//
// LimitAmount 用 decimal 承载：limit_amount 是 DECIMAL(24,8)，与聚合结果同口径
// 比较，不经过 float64。
type Limit struct {
	ID          uint64
	Scope       billing.Scope
	ScopeID     uint64
	Metric      billing.Metric
	WindowKind  billing.WindowKind
	Period      billing.Period
	LimitAmount decimal.Decimal
	Action      billing.Action
}

// Exceeded 报告已用量是否达到限额。
//
// 限额是上限，已用量达到 limit_amount 即不再放行（>= 而非 >）：否则恰好用满的那次
// 请求之后还会再放行一次，实际用量会稳定越过限额一格。
func (l Limit) Exceeded(used decimal.Decimal) bool {
	return used.GreaterThanOrEqual(l.LimitAmount)
}

// UsageQuery 是一条限额在某个窗口内的用量聚合条件。
type UsageQuery struct {
	Scope   billing.Scope
	ScopeID uint64
	// QuotaID 用于读 account_quota_event 里最近一次 reset 基准。
	QuotaID uint64
	Metric  billing.Metric
	// Since 是窗口起点；零值表示不做窗口下界，聚合只受 reset 基准约束（period=total）。
	Since time.Time
}

// Repo 是限额判定依赖的数据面。
type Repo interface {
	// Quotas 读某个 scope 与实体下的全部限额定义；scopeID 为 0 时读全部。
	Quotas(ctx context.Context, scope billing.Scope, scopeID uint64) ([]Limit, error)
	// Usage 聚合一条限额在窗口内的已用量。
	Usage(ctx context.Context, q UsageQuery) (decimal.Decimal, error)
}

// Used 聚合一条限额在当前窗口内的已用量。
//
// ok 为 false 表示 window_kind 与 period 的组合没有可判定的窗口（历史脏行或越权
// 写入），调用方应跳过该限额而不是按全时段聚合。error 非 nil 表示聚合查询失败。
func Used(ctx context.Context, repo Repo, limit Limit, now time.Time) (decimal.Decimal, bool, error) {
	start, ok := WindowStart(limit.WindowKind, limit.Period, now)
	if !ok {
		return decimal.Zero, false, nil
	}
	used, err := repo.Usage(ctx, UsageQuery{
		Scope:   limit.Scope,
		ScopeID: limit.ScopeID,
		QuotaID: limit.ID,
		Metric:  limit.Metric,
		Since:   start,
	})
	if err != nil {
		return decimal.Zero, true, err
	}
	return used, true, nil
}

// Violation 是一次超限结论。
type Violation struct {
	Limit Limit
	Used  decimal.Decimal
	// RetryAfter 是 throttle 处置下建议的退避时长；无法估出时为零值。
	RetryAfter time.Duration
}

// Service 执行窗口限额判定。
type Service struct {
	repo Repo
	logf func(msg string, args ...any)
}

// New 构造判定器；logf 为 nil 时不记日志。
func New(repo Repo, logf func(msg string, args ...any)) *Service {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Service{repo: repo, logf: logf}
}

// scopeRef 是一个限额判定维度与其实体 id。
type scopeRef struct {
	scope billing.Scope
	id    uint64
}

// Check 判定账户与 API key 两个维度的限额，未超限或无法判定时返回 nil。
//
// 两个维度共用同一套窗口计算与聚合查询，只是把维度列换成 account_id / api_key_id，
// 没有第二套实现。
//
// 不做 channel 与 plan 维度：鉴权阶段尚未选路，渠道限额没有可判定的对象（按渠道的
// 速率保护由限流承担）；plan 在流水上没有维度列。存放这两类 scope 的行保留在表里，
// 由具备相应上下文的消费者处理。
//
// apiKeyID 为 0 时跳过 key 维度：0 表示本次调用没有可归属的 key，流水里
// api_key_id = 0 的行（历史或内部写入）因此不计入任何 key 限额。
//
// 多个限额同时超限时优先返回 reject：它是更强的处置，用 throttle 盖住 reject 会让
// 已被明确拒绝的用量照样通过。
//
// 本 issue 不做缓存，每次请求按「单维度 × 单限额」聚合一次。后续如需缓存，入口就在
// 这里：按 (QuotaID, 窗口起点) 缓存 Used 的结果，失效时刻取窗口起点加周期。
func (s *Service) Check(ctx context.Context, accountID, apiKeyID uint64, now time.Time) *Violation {
	refs := []scopeRef{{scope: billing.ScopeAccount, id: accountID}}
	if apiKeyID != 0 {
		refs = append(refs, scopeRef{scope: billing.ScopeAPIKey, id: apiKeyID})
	}
	var throttle *Violation
	for _, ref := range refs {
		violation := s.checkScope(ctx, ref, now)
		if violation == nil {
			continue
		}
		if violation.Limit.Action == billing.ActionReject {
			return violation
		}
		if throttle == nil {
			throttle = violation
		}
	}
	return throttle
}

// checkScope 判定单个维度下的全部限额，未超限或无法判定时返回 nil。
func (s *Service) checkScope(ctx context.Context, ref scopeRef, now time.Time) *Violation {
	limits, err := s.repo.Quotas(ctx, ref.scope, ref.id)
	if err != nil {
		s.logf("限额定义读取失败，放行本次请求",
			"scope", string(ref.scope), "scope_id", ref.id, "error", err)
		return nil
	}
	var throttle *Violation
	for _, limit := range limits {
		used, ok, err := Used(ctx, s.repo, limit, now)
		if !ok {
			s.logf("限额的窗口组合不可判定，已跳过",
				"quota_id", limit.ID, "window_kind", string(limit.WindowKind), "period", string(limit.Period))
			continue
		}
		if err != nil {
			s.logf("限额聚合失败，放行本次请求",
				"quota_id", limit.ID, "scope", string(ref.scope), "scope_id", ref.id, "error", err)
			continue
		}
		if !limit.Exceeded(used) {
			continue
		}
		violation := &Violation{
			Limit:      limit,
			Used:       used,
			RetryAfter: RetryAfter(limit.WindowKind, limit.Period, now),
		}
		if limit.Action == billing.ActionReject {
			return violation
		}
		if throttle == nil {
			throttle = violation
		}
	}
	return throttle
}
