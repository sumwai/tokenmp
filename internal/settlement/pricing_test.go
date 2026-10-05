package settlement

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// dec 是测试里构造小数的简写；字面量写错时直接失败而不是留下零值。
func dec(t *testing.T, raw string) decimal.Decimal {
	t.Helper()
	value, err := decimal.NewFromString(raw)
	if err != nil {
		t.Fatalf("构造小数 %q 失败：%v", raw, err)
	}
	return value
}

func TestResolveChargesGroupsByUnit(t *testing.T) {
	usage := map[billing.Metric]int{
		billing.MetricInputToken:  1_000_000,
		billing.MetricOutputToken: 500_000,
	}
	components := []Component{
		{ID: 1, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
			UnitPrice: dec(t, "0.27"), BasisQty: dec(t, "1000000")},
		{ID: 2, Metric: billing.MetricOutputToken, UnitSettle: billing.UnitSettleCurrency,
			UnitPrice: dec(t, "1.10"), BasisQty: dec(t, "1000000")},
		{ID: 3, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleToken,
			UnitPrice: dec(t, "1"), BasisQty: dec(t, "1")},
	}

	charges, hit := ResolveCharges(usage, components)
	if len(charges) != 2 {
		t.Fatalf("结算单位数 = %d，期望 2：%#v", len(charges), charges)
	}
	// currency = 0.27 + 0.55 = 0.82；token = 1_000_000。
	if charges[0].Unit != billing.UnitSettleCurrency || charges[0].Qty.String() != "0.82" {
		t.Errorf("第 1 组 = %s/%s，期望 currency/0.82", charges[0].Unit, charges[0].Qty)
	}
	if charges[1].Unit != billing.UnitSettleToken || charges[1].Qty.String() != "1000000" {
		t.Errorf("第 2 组 = %s/%s，期望 token/1000000", charges[1].Unit, charges[1].Qty)
	}
	if len(hit) != 3 {
		t.Errorf("命中分量数 = %d，期望 3", len(hit))
	}
	if got := GrossAmount(charges); got.String() != "1000000.82" {
		t.Errorf("基础金额 = %s，期望 1000000.82", got)
	}
}

func TestResolveChargesSkipsUncoveredMetrics(t *testing.T) {
	// 用量里有 input，分量表只覆盖 output：不产生任何费用，也不命中分量。
	usage := map[billing.Metric]int{billing.MetricInputToken: 10}
	components := []Component{{
		ID: 1, Metric: billing.MetricOutputToken, UnitSettle: billing.UnitSettleCurrency,
		UnitPrice: dec(t, "1"), BasisQty: dec(t, "1"),
	}}
	charges, hit := ResolveCharges(usage, components)
	if len(charges) != 0 || len(hit) != 0 {
		t.Fatalf("未覆盖的 metric 不应产生费用：charges=%#v hit=%#v", charges, hit)
	}
}

func TestResolveChargesSkipsZeroUsage(t *testing.T) {
	usage := map[billing.Metric]int{billing.MetricInputToken: 0}
	components := []Component{{
		ID: 1, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
		UnitPrice: dec(t, "1"), BasisQty: dec(t, "1"),
	}}
	charges, hit := ResolveCharges(usage, components)
	if len(charges) != 0 || len(hit) != 0 {
		t.Fatalf("零用量不应产生费用：charges=%#v hit=%#v", charges, hit)
	}
}

func TestResolveChargesTierByRequestInput(t *testing.T) {
	components := []Component{
		{ID: 1, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
			UnitPrice: dec(t, "1"), BasisQty: dec(t, "1"),
			TierFrom: "0", TierTo: "1000", TierBasis: tierBasisRequestInput},
		{ID: 2, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
			UnitPrice: dec(t, "2"), BasisQty: dec(t, "1"),
			TierFrom: "1000", TierBasis: tierBasisRequestInput},
	}
	tests := []struct {
		name     string
		input    int
		wantQty  string
		wantHits int
	}{
		{name: "下界含", input: 999, wantQty: "999", wantHits: 1},
		{name: "上界不含", input: 1000, wantQty: "2000", wantHits: 1},
		{name: "无上界档", input: 5000, wantQty: "10000", wantHits: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := map[billing.Metric]int{billing.MetricInputToken: tt.input}
			charges, hit := ResolveCharges(usage, components)
			if len(charges) != 1 || charges[0].Qty.String() != tt.wantQty {
				t.Fatalf("charges = %#v，期望单组 %s", charges, tt.wantQty)
			}
			if len(hit) != tt.wantHits {
				t.Errorf("命中分量数 = %d，期望 %d", len(hit), tt.wantHits)
			}
		})
	}
}

func TestResolveChargesSkipsZeroBasis(t *testing.T) {
	usage := map[billing.Metric]int{billing.MetricInputToken: 10}
	components := []Component{{
		ID: 1, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
		UnitPrice: dec(t, "1"), BasisQty: decimal.Zero,
	}}
	charges, hit := ResolveCharges(usage, components)
	if len(charges) != 0 || len(hit) != 0 {
		t.Fatalf("基准量为 0 的分量应被跳过：charges=%#v hit=%#v", charges, hit)
	}
}
