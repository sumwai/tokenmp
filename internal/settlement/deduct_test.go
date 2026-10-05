package settlement

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// now 是扣减测试的判定时刻。
var now = time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)

func at(offset time.Duration) *time.Time {
	t := now.Add(offset)
	return &t
}

func TestPlanDeductionOrder(t *testing.T) {
	// 先过期者先扣；同到期取 priority 小者；再同取 id 小者；不过期的排最后。
	buckets := []Bucket{
		{ID: 3, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "10"), ExpiresAt: at(2 * time.Hour), Priority: 100},
		{ID: 1, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "10"), ExpiresAt: at(time.Hour), Priority: 100},
		{ID: 2, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "10"), ExpiresAt: at(time.Hour), Priority: 50},
		{ID: 4, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "10")},
	}
	needs := []Charge{{Unit: billing.UnitSettleCurrency, Qty: dec(t, "25")}}
	plan := PlanDeduction(needs, buckets, now)

	wantIDs := []uint64{2, 1, 3}
	if len(plan.Lines) != len(wantIDs) {
		t.Fatalf("扣减行数 = %d，期望 %d：%#v", len(plan.Lines), len(wantIDs), plan.Lines)
	}
	for i, want := range wantIDs {
		if plan.Lines[i].BucketID != want {
			t.Errorf("第 %d 行 bucket id = %d，期望 %d", i+1, plan.Lines[i].BucketID, want)
		}
	}
	if got := plan.Updates[2]; got.String() != "0" {
		t.Errorf("bucket 2 余量 = %s，期望 0", got)
	}
	if got := plan.Updates[1]; got.String() != "0" {
		t.Errorf("bucket 1 余量 = %s，期望 0", got)
	}
	if got := plan.Updates[3]; got.String() != "5" {
		t.Errorf("bucket 3 余量 = %s，期望 5", got)
	}
	if _, ok := plan.Updates[4]; ok {
		t.Errorf("未触达的 bucket 4 不应出现在更新里")
	}
	if len(plan.Shortfall) != 0 {
		t.Errorf("额度足够时不应有欠额：%#v", plan.Shortfall)
	}
}

func TestPlanDeductionSkipsExpired(t *testing.T) {
	buckets := []Bucket{
		{ID: 1, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "10"), ExpiresAt: at(-time.Minute)},
	}
	plan := PlanDeduction([]Charge{{Unit: billing.UnitSettleCurrency, Qty: dec(t, "5")}}, buckets, now)
	if len(plan.Lines) != 0 {
		t.Errorf("已过期的账本不应被扣：%#v", plan.Lines)
	}
	if plan.Shortfall[billing.UnitSettleCurrency].String() != "5" {
		t.Errorf("过期包不构成可用额度，应记欠额 5")
	}
}

func TestPlanDeductionChargeBalance(t *testing.T) {
	buckets := []Bucket{
		{ID: 1, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "1"), Fallback: billing.FallbackChargeBalance},
	}
	plan := PlanDeduction([]Charge{{Unit: billing.UnitSettleCurrency, Qty: dec(t, "5")}}, buckets, now)
	if len(plan.Lines) != 2 {
		t.Fatalf("应产生扣净与透支行两行：%#v", plan.Lines)
	}
	if got := plan.Updates[1]; got.String() != "-4" {
		t.Errorf("余额 = %s，期望 -4（允许透支）", got)
	}
	if len(plan.Shortfall) != 0 {
		t.Errorf("charge_balance 不应记欠额：%#v", plan.Shortfall)
	}
}

func TestPlanDeductionRejectShortfall(t *testing.T) {
	buckets := []Bucket{
		{ID: 1, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "1"), Fallback: billing.FallbackReject},
	}
	plan := PlanDeduction([]Charge{{Unit: billing.UnitSettleCurrency, Qty: dec(t, "5")}}, buckets, now)
	if got := plan.Updates[1]; got.String() != "0" {
		t.Errorf("余量 = %s，期望 0（只扣到 0）", got)
	}
	if plan.Shortfall[billing.UnitSettleCurrency].String() != "4" {
		t.Errorf("欠额 = %#v，期望 4", plan.Shortfall)
	}
}

func TestPlanDeductionMultipleUnits(t *testing.T) {
	buckets := []Bucket{
		{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "1000"), Fallback: billing.FallbackChargeBalance},
		{ID: 2, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "0.5"), Fallback: billing.FallbackReject},
	}
	needs := []Charge{
		{Unit: billing.UnitSettleToken, Qty: dec(t, "800")},
		{Unit: billing.UnitSettleCurrency, Qty: dec(t, "0.2")},
	}
	plan := PlanDeduction(needs, buckets, now)
	if len(plan.Lines) != 2 {
		t.Fatalf("两个单位各一行：%#v", plan.Lines)
	}
	if plan.Updates[1].String() != "200" {
		t.Errorf("token 余量 = %s，期望 200", plan.Updates[1])
	}
	if plan.Updates[2].String() != "0.3" {
		t.Errorf("currency 余量 = %s，期望 0.3", plan.Updates[2])
	}
	if len(plan.Shortfall) != 0 {
		t.Errorf("额度足够时不应有欠额：%#v", plan.Shortfall)
	}
}

func TestNeedAfterMultiplier(t *testing.T) {
	charges := []Charge{
		{Unit: billing.UnitSettleCurrency, Qty: dec(t, "0.27")},
		{Unit: billing.UnitSettleToken, Qty: dec(t, "1000")},
	}
	got := NeedAfterMultiplier(charges, decimal.RequireFromString("1.5"))
	if got[0].Qty.String() != "0.405" {
		t.Errorf("currency 扣减量 = %s，期望 0.405", got[0].Qty)
	}
	if got[1].Qty.String() != "1500" {
		t.Errorf("token 扣减量 = %s，期望 1500", got[1].Qty)
	}
}

// TestPlanDeductionConvertsOverflowByUnitRate 覆盖非货币包扣尽后按锁定的折算率
// 把差额折成 currency：转换行带折算率，currency 钱包被扣成负数。
func TestPlanDeductionConvertsOverflowByUnitRate(t *testing.T) {
	buckets := []Bucket{
		{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "100"),
			Fallback: billing.FallbackChargeBalance, UnitRate: dec(t, "0.1")},
		{ID: 2, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "0"),
			Fallback: billing.FallbackChargeBalance},
	}
	plan := PlanDeduction([]Charge{{Unit: billing.UnitSettleToken, Qty: dec(t, "300")}}, buckets, now)

	if len(plan.Lines) != 2 {
		t.Fatalf("应产生 token 扣净与折算 currency 两行：%#v", plan.Lines)
	}
	if got := plan.Lines[0]; got.BucketID != 1 || got.Unit != billing.UnitSettleToken ||
		got.Qty.String() != "100" || got.Rate.IsPositive() {
		t.Errorf("第 1 行应为 token 直接扣减且无折算率：%#v", got)
	}
	if got := plan.Lines[1]; got.BucketID != 2 || got.Unit != billing.UnitSettleCurrency ||
		got.Qty.String() != "20" || got.Rate.String() != "0.1" {
		t.Errorf("第 2 行应为 200 × 0.1 = 20 currency 且带折算率：%#v", got)
	}
	if plan.Updates[1].String() != "0" {
		t.Errorf("token 余量 = %s，期望 0", plan.Updates[1])
	}
	if plan.Updates[2].String() != "-20" {
		t.Errorf("currency 余量 = %s，期望 -20", plan.Updates[2])
	}
	if len(plan.Shortfall) != 0 {
		t.Errorf("转换成功后不应有欠额：%#v", plan.Shortfall)
	}
}

// TestPlanDeductionMissingUnitRateFallsBackToShortfall 覆盖折算率为零值（对应
// NULL）时退化为记欠额：不按 1:1 把 200 token 记成 200 currency。
func TestPlanDeductionMissingUnitRateFallsBackToShortfall(t *testing.T) {
	buckets := []Bucket{
		{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "100"),
			Fallback: billing.FallbackChargeBalance},
		{ID: 2, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "0"),
			Fallback: billing.FallbackChargeBalance},
	}
	plan := PlanDeduction([]Charge{{Unit: billing.UnitSettleToken, Qty: dec(t, "300")}}, buckets, now)

	if len(plan.Lines) != 1 {
		t.Fatalf("无折算率时不应产生 currency 行：%#v", plan.Lines)
	}
	if plan.Shortfall[billing.UnitSettleToken].String() != "200" {
		t.Errorf("欠额 = %#v，期望 token=200", plan.Shortfall)
	}
	if _, ok := plan.Updates[2]; ok {
		t.Errorf("无折算率时 currency 钱包不应被触达：%#v", plan.Updates)
	}
}

// TestPlanDeductionRejectIgnoresUnitRate 覆盖 fallback=reject 的包：
// 即使带折算率也不做跨单位转换，差额记欠额。
func TestPlanDeductionRejectIgnoresUnitRate(t *testing.T) {
	buckets := []Bucket{
		{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "100"),
			Fallback: billing.FallbackReject, UnitRate: dec(t, "0.1")},
		{ID: 2, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "0"),
			Fallback: billing.FallbackChargeBalance},
	}
	plan := PlanDeduction([]Charge{{Unit: billing.UnitSettleToken, Qty: dec(t, "300")}}, buckets, now)

	if len(plan.Lines) != 1 {
		t.Fatalf("reject 包不应产生折算行：%#v", plan.Lines)
	}
	if plan.Shortfall[billing.UnitSettleToken].String() != "200" {
		t.Errorf("欠额 = %#v，期望 token=200", plan.Shortfall)
	}
}

// TestPlanDeductionConvertedOverflowFillsCurrencyInOrder 覆盖混合账本扣减序：
// 折算出的 currency 先扣正余量的 currency 包，余量不足的部分才挂到后付钱包。
func TestPlanDeductionConvertedOverflowFillsCurrencyInOrder(t *testing.T) {
	buckets := []Bucket{
		{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "100"),
			Fallback: billing.FallbackChargeBalance, UnitRate: dec(t, "0.1")},
		{ID: 2, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "5"),
			Fallback: billing.FallbackReject, Priority: 50},
		{ID: 3, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "0"),
			Fallback: billing.FallbackChargeBalance, Priority: 100},
	}
	plan := PlanDeduction([]Charge{{Unit: billing.UnitSettleToken, Qty: dec(t, "300")}}, buckets, now)

	if len(plan.Lines) != 3 {
		t.Fatalf("应为 token + currency 包 + currency 钱包三行：%#v", plan.Lines)
	}
	if got := plan.Lines[1]; got.BucketID != 2 || got.Qty.String() != "5" || got.Rate.String() != "0.1" {
		t.Errorf("第 2 行应为 currency 包扣 5 且带折算率：%#v", got)
	}
	if got := plan.Lines[2]; got.BucketID != 3 || got.Qty.String() != "15" || got.Rate.String() != "0.1" {
		t.Errorf("第 3 行应为后付钱包扣 15 且带折算率：%#v", got)
	}
	if plan.Updates[2].String() != "0" {
		t.Errorf("currency 包余量 = %s，期望 0", plan.Updates[2])
	}
	if plan.Updates[3].String() != "-15" {
		t.Errorf("currency 钱包余量 = %s，期望 -15", plan.Updates[3])
	}
	if len(plan.Shortfall) != 0 {
		t.Errorf("转换成功后不应有欠额：%#v", plan.Shortfall)
	}
}

// TestPlanDeductionRoundsConvertedAmount 覆盖折算结果按金额标度 8 位四舍五入。
func TestPlanDeductionRoundsConvertedAmount(t *testing.T) {
	buckets := []Bucket{
		{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "0"),
			Fallback: billing.FallbackChargeBalance, UnitRate: dec(t, "0.123456789")},
		{ID: 2, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "0"),
			Fallback: billing.FallbackChargeBalance},
	}
	plan := PlanDeduction([]Charge{{Unit: billing.UnitSettleToken, Qty: dec(t, "1")}}, buckets, now)

	if len(plan.Lines) != 1 {
		t.Fatalf("应产生一行折算扣减：%#v", plan.Lines)
	}
	if got := plan.Lines[0].Qty.String(); got != "0.12345679" {
		t.Errorf("折算量 = %s，期望 0.12345679（8 位四舍五入）", got)
	}
	if plan.Updates[2].String() != "-0.12345679" {
		t.Errorf("currency 余量 = %s，期望 -0.12345679", plan.Updates[2])
	}
}

// TestPlanDeductionConvertedOverflowWithoutSink 覆盖无 currency 账本可承接时，
// 折算后的差额记为 currency 欠额：折算率已知，欠额以货币口径留痕。
func TestPlanDeductionConvertedOverflowWithoutSink(t *testing.T) {
	buckets := []Bucket{
		{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "100"),
			Fallback: billing.FallbackChargeBalance, UnitRate: dec(t, "0.1")},
	}
	plan := PlanDeduction([]Charge{{Unit: billing.UnitSettleToken, Qty: dec(t, "300")}}, buckets, now)

	if len(plan.Lines) != 1 {
		t.Fatalf("无 currency 账本时只应有 token 一行：%#v", plan.Lines)
	}
	if plan.Shortfall[billing.UnitSettleCurrency].String() != "20" {
		t.Errorf("欠额 = %#v，期望 currency=20", plan.Shortfall)
	}
	if _, ok := plan.Shortfall[billing.UnitSettleToken]; ok {
		t.Errorf("已折算的差额不应再记 token 欠额：%#v", plan.Shortfall)
	}
}
