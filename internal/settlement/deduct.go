package settlement

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件是账本扣减的唯一实现：按单位匹配、先过期先扣，包扣尽后按 fallback 处理。
//
// 扣减序：未过期、remaining > 0 的账本，按（expires_at 最近优先、priority、id）
// 依次扣。不过期的包（expires_at IS NULL）排在所有会过期的包之后：会过期的存量
// 先用掉才不会浪费。
//
// 包扣尽后的差额：currency 直接挂到可透支的钱包；非货币包在 fallback=charge_balance
// 且带折算率时先折成 currency，再走同一路径，否则记欠额（差额不按 1:1 记账）。

// PlanDeduction 计算一次结算的扣减明细与各账本新存量。
//
// needs 是乘以倍率后的各结算单位应扣量；buckets 是该账户的全部账本行。
// now 用于过期判定，由调用方传入以保持结果可复现。
func PlanDeduction(needs []Charge, buckets []Bucket, now time.Time) DeductionPlan {
	plan := newPlan()
	live := make([]Bucket, 0, len(buckets))
	for _, bucket := range buckets {
		if bucket.ExpiresAt != nil && !bucket.ExpiresAt.After(now) {
			continue
		}
		live = append(live, bucket)
	}
	sortBuckets(live)

	remaining := make(map[uint64]decimal.Decimal, len(live))
	for _, bucket := range live {
		remaining[bucket.ID] = bucket.Remaining
	}
	byUnit := make(map[billing.UnitSettle][]Bucket)
	for _, bucket := range live {
		byUnit[bucket.Unit] = append(byUnit[bucket.Unit], bucket)
	}

	ordered := append([]Charge(nil), needs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Unit < ordered[j].Unit })
	currencyGroup := byUnit[billing.UnitSettleCurrency]
	for _, charge := range ordered {
		deductUnit(&plan, charge, byUnit[charge.Unit], currencyGroup, remaining)
	}
	return plan
}

// deductUnit 扣减一个结算单位的应扣量。
//
// currencyGroup 是本账户的全部 currency 账本：非货币包扣尽后若带折算率，
// 差额折成 currency 后要落到它上面，因此本函数需要跨单位可见。
func deductUnit(plan *DeductionPlan, charge Charge, group, currencyGroup []Bucket, remaining map[uint64]decimal.Decimal) {
	need := charge.Qty
	if !need.IsPositive() {
		return
	}
	need = fillBuckets(plan, charge.Unit, need, group, remaining, decimal.Zero)
	if !need.IsPositive() {
		return
	}
	if charge.Unit == billing.UnitSettleCurrency {
		// currency 账本允许透支：后付钱包是最后的兜底，包扣尽后差额挂在
		// fallback=charge_balance 的 currency 包上记负数（后付透支）。
		chargeCurrency(plan, need, currencyGroup, remaining, decimal.Zero)
		return
	}
	// 非货币差额按包锁定的折算率折成 currency 继续扣：折算率在购买时刻锁定，
	// 与定价快照同一「按历史事实复算」原则。折算率为 0 / NULL 时同义，
	// 退化为记欠额（与不转换的现状一致）。
	if rate := overflowRate(group); rate.IsPositive() {
		chargeCurrency(plan, round(need.Mul(rate), settleScale), currencyGroup, remaining, rate)
		return
	}
	plan.Shortfall[charge.Unit] = round(plan.Shortfall[charge.Unit].Add(need), settleScale)
}

// fillBuckets 按扣减序扣减正余量的账本，返回仍未满足的量。
//
// rate 原样写到每个明细行上：currency 转换产生的扣减带折算率，
// 原始单位扣减传零值。
func fillBuckets(plan *DeductionPlan, unit billing.UnitSettle, need decimal.Decimal,
	group []Bucket, remaining map[uint64]decimal.Decimal, rate decimal.Decimal) decimal.Decimal {
	for _, bucket := range group {
		if !need.IsPositive() {
			break
		}
		available := remaining[bucket.ID]
		if !available.IsPositive() {
			continue
		}
		take := available
		if need.LessThan(available) {
			take = need
		}
		plan.Lines = append(plan.Lines, DeductionLine{
			BucketID: bucket.ID, Unit: unit, Qty: round(take, settleScale), Rate: rate,
		})
		remaining[bucket.ID] = available.Sub(take)
		plan.Updates[bucket.ID] = round(remaining[bucket.ID], settleScale)
		need = need.Sub(take)
	}
	return need
}

// chargeCurrency 把 need 记到 currency 账本上：先按扣减序扣正余量，
// 仍不足时挂到可透支的钱包（fallback=charge_balance），无钱包则记欠额。
//
// rate 是产生这笔 currency 的折算率，原始 currency 扣减传零值。
func chargeCurrency(plan *DeductionPlan, need decimal.Decimal,
	group []Bucket, remaining map[uint64]decimal.Decimal, rate decimal.Decimal) {
	need = fillBuckets(plan, billing.UnitSettleCurrency, need, group, remaining, rate)
	if !need.IsPositive() {
		return
	}
	if sink, ok := negativeSink(group); ok {
		plan.Lines = append(plan.Lines, DeductionLine{
			BucketID: sink.ID, Unit: billing.UnitSettleCurrency, Qty: round(need, settleScale), Rate: rate,
		})
		remaining[sink.ID] = remaining[sink.ID].Sub(need)
		plan.Updates[sink.ID] = round(remaining[sink.ID], settleScale)
		return
	}
	plan.Shortfall[billing.UnitSettleCurrency] = round(plan.Shortfall[billing.UnitSettleCurrency].Add(need), settleScale)
}

// overflowRate 返回非货币包扣尽后差额的折算率。
//
// 取扣减序里最后一个 fallback=charge_balance 且带正折算率的包：差额发生在整组包
// 扣尽之后，兜底语义由扣减序里最靠后的后付包承担，与 negativeSink 取最后一个同源。
// 多个后付包折算率不同时，结果由扣减序唯一确定，不依赖遍历顺序。
func overflowRate(group []Bucket) decimal.Decimal {
	for i := len(group) - 1; i >= 0; i-- {
		if group[i].Fallback == billing.FallbackChargeBalance && group[i].UnitRate.IsPositive() {
			return group[i].UnitRate
		}
	}
	return decimal.Zero
}

// negativeSink 返回可以透支的 currency 账本：扣减序里最后一个 fallback=charge_balance 的包。
//
// 取最后一个而不是第一个：会过期的、priority 更小的包先被扣干，
// 无限额的货币钱包排在最后，把它记成负数最符合「后付」的语义。
func negativeSink(group []Bucket) (Bucket, bool) {
	for i := len(group) - 1; i >= 0; i-- {
		if group[i].Fallback == billing.FallbackChargeBalance {
			return group[i], true
		}
	}
	return Bucket{}, false
}

// sortBuckets 按（expires_at 最近优先、priority、id）排序，不过期的排最后。
func sortBuckets(buckets []Bucket) {
	sort.SliceStable(buckets, func(i, j int) bool {
		a, b := buckets[i], buckets[j]
		if (a.ExpiresAt == nil) != (b.ExpiresAt == nil) {
			return b.ExpiresAt == nil
		}
		if a.ExpiresAt != nil && !a.ExpiresAt.Equal(*b.ExpiresAt) {
			return a.ExpiresAt.Before(*b.ExpiresAt)
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.ID < b.ID
	})
}

// NeedAfterMultiplier 把基础应扣量乘以倍率，得到实际扣减量。
func NeedAfterMultiplier(charges []Charge, multiplier decimal.Decimal) []Charge {
	result := make([]Charge, 0, len(charges))
	for _, charge := range charges {
		result = append(result, Charge{Unit: charge.Unit, Qty: round(charge.Qty.Mul(multiplier), settleScale)})
	}
	return result
}
