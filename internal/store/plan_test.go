package store

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/plan"
)

func TestEncodeChannelConfig(t *testing.T) {
	tests := []struct {
		name    string
		give    string
		wantNil bool
		wantErr string
	}{
		{name: "空配置写 NULL", give: "", wantNil: true},
		{name: "空白配置写 NULL", give: "   ", wantNil: true},
		{name: "JSON null 写 NULL", give: "null", wantNil: true},
		{name: "JSON 对象原样落库", give: `{"probe":{"url":"https://up"}}`},
		{name: "数组不是对象", give: `[1,2]`, wantErr: "JSON 对象"},
		{name: "裸字符串不是对象", give: `"probe"`, wantErr: "JSON 对象"},
		{name: "非法 JSON", give: `{`, wantErr: "JSON 对象"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := encodeChannelConfig([]byte(tt.give))
			switch {
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("错误 = %v，期望包含 %q", err, tt.wantErr)
				}
			case tt.wantNil:
				if err != nil {
					t.Fatalf("意外错误：%v", err)
				}
				if got != nil {
					t.Fatalf("期望 NULL，得到 %#v", got)
				}
			default:
				if err != nil {
					t.Fatalf("意外错误：%v", err)
				}
				raw, ok := got.([]byte)
				if !ok || string(raw) != tt.give {
					t.Fatalf("配置 = %#v，期望 %#v", got, tt.give)
				}
			}
		})
	}
}

func TestValidateUpstreamPlan(t *testing.T) {
	valid := plan.UpstreamPlan{MerchantID: 1, CredGroup: "g", Name: "n", Multiplier: decimal.NewFromInt(1)}
	if err := validateUpstreamPlan(valid); err != nil {
		t.Fatalf("合法套餐不应报错：%v", err)
	}
	tests := []struct {
		name string
		give plan.UpstreamPlan
	}{
		{name: "商家为零", give: plan.UpstreamPlan{CredGroup: "g", Name: "n", Multiplier: decimal.NewFromInt(1)}},
		{name: "凭据分组为空", give: plan.UpstreamPlan{MerchantID: 1, Name: "n", Multiplier: decimal.NewFromInt(1)}},
		{name: "套餐名为空", give: plan.UpstreamPlan{MerchantID: 1, CredGroup: "g", Multiplier: decimal.NewFromInt(1)}},
		{name: "倍率为零", give: plan.UpstreamPlan{MerchantID: 1, CredGroup: "g", Name: "n"}},
		{name: "倍率为负", give: plan.UpstreamPlan{MerchantID: 1, CredGroup: "g", Name: "n", Multiplier: decimal.NewFromInt(-1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateUpstreamPlan(tt.give); err == nil {
				t.Fatal("应当被拒绝")
			}
		})
	}
}

func TestValidatePlanQuota(t *testing.T) {
	valid := plan.Quota{
		Metric: billing.MetricInputToken, WindowKind: billing.WindowKindRolling,
		Period: billing.Period5h, LimitAmount: decimal.NewFromInt(100),
	}
	if err := validatePlanQuota(valid); err != nil {
		t.Fatalf("合法限额不应报错：%v", err)
	}
	tests := []struct {
		name string
		give plan.Quota
	}{
		{name: "未知指标", give: plan.Quota{Metric: "nope", WindowKind: billing.WindowKindRolling, Period: billing.Period5h, LimitAmount: decimal.NewFromInt(1)}},
		{name: "窗口组合不可判定", give: plan.Quota{Metric: billing.MetricInputToken, WindowKind: billing.WindowKindCalendar, Period: billing.Period5h, LimitAmount: decimal.NewFromInt(1)}},
		{name: "限额为零", give: plan.Quota{Metric: billing.MetricInputToken, WindowKind: billing.WindowKindRolling, Period: billing.Period5h}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validatePlanQuota(tt.give); err == nil {
				t.Fatal("应当被拒绝")
			}
		})
	}
}
