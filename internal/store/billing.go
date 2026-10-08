package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件是 0002 迁移引入的计费表的读写入口。
//
// 写入入口先过 billing 的枚举白名单再拼参数化 SQL：脏枚举值不落库。
// 查询一律参数化，不拼字符串。
//
// 金额与数量用 string 承载，不用 float64：DECIMAL 是精确十进制，
// float64 是二进制浮点，来回转换会在对账时累积误差。本存储层不做算术，
// 口径由结算函数决定。
//
// 读回的是数据库的定点文本，带列定义的标度（写入 "0.27" 读回
// "0.27000000"）；数值精确，标度不保留。比较时按数值解析，不要做
// 字符串相等判断。

// executor 是写入路径依赖的最小执行面，*sql.DB 与 *sql.Tx 都满足。
//
// 抽出这一层是为了让单测注入记录型假实现，断言参数化 SQL 与参数序列，
// 无需真实数据库。读路径要返回 *sql.Rows，脱离驱动无法伪造，
// 由集成测试覆盖。
type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// session 是读路径依赖的最小执行面：与 executor 同一思路，补上查询能力，
// 让同一段查询代码既能在 *sql.DB 上跑，也能在 *sql.Tx 上跑（结算要求事务内读）。
type session interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// scanTime 承载 DATE / DATETIME / TIME 列。
//
// go-sql-driver 在 DSN 开启 parseTime 时把这些列解析成 time.Time，
// 未开启时原样返回 []byte；两种形态都接受，存储层因此不依赖 DSN 上
// 一个不显眼的选项。TIME 列不含日期部分，按当日零点解析，日期部分无意义。
type scanTime struct {
	Time  time.Time
	Valid bool
}

// scanTimeLayouts 覆盖驱动可能给出的形态，长的在前避免误匹配。
var scanTimeLayouts = []string{
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"15:04:05.999999",
	"15:04:05",
}

// Scan 实现 sql.Scanner。
func (s *scanTime) Scan(src any) error {
	if src == nil {
		*s = scanTime{}
		return nil
	}
	switch v := src.(type) {
	case time.Time:
		s.Time, s.Valid = v, true
		return nil
	case []byte:
		return s.parse(string(v))
	case string:
		return s.parse(v)
	default:
		return fmt.Errorf("store: 无法把 %T 解析为时间", src)
	}
}

func (s *scanTime) parse(raw string) error {
	for _, layout := range scanTimeLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			s.Time, s.Valid = t, true
			return nil
		}
	}
	return fmt.Errorf("store: 无法解析时间 %q", raw)
}

// nullArg 把空字符串转成 SQL NULL，非空原样。
// 空串与 NULL 在本 schema 里同义（未设置），统一在一处收敛。
func nullArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// optionalStringArg 把可空字符串转成驱动参数；nil 即 SQL NULL。
//
// 与 nullArg 的分工：nullArg 按空串判定「未设置」，适用于列语义里空串无意义、
// 空串与 NULL 同义的场景；本函数按指针判定，供调用方显式区分「有值」与
// 「无值」，用于 unit_rate 这类 0 与 NULL 同义但空串非法的数值列。
func optionalStringArg(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// timeArg 把可空时间转成驱动参数。
func timeArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

// insertID 统一处理写入结果与自增主键读取。
func insertID(res sql.Result, err error, table string) (uint64, error) {
	if err != nil {
		return 0, fmt.Errorf("store: 写入 %s 失败: %w", table, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: 读取 %s 自增 id 失败: %w", table, err)
	}
	if id < 0 {
		return 0, fmt.Errorf("store: %s 返回负的自增 id %d", table, id)
	}
	return uint64(id), nil
}

// Pricing 是 billing_pricing 的一行：某商家对某模型的一个定价版本。
type Pricing struct {
	ID          uint64     `json:"id"`
	MerchantID  uint64     `json:"merchant_id"`
	Model       string     `json:"model"`
	Version     int        `json:"version"`
	EffectiveAt time.Time  `json:"effective_at"`
	RetiredAt   *time.Time `json:"retired_at"`
}

// PriceComponent 是 billing_price_component 的一行。
//
// 单价是折算率：结算量 = usage[metric] × UnitPrice / BasisQty。
// TierFrom / TierTo / TierBasis 为空字符串即 SQL NULL，表示无阶梯。
type PriceComponent struct {
	ID         uint64
	PricingID  uint64
	Metric     billing.Metric
	UnitSettle billing.UnitSettle
	UnitPrice  string
	BasisQty   string
	TierFrom   string
	TierTo     string
	TierBasis  string
}

// PriceRule 是 billing_price_rule 的一行：条件倍率。
//
// Metric 为空字符串即 NULL（全部指标）；TimeFrom / TimeTo 是 "HH:MM:SS"，
// 空字符串即 NULL；WeekdayMask / DayKindMask 为 nil 即 NULL。
type PriceRule struct {
	ID          uint64         `json:"id"`
	Scope       billing.Scope  `json:"scope"`
	ScopeID     uint64         `json:"scope_id"`
	Metric      billing.Metric `json:"metric,omitempty"`
	Multiplier  string         `json:"multiplier"`
	ValidFrom   *time.Time     `json:"valid_from"`
	ValidTo     *time.Time     `json:"valid_to"`
	TimeFrom    string         `json:"time_from,omitempty"`
	TimeTo      string         `json:"time_to,omitempty"`
	WeekdayMask *uint8         `json:"weekday_mask"`
	DayKindMask *uint16        `json:"day_kind_mask"`
	Calendar    string         `json:"calendar,omitempty"`
	Priority    int            `json:"priority"`
}

// CalendarDay 是 sys_calendar 的一行。
type CalendarDay struct {
	Date    string          `json:"date"` // "2006-01-02"
	DayKind billing.DayKind `json:"day_kind"`
}

// Product 是 merchant_product 的一行：商品档位。
type Product struct {
	ID           uint64             `json:"id"`
	MerchantID   uint64             `json:"merchant_id"`
	Name         string             `json:"name"`
	Unit         billing.UnitSettle `json:"unit"`
	Qty          string             `json:"qty"`
	Price        string             `json:"price"`
	ModelScope   []byte             `json:"model_scope,omitempty"` // JSON；nil 即 NULL
	ValidityDays int                `json:"validity_days"`
}

// Purchase 是 account_purchase 的一行。
type Purchase struct {
	ID          uint64    `json:"id"`
	AccountID   uint64    `json:"account_id"`
	MerchantID  uint64    `json:"merchant_id"`
	ProductID   uint64    `json:"product_id"`
	Qty         string    `json:"qty"`
	PricePaid   string    `json:"price_paid"`
	PurchasedAt time.Time `json:"purchased_at"`
}

// Quota 是 account_quota 的一行：窗口型限额定义。
type Quota struct {
	ID          uint64
	Scope       billing.Scope
	ScopeID     uint64
	Metric      billing.Metric
	WindowKind  billing.WindowKind
	Period      billing.Period
	LimitAmount string
	Action      billing.Action
}

// QuotaEvent 是 account_quota_event 的一行：重置基准。
type QuotaEvent struct {
	ID         uint64
	QuotaID    uint64
	Event      billing.QuotaEvent
	BaselineAt time.Time
	Reason     string
	Operator   string
}

// Adjustment 是 billing_adjustment 的一行：调账。
type Adjustment struct {
	ID          uint64    `json:"id"`
	AccountID   uint64    `json:"account_id"`
	DeltaAmount string    `json:"delta_amount"`
	Reason      string    `json:"reason"`
	Operator    string    `json:"operator"`
	CreatedAt   time.Time `json:"created_at"`
}

// UsageRow 是 billing_usage 的一行：一次转发的用量事实。
//
// Usage 的键必须是 billing 的白名单指标，写入前逐个校验，脏键不落库。
// APIKeyID 为 0 表示这次转发没有可归属的 key，该行不计入 api_key 限额。
// 本条路径只写占位结算字段：pricing_id / gross_amount / multiplier / settlement
// 由 SQL 固定为 0 / 0 / 1 / NULL，不经调用方传入。带真实结算字段的写入见
// settlement.go 的 Tx.InsertUsage。
type UsageRow struct {
	MerchantID     uint64
	AccountID      uint64
	ChannelID      uint64
	APIKeyID       uint64
	Model          string
	RequestedModel string
	Protocol       string
	CrossProtocol  bool
	Usage          map[billing.Metric]int
}

// insertUsageSQL 把结算列写死为「未结算」形态。
//
// 不把 pricing_id 等做成占位符：本条路径的口径是「只落用量、不结算」，允许调用方
// 传值会给出「这里能结算」的假象。真实结算走 settlement.go 的显式参数语句。
const insertUsageSQL = "INSERT INTO billing_usage " +
	"(merchant_id, account_id, channel_id, api_key_id, model, requested_model, protocol, cross_protocol, `usage`, pricing_id, gross_amount, multiplier, settlement) " +
	"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 1, NULL)"

// validateUsageInput 校验一条用量流水的最小事实，占位与结算两条写入路径共用。
func validateUsageInput(merchantID, accountID, channelID uint64, model string, usage map[billing.Metric]int) error {
	if merchantID == 0 {
		return errors.New("store: billing_usage.merchant_id 不能为 0")
	}
	if accountID == 0 {
		return errors.New("store: billing_usage.account_id 不能为 0")
	}
	if channelID == 0 {
		return errors.New("store: billing_usage.channel_id 不能为 0")
	}
	if strings.TrimSpace(model) == "" {
		return errors.New("store: billing_usage.model 不能为空")
	}
	for metric := range usage {
		if err := billing.ValidateMetric(metric); err != nil {
			return err
		}
	}
	return nil
}

// encodeUsage 把用量分量序列化为 JSON 列值。
//
// 空 map 序列化为 {}，不是 null：usage 列是 NOT NULL 的 JSON，null 会被拒绝。
// nil map 也要换成空 map，否则 json.Marshal 会给出 null。
func encodeUsage(usage map[billing.Metric]int) ([]byte, error) {
	if usage == nil {
		usage = map[billing.Metric]int{}
	}
	payload, err := json.Marshal(usage)
	if err != nil {
		return nil, fmt.Errorf("store: 编码 billing_usage.usage 失败: %w", err)
	}
	return payload, nil
}

// insertUsage 写一条用量流水。
//
// usage 为空集合时仍然插入：billing_usage 一行即一次请求，次数即行数；
// 上游没给用量不等于这次请求没有发生。
func insertUsage(ctx context.Context, ex executor, row UsageRow) (uint64, error) {
	if err := validateUsageInput(row.MerchantID, row.AccountID, row.ChannelID, row.Model, row.Usage); err != nil {
		return 0, err
	}
	payload, err := encodeUsage(row.Usage)
	if err != nil {
		return 0, err
	}
	res, err := ex.ExecContext(ctx, insertUsageSQL,
		row.MerchantID, row.AccountID, row.ChannelID, row.APIKeyID, row.Model,
		nullableString(row.RequestedModel), nullableString(row.Protocol), row.CrossProtocol, payload)
	return insertID(res, err, "billing_usage")
}

// nullableString 把空串转成 SQL NULL。
func nullableString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// InsertUsage 写一条用量流水，返回新行 id。
func (s *Store) InsertUsage(ctx context.Context, row UsageRow) (uint64, error) {
	return insertUsage(ctx, s.db, row)
}

const insertPricingSQL = `INSERT INTO billing_pricing (merchant_id, model, version, effective_at) VALUES (?, ?, ?, ?)`

// insertPricing 写一条定价版本行。
//
// 刻意不在这里顺带置位旧版本的 retired_at：改价是「置位旧版 + 插新版」
// 两个动作，拆开是为了让调用方能在同一事务里控制先后，避免中途失败留下
// 无 active 定价的窗口。RetireActivePricing 提供置位动作。
func insertPricing(ctx context.Context, ex executor, p Pricing) (uint64, error) {
	if p.MerchantID == 0 {
		return 0, errors.New("store: billing_pricing.merchant_id 不能为 0")
	}
	if strings.TrimSpace(p.Model) == "" {
		return 0, errors.New("store: billing_pricing.model 不能为空")
	}
	if p.Version <= 0 {
		return 0, fmt.Errorf("store: billing_pricing.version 必须为正数，得到 %d", p.Version)
	}
	if p.EffectiveAt.IsZero() {
		return 0, errors.New("store: billing_pricing.effective_at 不能为零值")
	}
	res, err := ex.ExecContext(ctx, insertPricingSQL, p.MerchantID, p.Model, p.Version, p.EffectiveAt)
	return insertID(res, err, "billing_pricing")
}

// InsertPricing 发布一个定价版本，返回新行 id。
func (s *Store) InsertPricing(ctx context.Context, p Pricing) (uint64, error) {
	return insertPricing(ctx, s.db, p)
}

const retireActivePricingSQL = `UPDATE billing_pricing SET retired_at = ? WHERE merchant_id = ? AND model = ? AND retired_at IS NULL`

// RetireActivePricing 把该商家该模型当前所有 active 版本置为 retired。
func (s *Store) RetireActivePricing(ctx context.Context, merchantID uint64, model string, retiredAt time.Time) error {
	if retiredAt.IsZero() {
		return errors.New("store: retired_at 不能为零值")
	}
	if _, err := s.db.ExecContext(ctx, retireActivePricingSQL, retiredAt, merchantID, model); err != nil {
		return fmt.Errorf("store: 置位 billing_pricing.retired_at 失败: %w", err)
	}
	return nil
}

const activePricingSQL = `SELECT id, merchant_id, model, version, effective_at, retired_at
FROM billing_pricing
WHERE merchant_id = ? AND model = ? AND retired_at IS NULL AND effective_at <= ?
ORDER BY version DESC
LIMIT 1`

// activePricing 查某商家某模型在 asOf 时刻生效的定价版本。
//
// 生效时刻由调用方传入而不是用数据库的 NOW()：判定依赖数据库时钟会让
// 结算结果不可复现，也让补算历史流水无法指定时刻。无匹配时返回的错误
// 可用 errors.Is(err, sql.ErrNoRows) 判断。
func activePricing(ctx context.Context, q session, merchantID uint64, model string, asOf time.Time) (*Pricing, error) {
	row := q.QueryRowContext(ctx, activePricingSQL, merchantID, model, asOf)
	var (
		p           Pricing
		effectiveAt scanTime
		retiredAt   scanTime
	)
	err := row.Scan(&p.ID, &p.MerchantID, &p.Model, &p.Version, &effectiveAt, &retiredAt)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 billing_pricing 失败: %w", err)
	}
	if !effectiveAt.Valid {
		return nil, errors.New("store: billing_pricing.effective_at 为 NULL，与列定义不符")
	}
	p.EffectiveAt = effectiveAt.Time
	if retiredAt.Valid {
		p.RetiredAt = &retiredAt.Time
	}
	return &p, nil
}

// ActivePricing 查某商家某模型在 asOf 时刻生效的定价版本。
func (s *Store) ActivePricing(ctx context.Context, merchantID uint64, model string, asOf time.Time) (*Pricing, error) {
	return activePricing(ctx, s.db, merchantID, model, asOf)
}

const priceComponentPlaceholder = "(?, ?, ?, ?, ?, ?, ?, ?)"

// priceComponentColumns 与占位符的列数一致，仅用于预分配参数切片。
const priceComponentColumns = 8

const insertPriceComponentPrefix = `INSERT INTO billing_price_component
  (pricing_id, metric, unit_settle, unit_price, basis_qty, tier_from, tier_to, tier_basis)
VALUES `

// insertPriceComponents 批量写计价分量，整批校验通过才拼 SQL。
//
// 一次定价发布的分量是一个整体，拆成多次 Exec 会在中途失败时留下半个
// 定价；批量写让调用方在单个事务内一次完成。空切片直接返回，不产生
// 非法 SQL。
func insertPriceComponents(ctx context.Context, ex executor, components []PriceComponent) error {
	if len(components) == 0 {
		return nil
	}
	for i, c := range components {
		if c.PricingID == 0 {
			return fmt.Errorf("store: 第 %d 个计价分量的 pricing_id 不能为 0", i+1)
		}
		if err := billing.ValidateMetric(c.Metric); err != nil {
			return fmt.Errorf("store: 第 %d 个计价分量: %w", i+1, err)
		}
		if err := billing.ValidateUnitSettle(c.UnitSettle); err != nil {
			return fmt.Errorf("store: 第 %d 个计价分量: %w", i+1, err)
		}
		if strings.TrimSpace(c.UnitPrice) == "" {
			return fmt.Errorf("store: 第 %d 个计价分量的 unit_price 不能为空", i+1)
		}
		if strings.TrimSpace(c.BasisQty) == "" {
			return fmt.Errorf("store: 第 %d 个计价分量的 basis_qty 不能为空", i+1)
		}
	}

	var b strings.Builder
	b.WriteString(insertPriceComponentPrefix)
	args := make([]any, 0, len(components)*priceComponentColumns)
	for i, c := range components {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(priceComponentPlaceholder)
		args = append(args, c.PricingID, c.Metric, c.UnitSettle, c.UnitPrice, c.BasisQty,
			nullArg(c.TierFrom), nullArg(c.TierTo), nullArg(c.TierBasis))
	}
	if _, err := ex.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("store: 写入 billing_price_component 失败: %w", err)
	}
	return nil
}

// InsertPriceComponents 批量写入某个定价版本的计价分量。
func (s *Store) InsertPriceComponents(ctx context.Context, components []PriceComponent) error {
	return insertPriceComponents(ctx, s.db, components)
}

const priceComponentsSQL = `SELECT id, pricing_id, metric, unit_settle, unit_price, basis_qty, tier_from, tier_to, tier_basis
FROM billing_price_component
WHERE pricing_id = ?
ORDER BY id`

// priceComponents 查某个定价版本的全部分量。
func priceComponents(ctx context.Context, q session, pricingID uint64) ([]PriceComponent, error) {
	rows, err := q.QueryContext(ctx, priceComponentsSQL, pricingID)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 billing_price_component 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var components []PriceComponent
	for rows.Next() {
		var (
			c                           PriceComponent
			tierFrom, tierTo, tierBasis sql.NullString
			metricRaw, unitSettleRaw    string
		)
		if err := rows.Scan(&c.ID, &c.PricingID, &metricRaw, &unitSettleRaw, &c.UnitPrice, &c.BasisQty,
			&tierFrom, &tierTo, &tierBasis); err != nil {
			return nil, fmt.Errorf("store: 解析 billing_price_component 行失败: %w", err)
		}
		c.Metric = billing.MetricFromDB(metricRaw)
		c.UnitSettle = billing.UnitSettleFromDB(unitSettleRaw)
		c.TierFrom = tierFrom.String
		c.TierTo = tierTo.String
		c.TierBasis = tierBasis.String
		components = append(components, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 billing_price_component 行失败: %w", err)
	}
	return components, nil
}

// PriceComponents 查某个定价版本的全部分量。
func (s *Store) PriceComponents(ctx context.Context, pricingID uint64) ([]PriceComponent, error) {
	return priceComponents(ctx, s.db, pricingID)
}

const insertPriceRuleSQL = `INSERT INTO billing_price_rule
  (scope, scope_id, metric, multiplier, valid_from, valid_to, time_from, time_to, weekday_mask, day_kind_mask, calendar, priority)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// insertPriceRule 写一条条件倍率规则。
func insertPriceRule(ctx context.Context, ex executor, r PriceRule) (uint64, error) {
	if err := billing.ValidateScope(r.Scope); err != nil {
		return 0, err
	}
	if r.ScopeID == 0 {
		return 0, errors.New("store: billing_price_rule.scope_id 不能为 0")
	}
	if r.Metric != "" {
		if err := billing.ValidateMetric(r.Metric); err != nil {
			return 0, err
		}
	}
	if strings.TrimSpace(r.Multiplier) == "" {
		return 0, errors.New("store: billing_price_rule.multiplier 不能为空")
	}
	res, err := ex.ExecContext(ctx, insertPriceRuleSQL,
		r.Scope, r.ScopeID, nullArg(string(r.Metric)), r.Multiplier,
		timeArg(r.ValidFrom), timeArg(r.ValidTo),
		nullArg(r.TimeFrom), nullArg(r.TimeTo),
		r.WeekdayMask, r.DayKindMask, nullArg(r.Calendar), r.Priority)
	return insertID(res, err, "billing_price_rule")
}

// InsertPriceRule 新增一条条件倍率规则。
func (s *Store) InsertPriceRule(ctx context.Context, r PriceRule) (uint64, error) {
	return insertPriceRule(ctx, s.db, r)
}

// DeletePriceRule 按 id 删除一条规则。
func (s *Store) DeletePriceRule(ctx context.Context, id uint64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM billing_price_rule WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: 删除 billing_price_rule 失败: %w", err)
	}
	return nil
}

const priceRulesSQL = `SELECT id, scope, scope_id, metric, multiplier, valid_from, valid_to, time_from, time_to, weekday_mask, day_kind_mask, calendar, priority
FROM billing_price_rule
WHERE scope = ? AND scope_id = ?
ORDER BY priority DESC, id`

// priceRulesByScope 查某个 scope 下的全部规则，按优先级从高到低。
//
// 只做过滤不做匹配：命中的选取与组合是规则匹配函数的职责。
func priceRulesByScope(ctx context.Context, q session, scope billing.Scope, scopeID uint64) ([]PriceRule, error) {
	if err := billing.ValidateScope(scope); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, priceRulesSQL, scope, scopeID)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 billing_price_rule 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var rules []PriceRule
	for rows.Next() {
		var (
			r                                    PriceRule
			scopeRaw, metricRaw, calendarRaw     sql.NullString
			validFrom, validTo, timeFrom, timeTo scanTime
			weekdayMask, dayKindMask             sql.NullByte
		)
		if err := rows.Scan(&r.ID, &scopeRaw, &r.ScopeID, &metricRaw, &r.Multiplier,
			&validFrom, &validTo, &timeFrom, &timeTo,
			&weekdayMask, &dayKindMask, &calendarRaw, &r.Priority); err != nil {
			return nil, fmt.Errorf("store: 解析 billing_price_rule 行失败: %w", err)
		}
		r.Scope = billing.ScopeFromDB(scopeRaw.String)
		r.Metric = billing.MetricFromDB(metricRaw.String)
		r.Calendar = calendarRaw.String
		if validFrom.Valid {
			r.ValidFrom = &validFrom.Time
		}
		if validTo.Valid {
			r.ValidTo = &validTo.Time
		}
		// TIME 列只取时分秒，日期部分无意义。
		if timeFrom.Valid {
			r.TimeFrom = timeFrom.Time.Format("15:04:05")
		}
		if timeTo.Valid {
			r.TimeTo = timeTo.Time.Format("15:04:05")
		}
		if weekdayMask.Valid {
			v := weekdayMask.Byte
			r.WeekdayMask = &v
		}
		if dayKindMask.Valid {
			// TINYINT UNSIGNED 列最大 255，宽化到 uint16 不丢位。
			v := uint16(dayKindMask.Byte)
			r.DayKindMask = &v
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 billing_price_rule 行失败: %w", err)
	}
	return rules, nil
}

// PriceRulesByScope 查某个 scope 下的全部规则，按优先级从高到低。
//
// 只做过滤不做匹配：命中的选取与组合是规则匹配函数的职责。
func (s *Store) PriceRulesByScope(ctx context.Context, scope billing.Scope, scopeID uint64) ([]PriceRule, error) {
	return priceRulesByScope(ctx, s.db, scope, scopeID)
}

const calendarPlaceholder = "(?, ?, ?)"

// calendarColumns 与占位符的列数一致，仅用于预分配参数切片。
const calendarColumns = 3

const upsertCalendarPrefix = "INSERT INTO sys_calendar (calendar, `date`, day_kind) VALUES "

// upsertCalendarSuffix 用 VALUES() 而不是新式别名语法：别名语法要求
// MySQL 8.0.19+，本仓库未把最低版本钉到那里。VALUES() 在新版本只是告警。
const upsertCalendarSuffix = " ON DUPLICATE KEY UPDATE day_kind = VALUES(day_kind)"

// upsertCalendarDays 批量 upsert 日历日期。
//
// 放假通知是一次写一段日期区间，逐行 upsert 会增加往返与半写窗口；
// 批量一条语句让整段日期在同一事务语义内落地。
func upsertCalendarDays(ctx context.Context, ex executor, calendar string, days []CalendarDay) error {
	if strings.TrimSpace(calendar) == "" {
		return errors.New("store: sys_calendar.calendar 不能为空")
	}
	if len(days) == 0 {
		return nil
	}
	for i, d := range days {
		if strings.TrimSpace(d.Date) == "" {
			return fmt.Errorf("store: 第 %d 个日历日期的 date 不能为空", i+1)
		}
		if err := billing.ValidateDayKind(d.DayKind); err != nil {
			return fmt.Errorf("store: 第 %d 个日历日期: %w", i+1, err)
		}
	}

	var b strings.Builder
	b.WriteString(upsertCalendarPrefix)
	args := make([]any, 0, len(days)*calendarColumns)
	for i, d := range days {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(calendarPlaceholder)
		args = append(args, calendar, d.Date, d.DayKind)
	}
	b.WriteString(upsertCalendarSuffix)
	if _, err := ex.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("store: 写入 sys_calendar 失败: %w", err)
	}
	return nil
}

// UpsertCalendarDays 批量写入或更新日历日期。
func (s *Store) UpsertCalendarDays(ctx context.Context, calendar string, days []CalendarDay) error {
	return upsertCalendarDays(ctx, s.db, calendar, days)
}

const calendarDaysSQL = `SELECT ` + "`date`" + `, day_kind FROM sys_calendar WHERE calendar = ? AND ` + "`date`" + ` >= ? AND ` + "`date`" + ` <= ? ORDER BY ` + "`date`"

// CalendarDays 查某日历在 [from, to] 闭区间内的日期，from / to 形如 "2006-01-02"。
func (s *Store) CalendarDays(ctx context.Context, calendar, from, to string) ([]CalendarDay, error) {
	rows, err := s.db.QueryContext(ctx, calendarDaysSQL, calendar, from, to)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 sys_calendar 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var days []CalendarDay
	for rows.Next() {
		var (
			date    scanTime
			kindRaw string
		)
		if err := rows.Scan(&date, &kindRaw); err != nil {
			return nil, fmt.Errorf("store: 解析 sys_calendar 行失败: %w", err)
		}
		days = append(days, CalendarDay{
			Date:    date.Time.Format("2006-01-02"),
			DayKind: billing.DayKindFromDB(kindRaw),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 sys_calendar 行失败: %w", err)
	}
	return days, nil
}

const insertProductSQL = `INSERT INTO merchant_product (merchant_id, name, unit, qty, price, model_scope, validity_days)
VALUES (?, ?, ?, ?, ?, ?, ?)`

// insertProduct 写一条商品档位。
func insertProduct(ctx context.Context, ex executor, p Product) (uint64, error) {
	if p.MerchantID == 0 {
		return 0, errors.New("store: merchant_product.merchant_id 不能为 0")
	}
	if strings.TrimSpace(p.Name) == "" {
		return 0, errors.New("store: merchant_product.name 不能为空")
	}
	if err := billing.ValidateUnitSettle(p.Unit); err != nil {
		return 0, err
	}
	if strings.TrimSpace(p.Qty) == "" {
		return 0, errors.New("store: merchant_product.qty 不能为空")
	}
	if strings.TrimSpace(p.Price) == "" {
		return 0, errors.New("store: merchant_product.price 不能为空")
	}
	if p.ValidityDays < 0 {
		return 0, fmt.Errorf("store: merchant_product.validity_days 不能为负数，得到 %d", p.ValidityDays)
	}
	var modelScope any
	if len(p.ModelScope) > 0 {
		modelScope = []byte(p.ModelScope)
	}
	res, err := ex.ExecContext(ctx, insertProductSQL, p.MerchantID, p.Name, p.Unit, p.Qty, p.Price, modelScope, p.ValidityDays)
	return insertID(res, err, "merchant_product")
}

// InsertProduct 上架一个商品档位。
func (s *Store) InsertProduct(ctx context.Context, p Product) (uint64, error) {
	return insertProduct(ctx, s.db, p)
}

const productSQL = `SELECT id, merchant_id, name, unit, qty, price, model_scope, validity_days
FROM merchant_product
WHERE id = ?`

// Product 按 id 查商品档位；无匹配时错误可用 errors.Is(err, sql.ErrNoRows) 判断。
func (s *Store) Product(ctx context.Context, id uint64) (*Product, error) {
	var (
		p        Product
		unitRaw  string
		scopeRaw []byte
	)
	err := s.db.QueryRowContext(ctx, productSQL, id).Scan(
		&p.ID, &p.MerchantID, &p.Name, &unitRaw, &p.Qty, &p.Price, &scopeRaw, &p.ValidityDays)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 merchant_product 失败: %w", err)
	}
	p.Unit = billing.UnitSettleFromDB(unitRaw)
	p.ModelScope = scopeRaw
	return &p, nil
}

const insertPurchaseSQL = `INSERT INTO account_purchase (account_id, merchant_id, product_id, qty, price_paid, purchased_at)
VALUES (?, ?, ?, ?, ?, ?)`

// insertPurchase 写一条购买记录。
func insertPurchase(ctx context.Context, ex executor, p Purchase) (uint64, error) {
	if p.AccountID == 0 {
		return 0, errors.New("store: account_purchase.account_id 不能为 0")
	}
	if p.MerchantID == 0 {
		return 0, errors.New("store: account_purchase.merchant_id 不能为 0")
	}
	if p.ProductID == 0 {
		return 0, errors.New("store: account_purchase.product_id 不能为 0")
	}
	if strings.TrimSpace(p.Qty) == "" {
		return 0, errors.New("store: account_purchase.qty 不能为空")
	}
	if strings.TrimSpace(p.PricePaid) == "" {
		return 0, errors.New("store: account_purchase.price_paid 不能为空")
	}
	if p.PurchasedAt.IsZero() {
		return 0, errors.New("store: account_purchase.purchased_at 不能为零值")
	}
	res, err := ex.ExecContext(ctx, insertPurchaseSQL,
		p.AccountID, p.MerchantID, p.ProductID, p.Qty, p.PricePaid, p.PurchasedAt)
	return insertID(res, err, "account_purchase")
}

// InsertPurchase 记录一次购买。
func (s *Store) InsertPurchase(ctx context.Context, p Purchase) (uint64, error) {
	return insertPurchase(ctx, s.db, p)
}

const insertQuotaSQL = `INSERT INTO account_quota (scope, scope_id, metric, window_kind, period, limit_amount, action)
VALUES (?, ?, ?, ?, ?, ?, ?)`

// insertQuota 写一条窗口型限额定义。
func insertQuota(ctx context.Context, ex executor, q Quota) (uint64, error) {
	if err := billing.ValidateScope(q.Scope); err != nil {
		return 0, err
	}
	if q.ScopeID == 0 {
		return 0, errors.New("store: account_quota.scope_id 不能为 0")
	}
	if err := billing.ValidateMetric(q.Metric); err != nil {
		return 0, err
	}
	if err := billing.ValidateWindowKind(q.WindowKind); err != nil {
		return 0, err
	}
	if err := billing.ValidatePeriod(q.Period); err != nil {
		return 0, err
	}
	if strings.TrimSpace(q.LimitAmount) == "" {
		return 0, errors.New("store: account_quota.limit_amount 不能为空")
	}
	if err := billing.ValidateAction(q.Action); err != nil {
		return 0, err
	}
	res, err := ex.ExecContext(ctx, insertQuotaSQL,
		q.Scope, q.ScopeID, q.Metric, q.WindowKind, q.Period, q.LimitAmount, q.Action)
	return insertID(res, err, "account_quota")
}

// InsertQuota 新增一条窗口型限额定义。
func (s *Store) InsertQuota(ctx context.Context, q Quota) (uint64, error) {
	return insertQuota(ctx, s.db, q)
}

const insertQuotaEventSQL = "INSERT INTO account_quota_event (quota_id, `event`, baseline_at, reason, operator) VALUES (?, ?, ?, ?, ?)"

// insertQuotaEvent 写一条限额重置事件。
//
// operator 必填：额度变更只追加事实，审计链要能回答「谁在什么时候重置的」。
func insertQuotaEvent(ctx context.Context, ex executor, e QuotaEvent) (uint64, error) {
	if e.QuotaID == 0 {
		return 0, errors.New("store: account_quota_event.quota_id 不能为 0")
	}
	if err := billing.ValidateQuotaEvent(e.Event); err != nil {
		return 0, err
	}
	if e.BaselineAt.IsZero() {
		return 0, errors.New("store: account_quota_event.baseline_at 不能为零值")
	}
	if strings.TrimSpace(e.Operator) == "" {
		return 0, errors.New("store: account_quota_event.operator 不能为空")
	}
	res, err := ex.ExecContext(ctx, insertQuotaEventSQL, e.QuotaID, e.Event, e.BaselineAt, e.Reason, e.Operator)
	return insertID(res, err, "account_quota_event")
}

// InsertQuotaEvent 追加一条限额重置事件。
func (s *Store) InsertQuotaEvent(ctx context.Context, e QuotaEvent) (uint64, error) {
	return insertQuotaEvent(ctx, s.db, e)
}

const insertAdjustmentSQL = `INSERT INTO billing_adjustment (account_id, delta_amount, reason, operator, created_at)
VALUES (?, ?, ?, ?, ?)`

// insertAdjustment 写一条调账流水。
func insertAdjustment(ctx context.Context, ex executor, a Adjustment) (uint64, error) {
	if a.AccountID == 0 {
		return 0, errors.New("store: billing_adjustment.account_id 不能为 0")
	}
	if strings.TrimSpace(a.DeltaAmount) == "" {
		return 0, errors.New("store: billing_adjustment.delta_amount 不能为空")
	}
	if strings.TrimSpace(a.Reason) == "" {
		return 0, errors.New("store: billing_adjustment.reason 不能为空")
	}
	if strings.TrimSpace(a.Operator) == "" {
		return 0, errors.New("store: billing_adjustment.operator 不能为空")
	}
	if a.CreatedAt.IsZero() {
		return 0, errors.New("store: billing_adjustment.created_at 不能为零值")
	}
	res, err := ex.ExecContext(ctx, insertAdjustmentSQL,
		a.AccountID, a.DeltaAmount, a.Reason, a.Operator, a.CreatedAt)
	return insertID(res, err, "billing_adjustment")
}

// InsertAdjustment 追加一条调账流水。
func (s *Store) InsertAdjustment(ctx context.Context, a Adjustment) (uint64, error) {
	return insertAdjustment(ctx, s.db, a)
}

// AccountUsageFilter 是账户面用量列表的过滤条件。
//
// 与管理面的 ListUsage 分开：管理面按账户与起始时刻粗筛，随后原样展示 JSON；
// 账户面要按模型、密钥与闭区间精确筛选，还要总数做偏移分页。两者合并会让任一侧
// 为了另一侧的需要带上不用的分支，故各留一份。
type AccountUsageFilter struct {
	// AccountID 是流水的归属账户，必填：作用域只能来自会话。
	AccountID uint64
	// Since / Until 是写入时刻的闭区间；零值表示该侧不限。
	Since time.Time
	Until time.Time
	// RequestedModel 按客户端请求的模型名精确匹配；空串表示不过滤。
	//
	// 匹配的是 requested_model 而不是 model：页面上露出的模型名是客户端写的那个，
	// 按履约模型过滤会让用户按自己看到的名字筛不出自己的流水。
	RequestedModel string
	// APIKeyID 按签发本次调用的密钥过滤；0 表示不过滤。
	APIKeyID uint64
	// Limit / Offset 是偏移分页参数；Limit 必须为正。
	Limit  int
	Offset int
}

// AccountUsageRow 是账户面用量列表的一行。
//
// 不含渠道与商家标识：它们是运营口径，账户面不暴露。
type AccountUsageRow struct {
	ID uint64
	// Model 是实际履约的上游模型名，RequestedModel 是客户端请求的模型名。
	// 两者不同表示网关改写过模型名。
	Model          string
	RequestedModel string
	// Protocol 是客户端使用的线协议；历史行没有该列取值，读回空串。
	Protocol      string
	CrossProtocol bool
	// Usage 是原始的 metric -> 数量 JSON，由调用方映射为对外字段。
	Usage json.RawMessage
	// GrossAmount 是基础价 × 用量，Multiplier 是解析后的最终倍率；
	// 两者保持数据库文本，扣减量由调用方折算。
	GrossAmount string
	Multiplier  string
	CreatedAt   time.Time
}

// accountIDCondition 是账户维度的过滤谓词：账户面的流水、模型与请求记录读路径
// 都按它收敛作用域，写成一个常量让「作用域必须带账户」这条规则只有一处出处。
const accountIDCondition = "account_id = ?"

// accountUsageColumns 是账户面列表的列清单，计数与取页共用同一份谓词。
const accountUsageColumns = `SELECT id, model, requested_model, protocol, cross_protocol, ` + "`usage`" +
	`, gross_amount, multiplier, created_at
FROM billing_usage`

// accountUsageWhere 组装账户面列表的 WHERE 子句与参数。
//
// 单独抽出来是为了让计数与取页必然同源：两处各拼一次谓词，任一处漏条件都会让
// total 与当页对不上，而那种偏差只在特定过滤组合下出现。
func accountUsageWhere(f AccountUsageFilter) (string, []any) {
	conditions := []string{accountIDCondition}
	args := []any{f.AccountID}
	if !f.Since.IsZero() {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, f.Since)
	}
	if !f.Until.IsZero() {
		conditions = append(conditions, "created_at <= ?")
		args = append(args, f.Until)
	}
	if f.RequestedModel != "" {
		conditions = append(conditions, "requested_model = ?")
		args = append(args, f.RequestedModel)
	}
	if f.APIKeyID != 0 {
		conditions = append(conditions, "api_key_id = ?")
		args = append(args, f.APIKeyID)
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

// ListAccountUsage 按账户分页列出用量流水，返回当页行与满足条件的总数。
//
// 排序按主键倒序：写入时刻相同的两行（同一秒内落库）也有确定顺序，
// 翻页不会因排序不稳定而重复或漏行。主键与写入顺序一致，故与契约的
// 「按写入时刻倒序」等价。
func (s *Store) ListAccountUsage(ctx context.Context, f AccountUsageFilter) ([]AccountUsageRow, int, error) {
	if f.AccountID == 0 {
		return nil, 0, errors.New("store: billing_usage.account_id 不能为 0")
	}
	if f.Limit <= 0 {
		return nil, 0, errors.New("store: 分页条数必须为正")
	}
	if f.Offset < 0 {
		return nil, 0, errors.New("store: 分页偏移不能为负")
	}
	where, args := accountUsageWhere(f)

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM billing_usage"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: 统计 billing_usage 失败: %w", err)
	}

	//nolint:gosec // G202：拼进去的是列清单与由常量组成的谓词，取值一律走问号占位符。
	query := accountUsageColumns + where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	rows, err := s.db.QueryContext(ctx, query, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: 查询 billing_usage 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []AccountUsageRow
	for rows.Next() {
		var (
			r              AccountUsageRow
			requestedModel sql.NullString
			protocol       sql.NullString
			usageRaw       []byte
			createdAt      scanTime
		)
		if err := rows.Scan(&r.ID, &r.Model, &requestedModel, &protocol, &r.CrossProtocol,
			&usageRaw, &r.GrossAmount, &r.Multiplier, &createdAt); err != nil {
			return nil, 0, fmt.Errorf("store: 解析 billing_usage 行失败: %w", err)
		}
		// requested_model 与 protocol 是 0008 迁移新增的可空列：历史行为 NULL，
		// 读回空串表示「那一列没有取值」，不是「模型名为空」这种业务事实。
		r.RequestedModel = requestedModel.String
		r.Protocol = protocol.String
		r.Usage = json.RawMessage(usageRaw)
		r.CreatedAt = createdAt.Time
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: 遍历 billing_usage 行失败: %w", err)
	}
	return records, total, nil
}
