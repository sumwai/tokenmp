package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件覆盖订单口径的纯计算：折算率与购买派生存量的算式，不经数据库。

// TestDeriveUnitRate 断言折算率 = 售价 / 每份数量，位数取列标度，
// 且与购买份数无关（多份购买的 price_paid / total 约简后等于单份比值）。
func TestDeriveUnitRate(t *testing.T) {
	tests := []struct {
		name  string
		price string
		qty   string
		want  string
	}{
		{name: "整数比", price: "10", qty: "100000000", want: "0.0000001"},
		{name: "整除", price: "20", qty: "10", want: "2"},
		{name: "除不尽按标度四舍五入", price: "1", qty: "3", want: "0.33333333"},
		{name: "大数不丢精度", price: "9007199254740993", qty: "1", want: "9007199254740993"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			price, err := decimal.NewFromString(tt.price)
			if err != nil {
				t.Fatalf("解析售价：%v", err)
			}
			qty, err := decimal.NewFromString(tt.qty)
			if err != nil {
				t.Fatalf("解析数量：%v", err)
			}
			if got := DeriveUnitRate(price, qty); got != tt.want {
				t.Errorf("DeriveUnitRate(%s, %s) = %q，期望 %q", tt.price, tt.qty, got, tt.want)
			}
		})
	}
}

// TestOrderRowFrom 断言订单展示口径：存量 = 每份数量 × 份数，
// 折算率取自档位，金额与数量保持十进制字符串。
func TestOrderRowFrom(t *testing.T) {
	product := Product{
		ID: 9, MerchantID: 1, Name: "10 元 100M token", Unit: billing.UnitSettleToken,
		Qty: "100000000", Price: "10",
	}
	row, err := OrderRowFrom(Purchase{ID: 77, ProductID: 9, Qty: "2", PricePaid: "20"}, product)
	if err != nil {
		t.Fatalf("折算订单失败：%v", err)
	}
	if row.Total != "200000000" {
		t.Errorf("派生存量 = %q，期望 200000000", row.Total)
	}
	if row.UnitRate != "0.0000001" {
		t.Errorf("折算率 = %q，期望 0.0000001", row.UnitRate)
	}
	if row.ProductName != "10 元 100M token" || row.Unit != billing.UnitSettleToken {
		t.Errorf("档位口径未透传：%+v", row)
	}
}

// TestOrderRowFromNormalizesDBScale 断言读回的定点文本被归一到 decimal.String() 的形态：
// DECIMAL 列读回带列标度（"2.00000000"），契约承诺的十进制字符串不带这些尾零。
func TestOrderRowFromNormalizesDBScale(t *testing.T) {
	row, err := OrderRowFrom(
		Purchase{ID: 77, Qty: "2.00000000", PricePaid: "20.00000000"},
		Product{ID: 9, Qty: "100000000.00000000", Price: "10.00000000"},
	)
	if err != nil {
		t.Fatalf("折算订单失败：%v", err)
	}
	if row.Qty != "2" || row.PricePaid != "20" {
		t.Errorf("份数 / 实付 = %q / %q，期望 2 / 20", row.Qty, row.PricePaid)
	}
	if row.Total != "200000000" {
		t.Errorf("派生存量 = %q，期望 200000000", row.Total)
	}
	if row.UnitRate != "0.0000001" {
		t.Errorf("折算率 = %q，期望 0.0000001", row.UnitRate)
	}
}

// TestOrderRowFromRejectsBadAmounts 断言档位金额无法解析时报错而不是给一个近似的数。
func TestOrderRowFromRejectsBadAmounts(t *testing.T) {
	_, err := OrderRowFrom(Purchase{ID: 77, Qty: "1"}, Product{ID: 9, Qty: "abc", Price: "1"})
	if err == nil {
		t.Fatal("档位数量非法时应报错")
	}
}
