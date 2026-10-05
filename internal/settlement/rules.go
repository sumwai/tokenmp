package settlement

import (
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件是倍率匹配的唯一实现：scope → metric → 有效期 → 时段 → 星期 → 日历
// 逐层过滤，同一层多条取 priority 高者。
//
// 规则只描述「什么时候乘多少」，链的组合与具体场景无关，因此规则再多也只是数据，
// 本文件不随规则数量增长。

// weekdayBits 是星期位掩码的位位置基准：周一为 bit0，与 DDL 注释一致。
//
// Go 的 time.Weekday 以周日为 0，换算成周一为 0 需要 (wd + 6) % 7。
const weekdayOffset = 6

// daysPerWeek 是星期数：位掩码与取模都需要，集中一处。
const daysPerWeek = 7

// MatchRule 在一组候选规则里选出命中的最高优先级规则。
//
// usage 用于判定 metric 过滤：规则不限指标，或限制了某个本次确实出现的指标，才算候选；
// 只看「请求里出现过的指标」而不是某一个固定指标，是因为一次请求可以同时含输入与输出，
// 而规则可能只覆盖其中之一。
//
// dayKinds 是 calender 名到当日性质的映射，由调用方按规则里的 calendar 预先查好；
// 缺数据的日期按 workday 处理（口径见 DDL：sys_calendar 是数据，缺数据不等于不匹配）。
func MatchRule(rules []Rule, usage map[billing.Metric]int, at time.Time, dayKinds map[string]billing.DayKind) (Rule, bool) {
	var (
		best     Rule
		hasMatch bool
	)
	for _, rule := range rules {
		if !rule.matches(usage, at, dayKinds) {
			continue
		}
		if !hasMatch || rule.Priority > best.Priority ||
			(rule.Priority == best.Priority && rule.ID < best.ID) {
			best, hasMatch = rule, true
		}
	}
	return best, hasMatch
}

// matches 报告单条规则是否命中当前请求。
func (r Rule) matches(usage map[billing.Metric]int, at time.Time, dayKinds map[string]billing.DayKind) bool {
	if r.Metric != "" {
		if qty, ok := usage[r.Metric]; !ok || qty == 0 {
			return false
		}
	}
	if r.ValidFrom != nil && at.Before(*r.ValidFrom) {
		return false
	}
	if r.ValidTo != nil && !at.Before(*r.ValidTo) {
		return false
	}
	if !timeInWindow(at, r.TimeFrom, r.TimeTo) {
		return false
	}
	if r.WeekdayMask != nil && !weekdayBitSet(*r.WeekdayMask, at) {
		return false
	}
	if r.DayKindMask != nil && !dayKindBitSet(*r.DayKindMask, r.Calendar, dayKinds) {
		return false
	}
	return true
}

// timeInWindow 报告 at 的时分秒是否落在 [from, to) 内。
//
// from 晚于 to 表示跨零点的窗口（如 22:00–06:00），此时命中条件是「大于等于 from
// 或小于 to」。字符串按 "HH:MM:SS" 零填充比较与时刻比较同序。
func timeInWindow(at time.Time, from, to string) bool {
	if from == "" && to == "" {
		return true
	}
	tod := at.Format("15:04:05")
	switch {
	case from != "" && to != "" && from > to:
		return tod >= from || tod < to
	case from != "" && to != "":
		return tod >= from && tod < to
	case from != "":
		return tod >= from
	default:
		return tod < to
	}
}

// weekdayBitSet 报告 at 的星期是否在掩码内（bit0=周一 … bit6=周日）。
func weekdayBitSet(mask uint8, at time.Time) bool {
	bit := (int(at.Weekday()) + weekdayOffset) % daysPerWeek
	return mask&(1<<uint(bit)) != 0
}

// dayKindBitSet 报告当日性质是否在掩码内。
//
// 日历缺数据的日期按 workday 处理：放假通知没覆盖到的日子就是普通工作日。
// 数据库里出现未登记的性质时按 workday 处理并让规则自行取舍，而不是让整次结算失败。
func dayKindBitSet(mask uint16, calendar string, dayKinds map[string]billing.DayKind) bool {
	kind, ok := dayKinds[calendar]
	if !ok {
		kind = billing.DayKindWorkday
	}
	bit, err := kind.Bit()
	if err != nil {
		bit = billing.DayKindBitWorkday
	}
	return mask&bit != 0
}

// ChainMultiplier 把渠道倍率、账户倍率与命中规则倍率相乘。
//
// 结果按 DECIMAL(10,4) 的标度舍入，使写入流水的倍率与扣减所用倍率一致，
// 历史流水从快照里的各因子复算能得到同一结果。
func ChainMultiplier(channel, account decimal.Decimal, rules []Rule) decimal.Decimal {
	multiplier := channel.Mul(account)
	for _, rule := range rules {
		multiplier = multiplier.Mul(rule.Multiplier)
	}
	return round(multiplier, multiplierScale)
}
