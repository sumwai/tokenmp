package plan

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// planTestNow 是判定用例的固定当前时刻。
var planTestNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// quotaOf 构造一条限额行；lastUsed 与 limit 用字符串给出，避免测试里出现浮点字面量。
func quotaOf(metric billing.Metric, lastUsed, limit string) Quota {
	return Quota{
		ID:          1,
		Metric:      metric,
		WindowKind:  billing.WindowKindRolling,
		Period:      billing.Period5h,
		LimitAmount: decimal.RequireFromString(limit),
		LastUsed:    decimal.RequireFromString(lastUsed),
	}
}

func TestExhausted(t *testing.T) {
	fresh := planTestNow.Add(-time.Minute)
	stale := planTestNow.Add(-time.Hour)
	maxAge := 10 * time.Minute

	tests := []struct {
		name   string
		give   UpstreamPlan
		maxAge time.Duration
		want   bool
	}{
		{
			name: "全部限额用满且快照新鲜即耗尽",
			give: UpstreamPlan{CredGroup: "g", LastCheckedAt: fresh,
				Quotas: []Quota{quotaOf(billing.MetricInputToken, "100", "100")}},
			maxAge: maxAge,
			want:   true,
		},
		{
			name: "已用量超过限额同样算耗尽",
			give: UpstreamPlan{CredGroup: "g", LastCheckedAt: fresh,
				Quotas: []Quota{quotaOf(billing.MetricInputToken, "120", "100")}},
			maxAge: maxAge,
			want:   true,
		},
		{
			name: "有一条未用满即未耗尽",
			give: UpstreamPlan{CredGroup: "g", LastCheckedAt: fresh, Quotas: []Quota{
				quotaOf(billing.MetricInputToken, "100", "100"),
				quotaOf(billing.MetricOutputToken, "99", "100"),
			}},
			maxAge: maxAge,
			want:   false,
		},
		{
			name: "快照过期按未知放行",
			give: UpstreamPlan{CredGroup: "g", LastCheckedAt: stale,
				Quotas: []Quota{quotaOf(billing.MetricInputToken, "100", "100")}},
			maxAge: maxAge,
			want:   false,
		},
		{
			name: "从未采集视为未知",
			give: UpstreamPlan{CredGroup: "g",
				Quotas: []Quota{quotaOf(billing.MetricInputToken, "100", "100")}},
			maxAge: maxAge,
			want:   false,
		},
		{
			name:   "没有限额行无从判定",
			give:   UpstreamPlan{CredGroup: "g", LastCheckedAt: fresh},
			maxAge: maxAge,
			want:   false,
		},
		{
			name: "maxAge 为零表示不做过期检查",
			give: UpstreamPlan{CredGroup: "g", LastCheckedAt: stale,
				Quotas: []Quota{quotaOf(billing.MetricInputToken, "100", "100")}},
			maxAge: 0,
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Exhausted(tt.give, planTestNow, tt.maxAge); got != tt.want {
				t.Errorf("Exhausted() = %t，期望 %t", got, tt.want)
			}
		})
	}
}

func TestExhaustedCredGroups(t *testing.T) {
	fresh := planTestNow.Add(-time.Minute)
	stale := planTestNow.Add(-time.Hour)
	plans := []UpstreamPlan{
		{CredGroup: "full", LastCheckedAt: fresh, Quotas: []Quota{quotaOf(billing.MetricInputToken, "10", "10")}},
		{CredGroup: "partial", LastCheckedAt: fresh, Quotas: []Quota{quotaOf(billing.MetricInputToken, "1", "10")}},
		{CredGroup: "stale", LastCheckedAt: stale, Quotas: []Quota{quotaOf(billing.MetricInputToken, "10", "10")}},
		// 空 cred_group 不入集合：候选按 cred_group 命中，空值会误伤所有未分组的渠道。
		{CredGroup: "", LastCheckedAt: fresh, Quotas: []Quota{quotaOf(billing.MetricInputToken, "10", "10")}},
	}
	got := ExhaustedCredGroups(plans, planTestNow, 10*time.Minute)
	if _, ok := got["full"]; !ok {
		t.Errorf("full 应当被判为耗尽：%v", got)
	}
	for _, name := range []string{"partial", "stale", ""} {
		if _, ok := got[name]; ok {
			t.Errorf("%q 不应被判为耗尽：%v", name, got)
		}
	}
	if len(got) != 1 {
		t.Errorf("耗尽集合大小 = %d，期望 1：%v", len(got), got)
	}
}
