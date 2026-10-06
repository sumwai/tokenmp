package main

import (
	"testing"

	"github.com/sumwai/tokenmp/internal/billing"
)

func TestParsePlanQuotaArg(t *testing.T) {
	tests := []struct {
		name       string
		give       string
		wantMetric billing.Metric
		wantKind   billing.WindowKind
		wantPeriod billing.Period
		wantErr    bool
	}{
		{
			name: "显式窗口类型与周期", give: "input_token:rolling/5h:1000000",
			wantMetric: billing.MetricInputToken, wantKind: billing.WindowKindRolling, wantPeriod: billing.Period5h,
		},
		{
			name: "只写周期由周期推出窗口类型", give: "request:day:5000",
			wantMetric: billing.MetricRequest, wantKind: billing.WindowKindCalendar, wantPeriod: billing.PeriodDay,
		},
		{
			name: "5h 推出滚动窗口", give: "input_token:5h:100",
			wantMetric: billing.MetricInputToken, wantKind: billing.WindowKindRolling, wantPeriod: billing.Period5h,
		},
		{name: "分段数不足", give: "input_token:day", wantErr: true},
		{name: "分段数过多", give: "input_token:day:1:2", wantErr: true},
		{name: "未知指标", give: "not_a_metric:day:1", wantErr: true},
		{name: "窗口组合不可判定", give: "request:rolling/day:1", wantErr: true},
		{name: "限额为空", give: "request:day:", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePlanQuotaArg(tt.give)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("应当被拒绝，得到 %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if got.Metric != tt.wantMetric || got.WindowKind != tt.wantKind || got.Period != tt.wantPeriod {
				t.Errorf("解析结果 = %s/%s/%s，期望 %s/%s/%s",
					got.Metric, got.WindowKind, got.Period, tt.wantMetric, tt.wantKind, tt.wantPeriod)
			}
		})
	}
}

func TestParsePlanQuotaArgsRequiresAtLeastOne(t *testing.T) {
	if _, err := parsePlanQuotaArgs(nil); err == nil {
		t.Fatal("没有 --quota 应当报错")
	}
}
