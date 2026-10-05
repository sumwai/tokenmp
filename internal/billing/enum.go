// Package billing 是计费领域的公共定义。
//
// 本文件是枚举的唯一出处：迁移里 billing_* / account_quota 等表的字符串列
// 取值与本文件的 const 块一一对应，DDL 注释同步维护。新增取值只加 const
// 并在计费代码里识别，不改表结构、不发迁移。
//
// 读写的宽严不对称是有意的：写入由本进程产生，未知值必须拒绝，避免脏数据落库；
// 读取可能面对更新版本写入的数据，未知值原样返回，版本回滚不会因为一条
// 不认识的记录放大成整批查询失败。
package billing

import "fmt"

// Metric 是计费计量指标，对应 billing_price_component.metric 与
// billing_usage.usage 的键。
//
// 取值语义：
//
//	input_token        输入 token 数
//	output_token       输出 token 数
//	cache_read_token   缓存读取 token 数
//	cache_write_token  缓存写入 token 数（上游只报单一数量的形态）
//	cache_write_5m     TTL 5 分钟的缓存写入 token 数
//	cache_write_1h     TTL 1 小时的缓存写入 token 数
//	reasoning_token    推理 token 数，是 output_token 的子项
//	request            请求次数
//
// 子项口径：cache_read_token / cache_write_* 是 input_token 的子项，reasoning_token
// 是 output_token 的子项。它们各自独立计价：定价表里没写某个子项的分量时不重复计价，
// 写了也不叠加到主计数上（主计数已包含子项，叠加会重复收费）。
//
// 新增指标：本 const 块加一个取值，并在计费代码里识别该 metric。
// metric 是字符串列，不加数据库枚举，不发迁移。
type Metric string

const (
	// MetricInputToken 是输入 token。
	MetricInputToken Metric = "input_token"
	// MetricOutputToken 是输出 token。
	MetricOutputToken Metric = "output_token"
	// MetricCacheReadToken 是缓存读取 token。
	MetricCacheReadToken Metric = "cache_read_token"
	// MetricCacheWriteToken 是上游只报单一数量时的缓存写入 token。
	// 上游能区分 5 分钟与 1 小时分档时用 MetricCacheWrite5m / MetricCacheWrite1h。
	//
	//nolint:gosec // G101：这是计量指标名，不是凭据；启发式命中只因常量值里含 token。
	MetricCacheWriteToken Metric = "cache_write_token"
	// MetricCacheWrite5m 是 TTL 5 分钟的缓存写入 token。
	MetricCacheWrite5m Metric = "cache_write_5m"
	// MetricCacheWrite1h 是 TTL 1 小时的缓存写入 token。
	MetricCacheWrite1h Metric = "cache_write_1h"
	// MetricReasoningToken 是 output_token 的推理子项。
	// 它只作事实记录：定价分量里没写该 metric 时不计价，写了也不与 output_token 叠加。
	MetricReasoningToken Metric = "reasoning_token"
	// MetricRequest 是请求次数。
	MetricRequest Metric = "request"
)

// knownMetrics 是写入白名单，也是列举取值的唯一出处。
var knownMetrics = map[Metric]struct{}{
	MetricInputToken:      {},
	MetricOutputToken:     {},
	MetricCacheReadToken:  {},
	MetricCacheWriteToken: {},
	MetricCacheWrite5m:    {},
	MetricCacheWrite1h:    {},
	MetricReasoningToken:  {},
	MetricRequest:         {},
}

// Known 报告该指标是否在写入白名单内。
func (m Metric) Known() bool {
	_, ok := knownMetrics[m]
	return ok
}

// ValidateMetric 是写入口校验：未知指标直接拒绝。
func ValidateMetric(m Metric) error {
	if !m.Known() {
		return fmt.Errorf("billing: 未知的计量指标 %q", string(m))
	}
	return nil
}

// MetricFromDB 把数据库列值转成 Metric，刻意不做白名单校验。
// 未知值原样返回，由上层决定跳过该行还是按不匹配处理。
func MetricFromDB(raw string) Metric {
	return Metric(raw)
}

// UnitSettle 是结算单位，对应 billing_price_component.unit_settle，
// 与 account_bucket.unit、merchant_product.unit 共用同一取值集。
//
// 单价语义统一为折算率：unit_settle 指定折算到哪个单位，unit_price 按
// basis_qty 折算。同一张定价表因此能同时表达「后付钱包按 1M token 计价」
// 与「预付 token 包平摊扣存量」，口径差异只是数据。
//
//	currency  货币额度（后付钱包）
//	token     token 存量（token 包按 token 扣）
//	credit    赠送积分
//
// 新增单位：本 const 块加一个取值，并在计费代码里识别该单位。
// 刻意没有 currency 列：积分不是货币，计量单位由 metric 自带。
type UnitSettle string

const (
	// UnitSettleCurrency 是货币额度。
	UnitSettleCurrency UnitSettle = "currency"
	// UnitSettleToken 是 token 存量。
	UnitSettleToken UnitSettle = "token"
	// UnitSettleCredit 是赠送积分。
	UnitSettleCredit UnitSettle = "credit"
)

// knownUnitSettles 是写入白名单。
var knownUnitSettles = map[UnitSettle]struct{}{
	UnitSettleCurrency: {},
	UnitSettleToken:    {},
	UnitSettleCredit:   {},
}

// Known 报告该结算单位是否在写入白名单内。
func (u UnitSettle) Known() bool {
	_, ok := knownUnitSettles[u]
	return ok
}

// ValidateUnitSettle 是写入口校验：未知结算单位直接拒绝。
func ValidateUnitSettle(u UnitSettle) error {
	if !u.Known() {
		return fmt.Errorf("billing: 未知的结算单位 %q", string(u))
	}
	return nil
}

// UnitSettleFromDB 把数据库列值转成 UnitSettle，未知值原样返回。
func UnitSettleFromDB(raw string) UnitSettle {
	return UnitSettle(raw)
}

// WindowKind 是窗口型限额的窗口类型，对应 account_quota.window_kind。
//
// 窗口类型已决定重置语义，故 account_quota 没有 reset_policy 列：
//
//	rolling   滚动窗口，按流水时间戳聚合，不存在重置时刻（如 5 小时）
//	calendar  自然周期窗口，到期即重置（日 / 周 / 月）
//
// 新增窗口类型：本 const 块加一个取值，并在限额聚合代码里识别该类型。
type WindowKind string

const (
	// WindowKindRolling 是滚动窗口。
	WindowKindRolling WindowKind = "rolling"
	// WindowKindCalendar 是自然周期窗口。
	WindowKindCalendar WindowKind = "calendar"
)

// knownWindowKinds 是写入白名单。
var knownWindowKinds = map[WindowKind]struct{}{
	WindowKindRolling:  {},
	WindowKindCalendar: {},
}

// Known 报告该窗口类型是否在写入白名单内。
func (w WindowKind) Known() bool {
	_, ok := knownWindowKinds[w]
	return ok
}

// ValidateWindowKind 是写入口校验：未知窗口类型直接拒绝。
func ValidateWindowKind(w WindowKind) error {
	if !w.Known() {
		return fmt.Errorf("billing: 未知的窗口类型 %q", string(w))
	}
	return nil
}

// WindowKindFromDB 把数据库列值转成 WindowKind，未知值原样返回。
func WindowKindFromDB(raw string) WindowKind {
	return WindowKind(raw)
}

// Period 是窗口周期，对应 account_quota.period。
//
// 与 window_kind 拆成两列，而不是一个组合串：
//
//	5h     5 小时（配合 rolling 表示滚动窗口）
//	day    日
//	week   周
//	month  月
//	total  总量，不限窗口
//
// 合法组合由限额执行代码识别：rolling×5h、calendar×{day,week,month}、
// total 不依赖 window_kind。新增周期：本 const 块加一个取值，不发迁移。
type Period string

const (
	// Period5h 是 5 小时周期。
	Period5h Period = "5h"
	// PeriodDay 是日周期。
	PeriodDay Period = "day"
	// PeriodWeek 是周周期。
	PeriodWeek Period = "week"
	// PeriodMonth 是月周期。
	PeriodMonth Period = "month"
	// PeriodTotal 是总量口径。
	PeriodTotal Period = "total"
)

// knownPeriods 是写入白名单。
var knownPeriods = map[Period]struct{}{
	Period5h:    {},
	PeriodDay:   {},
	PeriodWeek:  {},
	PeriodMonth: {},
	PeriodTotal: {},
}

// Known 报告该周期是否在写入白名单内。
func (p Period) Known() bool {
	_, ok := knownPeriods[p]
	return ok
}

// ValidatePeriod 是写入口校验：未知周期直接拒绝。
func ValidatePeriod(p Period) error {
	if !p.Known() {
		return fmt.Errorf("billing: 未知的窗口周期 %q", string(p))
	}
	return nil
}

// PeriodFromDB 把数据库列值转成 Period，未知值原样返回。
func PeriodFromDB(raw string) Period {
	return Period(raw)
}

// DayKind 是日历中某一天的性质，对应 sys_calendar.day_kind。
//
// 节假日、调休是数据不是代码：放假通知 = 向 sys_calendar 插数据行，
// 规则引擎只问日历「今天是什么日子」，零代码变更零发版。
//
//	workday          普通工作日
//	weekend          普通周末
//	holiday          法定节假日
//	makeup_workday   调休上班（如国庆前的周日）
//
// 新增日期性质：本 const 块加一个取值，并在日历读取代码里识别该值。
type DayKind string

const (
	// DayKindWorkday 是普通工作日。
	DayKindWorkday DayKind = "workday"
	// DayKindWeekend 是普通周末。
	DayKindWeekend DayKind = "weekend"
	// DayKindHoliday 是法定节假日。
	DayKindHoliday DayKind = "holiday"
	// DayKindMakeupWorkday 是调休上班。
	DayKindMakeupWorkday DayKind = "makeup_workday"
)

// knownDayKinds 是写入白名单。
var knownDayKinds = map[DayKind]struct{}{
	DayKindWorkday:       {},
	DayKindWeekend:       {},
	DayKindHoliday:       {},
	DayKindMakeupWorkday: {},
}

// Known 报告该日期性质是否在写入白名单内。
func (d DayKind) Known() bool {
	_, ok := knownDayKinds[d]
	return ok
}

// ValidateDayKind 是写入口校验：未知日期性质直接拒绝。
func ValidateDayKind(d DayKind) error {
	if !d.Known() {
		return fmt.Errorf("billing: 未知的日期性质 %q", string(d))
	}
	return nil
}

// DayKindFromDB 把数据库列值转成 DayKind，未知值原样返回。
func DayKindFromDB(raw string) DayKind {
	return DayKind(raw)
}

// 位掩码取值，与 billing_price_rule.day_kind_mask 对应：一条规则按位与
// 判断当天属于哪一类，可以同时覆盖多类（如节假日与调休上班都给低价）。
//
// 位位置一旦写进历史规则行就不能再改，改了会让既有 mask 被重新解释；
// 新增 day_kind 只能追加到未使用的位。
const (
	// DayKindBitWorkday 对应 DayKindWorkday。
	DayKindBitWorkday uint16 = 1 << 0
	// DayKindBitWeekend 对应 DayKindWeekend。
	DayKindBitWeekend uint16 = 1 << 1
	// DayKindBitHoliday 对应 DayKindHoliday。
	DayKindBitHoliday uint16 = 1 << 2
	// DayKindBitMakeupWorkday 对应 DayKindMakeupWorkday。
	DayKindBitMakeupWorkday uint16 = 1 << 3
)

// Bit 返回该日期性质在位掩码中的位。
// 位位置与 sys_calendar.day_kind 的映射集中在这里，调用方不硬编码位移。
func (d DayKind) Bit() (uint16, error) {
	switch d {
	case DayKindWorkday:
		return DayKindBitWorkday, nil
	case DayKindWeekend:
		return DayKindBitWeekend, nil
	case DayKindHoliday:
		return DayKindBitHoliday, nil
	case DayKindMakeupWorkday:
		return DayKindBitMakeupWorkday, nil
	default:
		return 0, fmt.Errorf("billing: 日期性质 %q 没有对应的位掩码", string(d))
	}
}

// Scope 是规则的适用范围，对应 billing_price_rule.scope 与 account_quota.scope。
//
// scope_id 指向对应实体的主键，刻意不建外键。两张表各自适用的取值：
//
//	billing_price_rule: pricing | model_map | plan | account
//	account_quota:      account | api_key | channel | plan
//
// 合并为一个 const 块是为了枚举只有一处；表级适用集合由上面的注释与
// DDL 注释界定，写入校验只做白名单，不做表级收窄。
//
// 新增范围：本 const 块加一个取值，并在匹配 / 限额代码里识别该范围。
type Scope string

const (
	// ScopePricing 指向 billing_pricing。
	ScopePricing Scope = "pricing"
	// ScopeModelMap 指向 upstream_model_map（渠道级倍率）。
	ScopeModelMap Scope = "model_map"
	// ScopePlan 指向上游套餐。
	ScopePlan Scope = "plan"
	// ScopeAccount 指向 account。
	ScopeAccount Scope = "account"
	// ScopeAPIKey 指向 account_api_key。
	ScopeAPIKey Scope = "api_key"
	// ScopeChannel 指向 upstream_channel。
	ScopeChannel Scope = "channel"
)

// knownScopes 是写入白名单。
var knownScopes = map[Scope]struct{}{
	ScopePricing:  {},
	ScopeModelMap: {},
	ScopePlan:     {},
	ScopeAccount:  {},
	ScopeAPIKey:   {},
	ScopeChannel:  {},
}

// Known 报告该范围是否在写入白名单内。
func (s Scope) Known() bool {
	_, ok := knownScopes[s]
	return ok
}

// ValidateScope 是写入口校验：未知范围直接拒绝。
func ValidateScope(s Scope) error {
	if !s.Known() {
		return fmt.Errorf("billing: 未知的规则范围 %q", string(s))
	}
	return nil
}

// ScopeFromDB 把数据库列值转成 Scope，未知值原样返回。
func ScopeFromDB(raw string) Scope {
	return Scope(raw)
}

// Action 是限额超出时的处置方式，对应 account_quota.action。
//
//	reject    直接拒绝请求
//	throttle  限速放行
//
// 新增处置方式：本 const 块加一个取值，并在限额执行代码里识别该处置。
type Action string

const (
	// ActionReject 是拒绝。
	ActionReject Action = "reject"
	// ActionThrottle 是限速。
	ActionThrottle Action = "throttle"
)

// knownActions 是写入白名单。
var knownActions = map[Action]struct{}{
	ActionReject:   {},
	ActionThrottle: {},
}

// Known 报告该处置方式是否在写入白名单内。
func (a Action) Known() bool {
	_, ok := knownActions[a]
	return ok
}

// ValidateAction 是写入口校验：未知处置方式直接拒绝。
func ValidateAction(a Action) error {
	if !a.Known() {
		return fmt.Errorf("billing: 未知的限额处置方式 %q", string(a))
	}
	return nil
}

// ActionFromDB 把数据库列值转成 Action，未知值原样返回。
func ActionFromDB(raw string) Action {
	return Action(raw)
}

// QuotaEvent 是 account_quota_event.event 的事件类型。
//
// 额度变更事件化：事实只追加，不 UPDATE 计数器，审计链完整。
//
//	reset  重置窗口基准，聚合口径为 created_at > baseline_at
//
// 新增事件：本 const 块加一个取值，并在限额聚合代码里识别该事件。
// 总量额度（套餐 N 次）不走事件，直接是 account_bucket 行。
type QuotaEvent string

const (
	// QuotaEventReset 是窗口重置事件。
	QuotaEventReset QuotaEvent = "reset"
)

// knownQuotaEvents 是写入白名单。
var knownQuotaEvents = map[QuotaEvent]struct{}{
	QuotaEventReset: {},
}

// Known 报告该事件类型是否在写入白名单内。
func (e QuotaEvent) Known() bool {
	_, ok := knownQuotaEvents[e]
	return ok
}

// ValidateQuotaEvent 是写入口校验：未知事件类型直接拒绝。
func ValidateQuotaEvent(e QuotaEvent) error {
	if !e.Known() {
		return fmt.Errorf("billing: 未知的限额事件 %q", string(e))
	}
	return nil
}

// QuotaEventFromDB 把数据库列值转成 QuotaEvent，未知值原样返回。
func QuotaEventFromDB(raw string) QuotaEvent {
	return QuotaEvent(raw)
}
