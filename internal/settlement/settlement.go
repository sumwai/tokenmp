// Package settlement 实现用量结算：定价解析、折算、倍率链与账本扣减。
//
// 独立成包而不是塞进 store 或 cmd：折算公式、规则匹配与扣减序都是纯规则，
// 不依赖数据库，单独成包后可以用表驱动单测覆盖全部分支；store 只提供事务与查询，
// cmd 只做装配。
//
// 全仓库只有本包实现倍率匹配与折算，其它位置出现同义计算即为重复实现。
//
// 小数运算用 github.com/shopspring/decimal：金额与数量列是 DECIMAL(24,8)，
// decimal 与十进制文本往返无损；自研 int64 定点要自己处理进位与舍入，
// 在本场景里属于额外风险，不采纳。
package settlement

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// 结算与金额列的口径。标度与迁移里的 DECIMAL 定义一致：
// 金额与数量是 DECIMAL(24,8)，倍率是 DECIMAL(10,4)。
const (
	// settleScale 是金额与数量的小数位，对应 DECIMAL(24,8)。
	settleScale int32 = 8
	// multiplierScale 是倍率的小数位，对应 DECIMAL(10,4)。
	multiplierScale int32 = 4
	// divisionPrecision 是 decimal 做除法时保留的中间小数位。
	//
	// 取 24（列定义的整数位宽度）而不是默认的 16：折算率是「单价 / 基准量」，
	// 中间结果先于最终舍入截断会让复算与流水对不上；这里放大到足够宽，
	// 只在写入列时按 settleScale 舍入。
	divisionPrecision int32 = 24
)

// ErrNoPricing 表示（商家、模型、时刻）没有生效的定价版本。
//
// 无定价不是故障：新模型未上价时按占位口径落流水，不阻断转发。
var ErrNoPricing = errors.New("settlement: 没有生效的定价版本")

// Component 是一个计价分量：metric 折算到某个结算单位的比率。
//
// 单价语义是折算率：结算量 = usage[metric] × UnitPrice / BasisQty。
type Component struct {
	ID         uint64
	Metric     billing.Metric
	UnitSettle billing.UnitSettle
	UnitPrice  decimal.Decimal
	BasisQty   decimal.Decimal
	TierFrom   string
	TierTo     string
	TierBasis  string
}

// Pricing 是一个生效定价版本及其全部分量。
type Pricing struct {
	ID         uint64
	Version    int
	Components []Component
}

// Rule 是一条条件倍率规则。
//
// Metric 为空串表示不限指标；TimeFrom / TimeTo 形如 "HH:MM:SS"，空串表示不限；
// WeekdayMask / DayKindMask 为 nil 表示不限。
type Rule struct {
	ID          uint64
	Scope       billing.Scope
	ScopeID     uint64
	Metric      billing.Metric
	Multiplier  decimal.Decimal
	ValidFrom   *time.Time
	ValidTo     *time.Time
	TimeFrom    string
	TimeTo      string
	WeekdayMask *uint8
	DayKindMask *uint16
	Calendar    string
	Priority    int
}

// Bucket 是一行账本存量。
//
// ExpiresAt 为 nil 表示不过期，扣减时排在所有会过期的包之后。
// UnitRate 是购买时刻锁定的单位货币价值（price / qty）；零值表示无折算率，
// 包扣尽后的差额退化为记欠额。
type Bucket struct {
	ID        uint64
	Unit      billing.UnitSettle
	Remaining decimal.Decimal
	ExpiresAt *time.Time
	Fallback  billing.Fallback
	Priority  int
	UnitRate  decimal.Decimal
}

// Usage 是一条待落库的结算流水。
//
// 结算字段与归属一起交给存储层，由存储层在同一个事务里写入流水并更新账本。
// APIKeyID 为 0 表示无 key 维度，该行不计入 api_key 限额。
type Usage struct {
	MerchantID      uint64
	AccountID       uint64
	ChannelID       uint64
	APIKeyID        uint64
	Model           string
	Metrics         map[billing.Metric]int
	PricingID       uint64
	PricingSnapshot []byte
	GrossAmount     decimal.Decimal
	Multiplier      decimal.Decimal
	Settlement      []byte
}

// Input 是一次结算请求的输入事实。
//
// Model 是实际履约的上游模型名，定价按它解析；RequestedModel 是客户端请求的
// 模型名，渠道倍率按它查 upstream_model_map。两者可能不同（别名映射）。
type Input struct {
	RequestID      string
	MerchantID     uint64
	AccountID      uint64
	ChannelID      uint64
	APIKeyID       uint64
	Model          string
	RequestedModel string
	Usage          map[billing.Metric]int
	// AsOf 是定价与规则的判定时刻；零值取结算器时钟。
	AsOf time.Time
}

// validate 校验结算输入的最小事实。
//
// 归属缺失会让流水记到错误账户上，宁可在结算入口拒绝并由调用方落占位流水，
// 也不写一行归属可疑的记录。
func (in Input) validate() error {
	if in.MerchantID == 0 {
		return errors.New("settlement: merchant_id 不能为 0")
	}
	if in.AccountID == 0 {
		return errors.New("settlement: account_id 不能为 0")
	}
	if in.ChannelID == 0 {
		return errors.New("settlement: channel_id 不能为 0")
	}
	if in.Model == "" {
		return errors.New("settlement: model 不能为空")
	}
	for metric := range in.Usage {
		if err := billing.ValidateMetric(metric); err != nil {
			return err
		}
	}
	return nil
}

// Charge 是一个结算单位的应扣量。
type Charge struct {
	Unit billing.UnitSettle
	Qty  decimal.Decimal
}

// DeductionLine 是一行扣减明细。
//
// Rate 是跨单位折算率：非零表示这一行由其它结算单位的差额按 Rate 折算而来，
// 零值表示原始单位的直接扣减。明细带上 Rate 后，转换部分可从流水本身复算。
type DeductionLine struct {
	BucketID uint64
	Unit     billing.UnitSettle
	Qty      decimal.Decimal
	Rate     decimal.Decimal
}

// DeductionPlan 是一次扣减的结果：明细、各账本新存量与欠额。
type DeductionPlan struct {
	// Lines 按扣减顺序排列，一次请求可跨多个账本。
	Lines []DeductionLine
	// Updates 是发生变化的账本 id 与新存量，包含被透支成负数的 currency 账本。
	Updates map[uint64]decimal.Decimal
	// Shortfall 是仍不足的量，按结算单位记；欠额不追偿，仅作事实记录。
	Shortfall map[billing.UnitSettle]decimal.Decimal
}

// newPlan 构造一个空的扣减计划。
func newPlan() DeductionPlan {
	return DeductionPlan{
		Updates:   make(map[uint64]decimal.Decimal),
		Shortfall: make(map[billing.UnitSettle]decimal.Decimal),
	}
}

// round 把小数舍入到指定标度。
func round(d decimal.Decimal, scale int32) decimal.Decimal {
	return d.Round(scale)
}
