package settlement

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件是商家分佣与结算口径的唯一出处：merchant.settle_info 的形状，账期的取法，
// 以及「卖出价 − 平台抽成 − 上游成本」这条分账恒等式的计算。
//
// 与同包其余部分的分工：那里算「一次请求该扣多少」，本文件算「一个账期该分多少」。
// 两者共用同一套 decimal 标度与舍入口径，对账时不会出现两处各舍一次的分叉。
//
// 口径（与 docs/compatibility.md 的「分佣与结算口径」一节同源）：
//
//	卖出总额  账期内该商家名下账户的实付合计（account_purchase.price_paid）
//	上游成本  账期内该商家名下渠道的用量按牌价基础价合计（billing_usage.gross_amount）
//	平台抽成  卖出总额 × settle_info 的抽成率
//	商家收益  卖出总额 − 平台抽成 − 上游成本
//
// 四项合计的取值来自存储层聚合，本文件只负责口径本身：抽成率怎么解释、账期怎么取、
// 四个数怎么相互约束。刻意不在这里拼 SQL：口径是纯规则，进得了表驱动单测。

// settle_info 的空值形态：NULL 与「配了但什么都没写」。
const (
	// nullJSON 是 settle_info 为 SQL NULL 时读出的文本。
	nullJSON = "null"
	// emptyObjectJSON 是空对象的形态。
	emptyObjectJSON = "{}"
)

// DefaultSettlePeriod 是未配置账期时的默认值：按自然月出账。
//
// 月是唯一可用作默认的自然账期：日会让对账频率与流水条数同阶，周的账务含义没有
// 行业共识，而月是通行的结算周期。
const DefaultSettlePeriod = billing.PeriodMonth

// settlePeriods 是结算账期的写入白名单。
//
// 窗口限额的 5h 与 total 不在其中：账期是「一段时间结一次账」，滚动窗口与总量口径
// 都没有可出账的边界。限额与账期共用 billing.Period 这个类型，取值集合不同。
var settlePeriods = map[billing.Period]struct{}{
	billing.PeriodDay:   {},
	billing.PeriodWeek:  {},
	billing.PeriodMonth: {},
}

// settleInfoView 是 settle_info 的落库形态。
//
// 抽成率是十进制字符串而不是 JSON 数字：JSON 数字会被消费方解析成双精度浮点，
// 0.1 这类十进制小数在二进制浮点里没有精确表示，读回的值与库里写下的值就不相等。
// 口径上的数要能逐位复现，才对得起「对账」这个用途。
type settleInfoView struct {
	CommissionRate string `json:"commission_rate"`
	Period         string `json:"period"`
}

// SettleInfo 是 merchant.settle_info 的口径：平台抽成率与结算账期。
type SettleInfo struct {
	// CommissionRate 是平台抽成率，取值域 [0, 1)。
	CommissionRate decimal.Decimal
	// Period 是结算账期，只能是 day / week / month。
	Period billing.Period
}

// DefaultSettleInfo 返回未配置时的口径：不抽成、按自然月出账。
//
// 平台自营商家就是这一档：它在数据上同样是一行商家（不是特例），但不与自己分成，
// 因此抽成率为 0 而不是一个「平台自己抽给自己」的数。
func DefaultSettleInfo() SettleInfo {
	return SettleInfo{CommissionRate: decimal.Zero, Period: DefaultSettlePeriod}
}

// ValidateSettlePeriod 是账期写入校验。
func ValidateSettlePeriod(p billing.Period) error {
	if _, ok := settlePeriods[p]; !ok {
		return fmt.Errorf("settlement: 结算账期 %q 不是可出账的自然周期（day | week | month）", string(p))
	}
	return nil
}

// ValidateSettleInfo 校验口径的取值域。
//
// 抽成率必须落在 [0, 1)：等于 1 时商家收益恒非正，等于把商家踢出市场，
// 上限由口径本身给出，不靠调用方自觉。
//
// 小数位不得超过倍率标度（DECIMAL(10,4)）：多出来的位在倍率链里表达不了，
// 静默舍入会让人以为写下的抽成率生效了，而实际生效的是另一位小数。
func ValidateSettleInfo(info SettleInfo) error {
	if info.CommissionRate.IsNegative() {
		return fmt.Errorf("settlement: 平台抽成率 %s 不能为负", info.CommissionRate.String())
	}
	if info.CommissionRate.GreaterThanOrEqual(decimal.NewFromInt(1)) {
		return fmt.Errorf("settlement: 平台抽成率 %s 必须小于 1", info.CommissionRate.String())
	}
	if info.CommissionRate.Exponent() < -multiplierScale {
		return fmt.Errorf("settlement: 平台抽成率 %s 最多 %d 位小数", info.CommissionRate.String(), multiplierScale)
	}
	return ValidateSettlePeriod(info.Period)
}

// ParseSettleInfo 解析 merchant.settle_info 的原文。
//
// NULL、空串与空对象都返回默认口径而不是错误：未配置是常态（迁移时该列就是 NULL），
// 把「没配」当错误会让出账路径到处写分支。
//
// 读与写在这一处都是严的（写入拒绝、读取也拒绝越界取值），与 billing 枚举「读宽写严」
// 的口径不同：那里读到的未知值只是跳过一行，这里读到的越界抽成率会让整张对账单
// 算错钱。宁可让出账显式失败，也不静默按默认口径出账。
func ParseSettleInfo(raw []byte) (SettleInfo, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte(nullJSON)) || bytes.Equal(trimmed, []byte(emptyObjectJSON)) {
		return DefaultSettleInfo(), nil
	}
	var view settleInfoView
	if err := json.Unmarshal(trimmed, &view); err != nil {
		return SettleInfo{}, fmt.Errorf("settlement: settle_info 不是合法 JSON: %w", err)
	}
	info := DefaultSettleInfo()
	if rate := strings.TrimSpace(view.CommissionRate); rate != "" {
		parsed, err := decimal.NewFromString(rate)
		if err != nil {
			return SettleInfo{}, fmt.Errorf("settlement: settle_info 的抽成率 %q 不是合法数字", view.CommissionRate)
		}
		info.CommissionRate = parsed
	}
	if period := strings.TrimSpace(view.Period); period != "" {
		info.Period = billing.Period(period)
	}
	if err := ValidateSettleInfo(info); err != nil {
		return SettleInfo{}, err
	}
	return info, nil
}

// amountString 把金额渲染成对外形态的十进制字符串。
//
// 金额按金额列标度（DECIMAL(24,8)）补足小数位，与抽成率各按其列标度：读对账单的人
// 可以把每个字段直接对回它来自哪一列，也不用在「0.1 与 0.1000 是不是同一个数」上停顿。
func amountString(amount decimal.Decimal) string {
	return amount.StringFixed(settleScale)
}

// rateString 把抽成率渲染成落库形态的十进制字符串。
//
// 展示与落库共用同一套标度：读到的口径与库里的字节逐位一致，两个地方不用各解释一遍
// 「0.1 与 0.1000 是不是同一个数」。
func rateString(rate decimal.Decimal) string {
	return rate.StringFixed(multiplierScale)
}

// Encode 把口径编码成落库形态。
//
// 归一化是有意的：写入就把取值收敛到白名单与标度上，读取方不必再兼容
// 「抽成率写成百分号字符串」这类自由形态。抽成率按倍率标度补足小数位，
// 同一笔口径在库里只有一种写法（0.1 与 0.10 都是 0.1000），对账时可直接比字节。
func (info SettleInfo) Encode() ([]byte, error) {
	if err := ValidateSettleInfo(info); err != nil {
		return nil, err
	}
	return json.Marshal(settleInfoView{
		CommissionRate: rateString(info.CommissionRate),
		Period:         string(info.Period),
	})
}

// SettleInfoView 是 settle_info 的对外形态：抽成率同样是十进制字符串。
type SettleInfoView struct {
	MerchantID     uint64 `json:"merchant_id"`
	CommissionRate string `json:"commission_rate"`
	Period         string `json:"period"`
}

// View 输出口径的对外形态。
func (info SettleInfo) View(merchantID uint64) SettleInfoView {
	return SettleInfoView{
		MerchantID:     merchantID,
		CommissionRate: rateString(info.CommissionRate),
		Period:         string(info.Period),
	}
}

// Summary 是一个商家在一个账期内的对账事实，来自流水聚合，不含任何口径。
type Summary struct {
	// Trades 是账期内的成交笔数。
	Trades int64
	// GrossSales 是卖出总额。
	GrossSales decimal.Decimal
	// UpstreamCost 是上游成本。
	UpstreamCost decimal.Decimal
}

// Bill 是一个商家在一个账期内的对账结果。
//
// 四项金额满足 Payout + Commission == GrossSales − UpstreamCost，见 Balanced。
type Bill struct {
	MerchantID     uint64
	Period         billing.Period
	From           time.Time
	To             time.Time
	CommissionRate decimal.Decimal
	Trades         int64
	GrossSales     decimal.Decimal
	Commission     decimal.Decimal
	UpstreamCost   decimal.Decimal
	Payout         decimal.Decimal
}

// Settle 按口径把账期内的事实算成对账单。
//
// 收益由另外三项推出而不是独立累加：恒等式对任何输入都成立，包括上游成本超过卖出
// 总额（收益为负）的账期 —— 那是事实，不该被算法抹平成零。
// 舍入只发生在抽成这一处（乘法结果按金额列标度收敛），卖出与成本直接来自库中
// DECIMAL(24,8)，不再二次舍入，否则合计数会与逐行明细对不上。
func Settle(merchantID uint64, info SettleInfo, from, to time.Time, sum Summary) Bill {
	commission := sum.GrossSales.Mul(info.CommissionRate).Round(settleScale)
	return Bill{
		MerchantID:     merchantID,
		Period:         info.Period,
		From:           from,
		To:             to,
		CommissionRate: info.CommissionRate,
		Trades:         sum.Trades,
		GrossSales:     sum.GrossSales,
		Commission:     commission,
		UpstreamCost:   sum.UpstreamCost,
		Payout:         sum.GrossSales.Sub(commission).Sub(sum.UpstreamCost),
	}
}

// Balanced 报告对账单是否自洽：商家收益 + 平台抽成 == 卖出总额 − 上游成本。
//
// 计算路径本身就保证恒等式，本方法给消费方（对账单文件、运营核对、将来的页面）一个
// 不依赖内部实现的独立判据：读到的四个数是不是同一套账，一眼可判。
func (b Bill) Balanced() bool {
	return b.Payout.Add(b.Commission).Equal(b.GrossSales.Sub(b.UpstreamCost))
}

// BillView 是对账单的对外形态：金额一律十进制字符串。
type BillView struct {
	MerchantID     uint64 `json:"merchant_id"`
	Period         string `json:"period"`
	From           string `json:"from"`
	To             string `json:"to"`
	CommissionRate string `json:"commission_rate"`
	Trades         int64  `json:"trades"`
	GrossSales     string `json:"gross_sales"`
	Commission     string `json:"commission"`
	UpstreamCost   string `json:"upstream_cost"`
	Payout         string `json:"payout"`
}

// View 输出对账单的对外形态。
//
// 金额是十进制字符串而不是 JSON 数字：DECIMAL(24,8) 的有效位远超双精度，
// 写成数字会让消费方在解析时丢精度，对账的数从此对不上。
func (b Bill) View() BillView {
	return BillView{
		MerchantID:     b.MerchantID,
		Period:         string(b.Period),
		From:           b.From.UTC().Format(time.RFC3339),
		To:             b.To.UTC().Format(time.RFC3339),
		CommissionRate: rateString(b.CommissionRate),
		Trades:         b.Trades,
		GrossSales:     amountString(b.GrossSales),
		Commission:     amountString(b.Commission),
		UpstreamCost:   amountString(b.UpstreamCost),
		Payout:         amountString(b.Payout),
	}
}

// LastPeriod 返回 now 之前最近一个完整的自然账期 [from, to)。
//
// day 指上一自然日，week 指上一自然周（周一起），month 指上一自然月；
// 5h 与 total 没有可出账的边界，返回错误。
//
// 边界按 UTC 计算：billing_usage.created_at 与 account_purchase.purchased_at 由驱动
// 按 UTC 写入 DATETIME，账期边界用本地时区会和库里的时刻差一个时区偏移，
// 于是上一账期的流水被划到当期或反之。
func LastPeriod(period billing.Period, now time.Time) (time.Time, time.Time, error) {
	if err := ValidateSettlePeriod(period); err != nil {
		return time.Time{}, time.Time{}, err
	}
	utc := now.UTC()
	switch period {
	case billing.PeriodDay:
		start := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
		return start, start.AddDate(0, 0, 1), nil
	case billing.PeriodWeek:
		// 周一起算：weekdayOffset 与 daysPerWeek 与倍率规则的星期判定同源
		// （见 rules.go），两处各写一份位移迟早会漂开。
		offset := (int(utc.Weekday()) + weekdayOffset) % daysPerWeek
		start := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC).
			AddDate(0, 0, -offset-daysPerWeek)
		return start, start.AddDate(0, 0, daysPerWeek), nil
	case billing.PeriodMonth:
		start := time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
		return start, start.AddDate(0, 1, 0), nil
	default:
		// ValidateSettlePeriod 已经拦下其余取值，这里只是让 switch 完备。
		return time.Time{}, time.Time{}, fmt.Errorf("settlement: 结算账期 %q 没有可出账的自然边界", string(period))
	}
}
