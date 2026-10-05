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
	for _, charge := range ordered {
		deductUnit(&plan, charge, byUnit[charge.Unit], remaining)
	}
	return plan
}

// deductUnit 扣减一个结算单位的应扣量。
func deductUnit(plan *DeductionPlan, charge Charge, group []Bucket, remaining map[uint64]decimal.Decimal) {
	need := charge.Qty
	if !need.IsPositive() {
		return
	}
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
		plan.Lines = append(plan.Lines, DeductionLine{BucketID: bucket.ID, Unit: charge.Unit, Qty: round(take, settleScale)})
		remaining[bucket.ID] = available.Sub(take)
		plan.Updates[bucket.ID] = round(remaining[bucket.ID], settleScale)
		need = need.Sub(take)
	}
	if !need.IsPositive() {
		return
	}
	// currency 账本允许透支：后付钱包是最后的兜底，包扣尽后差额挂在
	// fallback=charge_balance 的 currency 包上记负数（后付透支）。
	if charge.Unit == billing.UnitSettleCurrency {
		if sink, ok := negativeSink(group); ok {
			plan.Lines = append(plan.Lines, DeductionLine{BucketID: sink.ID, Unit: charge.Unit, Qty: round(need, settleScale)})
			remaining[sink.ID] = remaining[sink.ID].Sub(need)
			plan.Updates[sink.ID] = round(remaining[sink.ID], settleScale)
			return
		}
	}
	// 非 currency 的差额不转 currency：schema 里没有跨结算单位的折算率
	// （account_bucket 不记商品单价，组件表也没有「折合成货币」的字段），
	// 按 1:1 记账会把 100M token 记成 100M 货币。欠额只记事实，不追偿。
	plan.Shortfall[charge.Unit] = round(plan.Shortfall[charge.Unit].Add(need), settleScale)
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
