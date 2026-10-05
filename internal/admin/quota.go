package admin

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/quota"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是窗口限额的管理动作：新增、列出（含已用量与剩余额度）、删除、重置。
//
// 已用量的聚合口径不自建：list 复用 internal/quota 的窗口计算与 quota.Repo 的聚合
// 查询，与鉴权链上的判定是同一份实现，两处不会漂移。

// QuotaInput 是新增限额定义的输入。
type QuotaInput struct {
	Scope       billing.Scope
	ScopeID     uint64
	Metric      billing.Metric
	WindowKind  billing.WindowKind
	Period      billing.Period
	LimitAmount string
	Action      billing.Action
}

// CreateQuota 新增一条窗口型限额定义。
//
// window_kind 与 period 的组合在写入口校验：不受支持的组合永远不会被判定，
// 写进去等于一条静默失效的配置。
func (s *Service) CreateQuota(ctx context.Context, in QuotaInput) (uint64, error) {
	if err := billing.ValidateScope(in.Scope); err != nil {
		return 0, err
	}
	if err := requireID("限额 scope-id", in.ScopeID); err != nil {
		return 0, err
	}
	if err := billing.ValidateMetric(in.Metric); err != nil {
		return 0, err
	}
	if err := quota.ValidWindow(in.WindowKind, in.Period); err != nil {
		return 0, err
	}
	if err := requirePositiveDecimal("限额 limit", in.LimitAmount); err != nil {
		return 0, err
	}
	if err := billing.ValidateAction(in.Action); err != nil {
		return 0, err
	}
	return s.store.InsertQuota(ctx, store.Quota{
		Scope:       in.Scope,
		ScopeID:     in.ScopeID,
		Metric:      in.Metric,
		WindowKind:  in.WindowKind,
		Period:      in.Period,
		LimitAmount: in.LimitAmount,
		Action:      in.Action,
	})
}

// QuotaView 是限额行的展示口径：定义加当前窗口的已用量与剩余额度。
//
// Used / Remaining 为 nil 表示该行无法判定（窗口组合不可判定或聚合失败）：
// 展示层用占位符，不因为一行算不出已用量而让整个 list 失败。
type QuotaView struct {
	ID          uint64             `json:"id"`
	Scope       billing.Scope      `json:"scope"`
	ScopeID     uint64             `json:"scope_id"`
	Metric      billing.Metric     `json:"metric"`
	WindowKind  billing.WindowKind `json:"window_kind"`
	Period      billing.Period     `json:"period"`
	LimitAmount string             `json:"limit_amount"`
	Action      billing.Action     `json:"action"`
	Used        *string            `json:"used"`
	Remaining   *string            `json:"remaining"`
}

// ListQuotas 列出限额定义并附当前窗口的已用量与剩余额度；scopeID 为 0 时列出全部。
func (s *Service) ListQuotas(ctx context.Context, scope billing.Scope, scopeID uint64) ([]QuotaView, error) {
	limits, err := s.store.Quotas(ctx, scope, scopeID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	views := make([]QuotaView, 0, len(limits))
	for _, limit := range limits {
		views = append(views, s.quotaView(ctx, limit, now))
	}
	return views, nil
}

// quotaView 算一条限额的展示行。
func (s *Service) quotaView(ctx context.Context, limit quota.Limit, now time.Time) QuotaView {
	view := QuotaView{
		ID:          limit.ID,
		Scope:       limit.Scope,
		ScopeID:     limit.ScopeID,
		Metric:      limit.Metric,
		WindowKind:  limit.WindowKind,
		Period:      limit.Period,
		LimitAmount: limit.LimitAmount.String(),
		Action:      limit.Action,
	}
	used, ok, err := quota.Used(ctx, s.store, limit, now)
	if !ok || err != nil {
		return view
	}
	usedText := used.String()
	view.Used = &usedText
	remaining := limit.LimitAmount.Sub(used)
	// 已用量可能因 reset 基准之前的流水或负调整超出限额，剩余额度不显示负数。
	if remaining.IsNegative() {
		remaining = decimal.Zero
	}
	remainingText := remaining.String()
	view.Remaining = &remainingText
	return view
}

// ResetQuotaInput 是重置限额窗口基准的输入。
type ResetQuotaInput struct {
	QuotaID    uint64
	BaselineAt time.Time
	Reason     string
	Operator   string
}

// ResetQuota 追加一条重置事件，把聚合下界推到 baseline_at。
//
// 只追加事件、不改限额行：已用量由流水聚合派生，重置是一次有审计的基准变更。
// reason 与 operator 必填；baseline 留空取当前时刻。
func (s *Service) ResetQuota(ctx context.Context, in ResetQuotaInput) (uint64, error) {
	if err := requireID("限额 id", in.QuotaID); err != nil {
		return 0, err
	}
	if err := requireString("限额重置 reason", in.Reason); err != nil {
		return 0, err
	}
	if err := requireString("限额重置 operator", in.Operator); err != nil {
		return 0, err
	}
	baseline := in.BaselineAt
	if baseline.IsZero() {
		baseline = s.now()
	}
	return s.store.InsertQuotaEvent(ctx, store.QuotaEvent{
		QuotaID:    in.QuotaID,
		Event:      billing.QuotaEventReset,
		BaselineAt: baseline,
		Reason:     in.Reason,
		Operator:   in.Operator,
	})
}

// DeleteQuota 按 id 删除限额定义。
func (s *Service) DeleteQuota(ctx context.Context, id uint64) error {
	if err := requireID("限额 id", id); err != nil {
		return err
	}
	return s.store.DeleteQuota(ctx, id)
}
