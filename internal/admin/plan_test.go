package admin

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/plan"
	"github.com/sumwai/tokenmp/internal/store"
)

// planFixedNow 与 fixedNow 同源，单独命名以免与既有用例的语义混淆。
var planFixedNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func TestCreatePlanRejectsBadInput(t *testing.T) {
	validQuota := PlanQuotaInput{
		Metric: billing.MetricInputToken, WindowKind: billing.WindowKindRolling,
		Period: billing.Period5h, LimitAmount: "100",
	}
	tests := []struct {
		name string
		give PlanInput
	}{
		{name: "商家为空", give: PlanInput{CredGroup: "g", Name: "n", Quotas: []PlanQuotaInput{validQuota}}},
		{name: "凭据分组为空", give: PlanInput{MerchantID: 1, Name: "n", Quotas: []PlanQuotaInput{validQuota}}},
		{name: "套餐名为空", give: PlanInput{MerchantID: 1, CredGroup: "g", Quotas: []PlanQuotaInput{validQuota}}},
		{name: "倍率为负", give: PlanInput{MerchantID: 1, CredGroup: "g", Name: "n", Multiplier: "-1", Quotas: []PlanQuotaInput{validQuota}}},
		{name: "没有限额行", give: PlanInput{MerchantID: 1, CredGroup: "g", Name: "n"}},
		{
			name: "未知指标",
			give: PlanInput{MerchantID: 1, CredGroup: "g", Name: "n", Quotas: []PlanQuotaInput{{
				Metric: "not_a_metric", WindowKind: billing.WindowKindRolling, Period: billing.Period5h, LimitAmount: "1",
			}}},
		},
		{
			name: "窗口组合不可判定",
			give: PlanInput{MerchantID: 1, CredGroup: "g", Name: "n", Quotas: []PlanQuotaInput{{
				Metric: billing.MetricInputToken, WindowKind: billing.WindowKindCalendar, Period: billing.Period5h, LimitAmount: "1",
			}}},
		},
		{
			name: "限额非正",
			give: PlanInput{MerchantID: 1, CredGroup: "g", Name: "n", Quotas: []PlanQuotaInput{{
				Metric: billing.MetricInputToken, WindowKind: billing.WindowKindRolling, Period: billing.Period5h, LimitAmount: "0",
			}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			if _, err := newService(f).CreatePlan(context.Background(), tt.give); err == nil {
				t.Fatal("非法输入应当被拒绝")
			}
			if f.called("InsertUpstreamPlan") {
				t.Error("校验失败不应触达存储层")
			}
		})
	}
}

func TestCreatePlanWritesStructuredRows(t *testing.T) {
	f := &fakeStore{}
	var gotPlan plan.UpstreamPlan
	var gotQuotas []plan.Quota
	f.insertUpstreamPlan = func(_ context.Context, p plan.UpstreamPlan, quotas []plan.Quota) (uint64, error) {
		gotPlan, gotQuotas = p, quotas
		return 42, nil
	}
	id, err := newService(f).CreatePlan(context.Background(), PlanInput{
		MerchantID: 3,
		CredGroup:  "group-a",
		Name:       "套餐",
		Quotas: []PlanQuotaInput{
			{Metric: billing.MetricInputToken, WindowKind: billing.WindowKindRolling, Period: billing.Period5h, LimitAmount: "1000000"},
			{Metric: billing.MetricRequest, WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay, LimitAmount: "5000"},
		},
	})
	if err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if id != 42 {
		t.Errorf("套餐 id = %d，期望 42", id)
	}
	if gotPlan.MerchantID != 3 || gotPlan.CredGroup != "group-a" || gotPlan.Name != "套餐" {
		t.Errorf("套餐内容不符：%+v", gotPlan)
	}
	if !gotPlan.Multiplier.Equal(decimal.NewFromInt(1)) {
		t.Errorf("未指定倍率应默认 1，得到 %s", gotPlan.Multiplier.String())
	}
	if len(gotQuotas) != 2 {
		t.Fatalf("限额行数 = %d，期望 2", len(gotQuotas))
	}
	if gotQuotas[0].Metric != billing.MetricInputToken || !gotQuotas[0].LimitAmount.Equal(decimal.RequireFromString("1000000")) {
		t.Errorf("第一条限额不符：%+v", gotQuotas[0])
	}
	if gotQuotas[1].Period != billing.PeriodDay {
		t.Errorf("第二条限额周期不符：%+v", gotQuotas[1])
	}
}

func TestCreateChannelValidatesConfig(t *testing.T) {
	base := ChannelInput{
		MerchantID: 1, Name: "n", Type: store.ChannelTypeOpenAIChat, CredGroup: "g", BaseURL: "u",
	}

	f := &fakeStore{}
	withBadConfig := base
	withBadConfig.Config = "[1]"
	if _, err := newService(f).CreateChannel(context.Background(), withBadConfig); err == nil {
		t.Fatal("数组形式的 config 应当被拒绝")
	}
	if f.called("InsertChannel") {
		t.Error("校验失败不应触达存储层")
	}

	f = &fakeStore{}
	var got []byte
	f.insertChannel = func(_ context.Context, c store.Channel) (uint64, error) {
		got = c.Config
		return 1, nil
	}
	withConfig := base
	withConfig.Config = `{"probe":{"url":"https://up"}}`
	if _, err := newService(f).CreateChannel(context.Background(), withConfig); err != nil {
		t.Fatalf("合法 config 不应报错：%v", err)
	}
	if string(got) != withConfig.Config {
		t.Errorf("config 应原样传给存储层，得到 %s", got)
	}
}

func TestListPlansComputesPercentAndResetsAt(t *testing.T) {
	checkedAt := planFixedNow.Add(-2 * time.Minute)
	f := &fakeStore{}
	f.plans = func(context.Context, uint64) ([]plan.UpstreamPlan, error) {
		return []plan.UpstreamPlan{{
			ID: 7, MerchantID: 3, CredGroup: "group-a", Name: "套餐",
			Multiplier:    decimal.RequireFromString("1.5"),
			LastCheckedAt: checkedAt,
			Quotas: []plan.Quota{{
				ID: 11, PlanID: 7,
				Metric: billing.MetricInputToken, WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay,
				LimitAmount: decimal.RequireFromString("100"),
				LastUsed:    decimal.RequireFromString("25"),
			}},
		}}, nil
	}
	s := New(f, WithClock(func() time.Time { return planFixedNow }))
	views, err := s.ListPlans(context.Background())
	if err != nil {
		t.Fatalf("列出套餐失败：%v", err)
	}
	if len(views) != 1 || len(views[0].Quotas) != 1 {
		t.Fatalf("套餐视图层数不符：%+v", views)
	}
	quota := views[0].Quotas[0]
	if quota.UsedPercent == nil || *quota.UsedPercent != "25" {
		t.Errorf("已用百分比 = %v，期望 25", quota.UsedPercent)
	}
	if quota.ResetsAt == nil || !quota.ResetsAt.Equal(time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("重置时刻 = %v，期望 2026-10-06T00:00:00Z", quota.ResetsAt)
	}
	if views[0].LastCheckedAt == nil || !views[0].LastCheckedAt.Equal(checkedAt) {
		t.Errorf("套餐采集时刻 = %v，期望 %s", views[0].LastCheckedAt, checkedAt)
	}
}

func TestListPlansWithoutCheckedAt(t *testing.T) {
	f := &fakeStore{}
	f.plans = func(context.Context, uint64) ([]plan.UpstreamPlan, error) {
		return []plan.UpstreamPlan{{
			ID: 7, MerchantID: 3, CredGroup: "group-a", Name: "套餐",
			Multiplier: decimal.RequireFromString("1"),
			Quotas: []plan.Quota{{
				ID: 11, PlanID: 7,
				Metric: billing.MetricInputToken, WindowKind: billing.WindowKindRolling, Period: billing.Period5h,
				LimitAmount: decimal.RequireFromString("100"),
			}},
		}}, nil
	}
	s := New(f, WithClock(func() time.Time { return planFixedNow }))
	views, err := s.ListPlans(context.Background())
	if err != nil {
		t.Fatalf("列出套餐失败：%v", err)
	}
	if views[0].LastCheckedAt != nil {
		t.Errorf("从未采集时应为 nil，得到 %v", views[0].LastCheckedAt)
	}
	quota := views[0].Quotas[0]
	if quota.LastCheckedAt != nil {
		t.Errorf("从未采集时限额行采集时刻应为 nil，得到 %v", quota.LastCheckedAt)
	}
	if quota.UsedPercent == nil || *quota.UsedPercent != "0" {
		t.Errorf("已用量为零时百分比应为 0，得到 %v", quota.UsedPercent)
	}
	// rolling 窗口没有固定边界，ResetAt 取一个完整周期作为保守估计，与下游限额展示同口径。
	if quota.ResetsAt == nil || !quota.ResetsAt.Equal(planFixedNow.Add(5*time.Hour)) {
		t.Errorf("rolling×5h 重置时刻 = %v，期望 %s", quota.ResetsAt, planFixedNow.Add(5*time.Hour))
	}
}
