package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/plan"
	"github.com/sumwai/tokenmp/internal/quota"
)

// 本文件是上游套餐的管理动作：新增与列出。
//
// 已用量的口径来自最近一次探针采集（upstream_plan_quota.last_used），不是本地聚合：
// 上游的窗口起点由上游决定，本地无从复算。列出的是采集事实加派生百分比，
// 窗口重置时刻则复用 internal/quota 的窗口计算，与下游限额同一套。

// defaultPlanMultiplier 是未指定套餐倍率时的取值。
const defaultPlanMultiplier = "1"

// usedPercentScale 是已用百分比保留的小数位数。
const usedPercentScale = 2

// percentBasis 是百分比换算基数：已用量 ×100 / 限额 = 百分比。
const percentBasis = 100

// PlanQuotaInput 是套餐里一条限额行的输入。
type PlanQuotaInput struct {
	Metric      billing.Metric
	WindowKind  billing.WindowKind
	Period      billing.Period
	LimitAmount string
}

// PlanInput 是新增套餐的输入。
type PlanInput struct {
	MerchantID uint64
	CredGroup  string
	Name       string
	Multiplier string
	ValidFrom  *time.Time
	ValidTo    *time.Time
	Quotas     []PlanQuotaInput
}

// CreatePlan 新增一条上游套餐及其限额行，返回套餐 id。
//
// 窗口组合、指标、限额都在写入口校验：写进去的每一条都必须是能被判定与采集的口径，
// 否则它只会成为一条永远不生效的配置。
func (s *Service) CreatePlan(ctx context.Context, in PlanInput) (uint64, error) {
	if err := requireID("套餐 merchant", in.MerchantID); err != nil {
		return 0, err
	}
	if err := requireString("套餐 cred-group", in.CredGroup); err != nil {
		return 0, err
	}
	if err := requireString("套餐 name", in.Name); err != nil {
		return 0, err
	}
	multiplier := in.Multiplier
	if multiplier == "" {
		multiplier = defaultPlanMultiplier
	}
	if err := requirePositiveDecimal("套餐 multiplier", multiplier); err != nil {
		return 0, err
	}
	multiplierValue, err := parseDecimal(multiplier)
	if err != nil {
		return 0, err
	}
	if len(in.Quotas) == 0 {
		return 0, errors.New("admin: 套餐至少需要一条限额行")
	}
	quotas := make([]plan.Quota, 0, len(in.Quotas))
	for i, item := range in.Quotas {
		if err := billing.ValidateMetric(item.Metric); err != nil {
			return 0, err
		}
		if err := quota.ValidWindow(item.WindowKind, item.Period); err != nil {
			return 0, err
		}
		if err := requirePositiveDecimal(fmt.Sprintf("套餐限额[%d] limit", i), item.LimitAmount); err != nil {
			return 0, err
		}
		limit, err := parseDecimal(item.LimitAmount)
		if err != nil {
			return 0, err
		}
		quotas = append(quotas, plan.Quota{
			Metric:      item.Metric,
			WindowKind:  item.WindowKind,
			Period:      item.Period,
			LimitAmount: limit,
		})
	}
	return s.store.InsertUpstreamPlan(ctx, plan.UpstreamPlan{
		MerchantID: in.MerchantID,
		CredGroup:  in.CredGroup,
		Name:       in.Name,
		Multiplier: multiplierValue,
		ValidFrom:  in.ValidFrom,
		ValidTo:    in.ValidTo,
	}, quotas)
}

// PlanQuotaView 是套餐里一条限额行的展示口径。
//
// UsedPercent 与 ResetsAt 是派生字段：前者由 last_used / limit_amount 得出，
// 后者复用 internal/quota 的窗口计算。无法判定时留空，不让一行算不出来拖垮整个列表。
type PlanQuotaView struct {
	ID            uint64             `json:"id"`
	Metric        billing.Metric     `json:"metric"`
	WindowKind    billing.WindowKind `json:"window_kind"`
	Period        billing.Period     `json:"period"`
	LimitAmount   string             `json:"limit_amount"`
	LastUsed      string             `json:"last_used"`
	UsedPercent   *string            `json:"used_percent"`
	ResetsAt      *time.Time         `json:"resets_at"`
	LastCheckedAt *time.Time         `json:"last_checked_at"`
}

// PlanView 是一条上游套餐的展示口径。
type PlanView struct {
	ID            uint64          `json:"id"`
	MerchantID    uint64          `json:"merchant_id"`
	CredGroup     string          `json:"cred_group"`
	Name          string          `json:"name"`
	Multiplier    string          `json:"multiplier"`
	ValidFrom     *time.Time      `json:"valid_from"`
	ValidTo       *time.Time      `json:"valid_to"`
	LastCheckedAt *time.Time      `json:"last_checked_at"`
	Quotas        []PlanQuotaView `json:"quotas"`
}

// ListPlans 列出全部上游套餐及限额行。
func (s *Service) ListPlans(ctx context.Context) ([]PlanView, error) {
	plans, err := s.store.Plans(ctx, 0)
	if err != nil {
		return nil, err
	}
	now := s.now()
	views := make([]PlanView, 0, len(plans))
	for _, p := range plans {
		views = append(views, s.planView(p, now))
	}
	return views, nil
}

// planView 算一条套餐的展示行。
func (s *Service) planView(p plan.UpstreamPlan, now time.Time) PlanView {
	view := PlanView{
		ID:            p.ID,
		MerchantID:    p.MerchantID,
		CredGroup:     p.CredGroup,
		Name:          p.Name,
		Multiplier:    p.Multiplier.String(),
		ValidFrom:     p.ValidFrom,
		ValidTo:       p.ValidTo,
		LastCheckedAt: optionalTime(p.LastCheckedAt),
	}
	view.Quotas = make([]PlanQuotaView, 0, len(p.Quotas))
	for _, q := range p.Quotas {
		view.Quotas = append(view.Quotas, planQuotaView(q, now))
	}
	return view
}

// planQuotaView 算一条套餐限额的展示行。
func planQuotaView(q plan.Quota, now time.Time) PlanQuotaView {
	view := PlanQuotaView{
		ID:            q.ID,
		Metric:        q.Metric,
		WindowKind:    q.WindowKind,
		Period:        q.Period,
		LimitAmount:   q.LimitAmount.String(),
		LastUsed:      q.LastUsed.String(),
		LastCheckedAt: optionalTime(q.LastCheckedAt),
	}
	if !q.LimitAmount.IsZero() {
		percent := q.LastUsed.Mul(decimal.NewFromInt(percentBasis)).DivRound(q.LimitAmount, usedPercentScale).String()
		view.UsedPercent = &percent
	}
	if resetsAt, ok := quota.ResetAt(q.WindowKind, q.Period, now); ok {
		view.ResetsAt = &resetsAt
	}
	return view
}

// optionalTime 把零值时间转成 nil，使 JSON 输出为 null 而不是零值时间。
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
