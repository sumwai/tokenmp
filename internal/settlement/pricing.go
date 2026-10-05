package settlement

import (
	"sort"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件是折算的唯一实现：把 usage 的分量按计价分量折算成各结算单位的应扣量。
//
// 口径：consumption = Σ(usage[metric] × unit_price / basis_qty)，按 unit_settle 分组。
// usage 里没有对应分量的 metric 不参与计价；分量表里 usage 未覆盖的 metric 不产生费用。

// tierBasisRequestInput 是阶梯依据取值「单请求输入量」。
//
// 取值集由数据定义，不加枚举约束（见迁移里 tier_basis 的注释）；本文件只识别
// 自己实现的口径，其余取值不参与过滤，避免用猜测把分量错误地排除掉。
const tierBasisRequestInput = "request_input"

// ResolveCharges 把一次用量折算为各结算单位的应扣量。
//
// 第二个返回值是按命中顺序排列的计价分量，供 pricing_snapshot 原样记录；
// 只有 usage 中出现的 metric 才列入，保证快照只描述真正参与本次结算的数据。
//
// 阶梯口径：分量带 tier_basis 时按该依据的基准量过滤，落在 [tier_from, tier_to)
// 内才命中；区间端空缺表示无界。无法识别的 tier_basis 不做过滤（宁可用分量，
// 不因为不认识一个取值就静默不计费）。
func ResolveCharges(usage map[billing.Metric]int, components []Component) ([]Charge, []Component) {
	charges := make(map[billing.UnitSettle]decimal.Decimal)
	hit := make([]Component, 0, len(components))
	for _, component := range components {
		qty, ok := usage[component.Metric]
		if !ok || qty == 0 {
			continue
		}
		if !component.appliesTo(usage) {
			continue
		}
		if component.BasisQty.IsZero() {
			// 分母为 0 无法折算；定价数据错误不应让整条结算失败，
			// 跳过该分量并让流水金额反映「这个分量没计价」。
			continue
		}
		value := decimal.NewFromInt(int64(qty)).
			Mul(component.UnitPrice).
			DivRound(component.BasisQty, divisionPrecision)
		charges[component.UnitSettle] = charges[component.UnitSettle].Add(value)
		hit = append(hit, component)
	}

	units := make([]billing.UnitSettle, 0, len(charges))
	for unit := range charges {
		units = append(units, unit)
	}
	// 单位排序让同一份用量每次得到同样的输出顺序，扣减明细与快照因此可比较。
	sort.Slice(units, func(i, j int) bool { return units[i] < units[j] })

	result := make([]Charge, 0, len(units))
	for _, unit := range units {
		result = append(result, Charge{Unit: unit, Qty: round(charges[unit], settleScale)})
	}
	return result, hit
}

// appliesTo 报告分量是否落在本次用量的阶梯区间内。
func (c Component) appliesTo(usage map[billing.Metric]int) bool {
	if c.TierBasis == "" || (c.TierFrom == "" && c.TierTo == "") {
		return true
	}
	if c.TierBasis != tierBasisRequestInput {
		return true
	}
	basis := decimal.NewFromInt(int64(usage[billing.MetricInputToken]))
	if c.TierFrom != "" {
		from, err := decimal.NewFromString(c.TierFrom)
		if err == nil && basis.LessThan(from) {
			return false
		}
	}
	if c.TierTo != "" {
		to, err := decimal.NewFromString(c.TierTo)
		if err == nil && !basis.LessThan(to) {
			return false
		}
	}
	return true
}

// GrossAmount 返回各结算单位应扣量之和（未乘倍率）。
//
// 多结算单位混在一起求和，数值只在「同一次请求的基础消费量」这一层有意义：
// 它是流水的基础金额列，最终金额还乘倍率，且真实扣减按单位分别落在账本明细里。
func GrossAmount(charges []Charge) decimal.Decimal {
	total := decimal.Zero
	for _, charge := range charges {
		total = total.Add(charge.Qty)
	}
	return round(total, settleScale)
}
