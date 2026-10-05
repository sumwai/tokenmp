package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/settlement"
)

// 本文件是结算路径的事务与查询入口。
//
// store 不承载结算规则：金额怎么算、倍率怎么链、账本按什么顺序扣都在
// internal/settlement；这里只把它的数据面接口实现为参数化 SQL，并保证多次读写
// 落在同一个事务里。抽取成独立的 Tx 类型而不是给 Store 加锁字段：
// 事务句柄只在一段结算内有效，塞进 Store 会让连接池与事务生命周期混在一起。

// Tx 是一段数据库事务，方法集合是结算路径需要的最小读写面。
type Tx struct {
	tx *sql.Tx
}

// 编译期断言：存储层实现结算器声明的数据面。
var (
	_ settlement.Repo = (*Store)(nil)
	_ settlement.Tx   = (*Tx)(nil)
)

// BeginTx 开启一段事务。
func (s *Store) BeginTx(ctx context.Context) (*Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: 开启事务失败: %w", err)
	}
	return &Tx{tx: tx}, nil
}

// InTx 在单个事务内执行 fn；fn 返回错误时回滚并返回该错误。
//
// 回滚错误不覆盖 fn 的错误：调用方关心的是「结算为什么失败」，
// 回滚失败只说明连接可能已断，忽略即可。
func (s *Store) InTx(ctx context.Context, fn func(context.Context, settlement.Tx) error) error {
	tx, err := s.BeginTx(ctx)
	if err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// Commit 提交事务。
func (t *Tx) Commit() error {
	if err := t.tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交事务失败: %w", err)
	}
	return nil
}

// Rollback 回滚事务。事务已结束时返回 sql.ErrTxDone，调用方通常忽略。
func (t *Tx) Rollback() error {
	if err := t.tx.Rollback(); err != nil {
		return fmt.Errorf("store: 回滚事务失败: %w", err)
	}
	return nil
}

// parseDecimal 把 DECIMAL 列的文本转成 decimal；解析失败按数据错误报出。
func parseDecimal(raw string) (decimal.Decimal, error) {
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, fmt.Errorf("store: 无法解析小数 %q: %w", raw, err)
	}
	return value, nil
}

const lockAccountSQL = "SELECT price_multiplier FROM account WHERE id = ? FOR UPDATE"

// LockAccount 锁定账户行并返回账户倍率。
//
// 同一账户的并发结算由这行 FOR UPDATE 串行化；锁在事务提交或回滚时释放。
func (t *Tx) LockAccount(ctx context.Context, accountID uint64) (decimal.Decimal, error) {
	var raw string
	if err := t.tx.QueryRowContext(ctx, lockAccountSQL, accountID).Scan(&raw); err != nil {
		return decimal.Zero, fmt.Errorf("store: 锁定 account 行失败: %w", err)
	}
	return parseDecimal(raw)
}

// ActivePricing 读生效定价及其全部分量。
//
// 无生效版本时返回 settlement.ErrNoPricing，由调用方按「无定价」口径处理，
// 不当作故障。
func (t *Tx) ActivePricing(ctx context.Context, merchantID uint64, model string, asOf time.Time) (*settlement.Pricing, error) {
	pricing, err := activePricing(ctx, t.tx, merchantID, model, asOf)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, settlement.ErrNoPricing
		}
		return nil, err
	}
	rows, err := priceComponents(ctx, t.tx, pricing.ID)
	if err != nil {
		return nil, err
	}
	components, err := convertComponents(rows)
	if err != nil {
		return nil, err
	}
	return &settlement.Pricing{ID: pricing.ID, Version: pricing.Version, Components: components}, nil
}

// convertComponents 把存储行转成结算分量，标度按 decimal 解析。
func convertComponents(rows []PriceComponent) ([]settlement.Component, error) {
	components := make([]settlement.Component, 0, len(rows))
	for _, row := range rows {
		unitPrice, err := parseDecimal(row.UnitPrice)
		if err != nil {
			return nil, err
		}
		basisQty, err := parseDecimal(row.BasisQty)
		if err != nil {
			return nil, err
		}
		components = append(components, settlement.Component{
			ID:         row.ID,
			Metric:     row.Metric,
			UnitSettle: row.UnitSettle,
			UnitPrice:  unitPrice,
			BasisQty:   basisQty,
			TierFrom:   row.TierFrom,
			TierTo:     row.TierTo,
			TierBasis:  row.TierBasis,
		})
	}
	return components, nil
}

// PriceRulesByScope 读某个 scope 下的规则并转成结算规则。
func (t *Tx) PriceRulesByScope(ctx context.Context, scope billing.Scope, scopeID uint64) ([]settlement.Rule, error) {
	rows, err := priceRulesByScope(ctx, t.tx, scope, scopeID)
	if err != nil {
		return nil, err
	}
	rules := make([]settlement.Rule, 0, len(rows))
	for _, row := range rows {
		multiplier, err := parseDecimal(row.Multiplier)
		if err != nil {
			return nil, err
		}
		rules = append(rules, settlement.Rule{
			ID:          row.ID,
			Scope:       row.Scope,
			ScopeID:     row.ScopeID,
			Metric:      row.Metric,
			Multiplier:  multiplier,
			ValidFrom:   row.ValidFrom,
			ValidTo:     row.ValidTo,
			TimeFrom:    row.TimeFrom,
			TimeTo:      row.TimeTo,
			WeekdayMask: row.WeekdayMask,
			DayKindMask: row.DayKindMask,
			Calendar:    row.Calendar,
			Priority:    row.Priority,
		})
	}
	return rules, nil
}

const modelMapMultiplierSQL = `SELECT price_multiplier FROM upstream_model_map
WHERE channel_id = ? AND model = ? AND enabled = 1`

// ModelMapMultiplier 读渠道模型映射的倍率。
//
// 没有映射时返回 1：渠道倍率是可选加成，缺失不应把整次结算变成失败。
func (t *Tx) ModelMapMultiplier(ctx context.Context, channelID uint64, model string) (decimal.Decimal, error) {
	var raw string
	err := t.tx.QueryRowContext(ctx, modelMapMultiplierSQL, channelID, model).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return decimal.NewFromInt(1), nil
	}
	if err != nil {
		return decimal.Zero, fmt.Errorf("store: 查询 upstream_model_map 失败: %w", err)
	}
	return parseDecimal(raw)
}

const calendarDaySQL = "SELECT day_kind FROM sys_calendar WHERE calendar = ? AND `date` = ?"

// CalendarDay 读日历某日的性质；无该日数据时 found 为 false。
func (t *Tx) CalendarDay(ctx context.Context, calendar, date string) (billing.DayKind, bool, error) {
	var raw string
	err := t.tx.QueryRowContext(ctx, calendarDaySQL, calendar, date).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return billing.DayKindWorkday, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: 查询 sys_calendar 失败: %w", err)
	}
	return billing.DayKindFromDB(raw), true, nil
}

// Bucket 是 account_bucket 的一行：账户的一笔可消耗存量。
//
// 只保留结算需要的列；account_id 由查询条件给出，不再回读。
type Bucket struct {
	ID        uint64
	Unit      billing.UnitSettle
	Remaining string
	ExpiresAt *time.Time
	Fallback  billing.Fallback
	Priority  int
}

const bucketColumns = "id, unit, remaining, expires_at, fallback, priority, unit_rate"

const lockBucketsSQL = "SELECT " + bucketColumns + " FROM account_bucket WHERE account_id = ? " +
	"ORDER BY (expires_at IS NULL), expires_at, priority, id FOR UPDATE"

const accountBucketsSQL = "SELECT " + bucketColumns + " FROM account_bucket WHERE account_id = ? " +
	"ORDER BY (expires_at IS NULL), expires_at, priority, id"

// LockBuckets 锁定并返回账户的全部账本行。
//
// 排序在 SQL 与纯函数里各做一次：SQL 的排序让锁的获取顺序稳定（并发的两个
// 结算不会交叉持锁），纯函数的排序保证即便存储层换了实现，扣减序也只由规则决定。
func (t *Tx) LockBuckets(ctx context.Context, accountID uint64) ([]settlement.Bucket, error) {
	rows, err := t.tx.QueryContext(ctx, lockBucketsSQL, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: 锁定 account_bucket 失败: %w", err)
	}
	return toSettlementBuckets(rows)
}

// AccountBuckets 读账户的全部账本行，不加锁，供转发前的额度预检使用。
func (s *Store) AccountBuckets(ctx context.Context, accountID uint64) ([]settlement.Bucket, error) {
	rows, err := s.db.QueryContext(ctx, accountBucketsSQL, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 account_bucket 失败: %w", err)
	}
	return toSettlementBuckets(rows)
}

func toSettlementBuckets(rows *sql.Rows) ([]settlement.Bucket, error) {
	defer func() { _ = rows.Close() }()
	var buckets []settlement.Bucket
	for rows.Next() {
		var (
			bucket    settlement.Bucket
			unitRaw   string
			fallback  string
			remaining string
			expiresAt scanTime
			unitRate  sql.NullString
		)
		if err := rows.Scan(&bucket.ID, &unitRaw, &remaining, &expiresAt,
			&fallback, &bucket.Priority, &unitRate); err != nil {
			return nil, fmt.Errorf("store: 解析 account_bucket 行失败: %w", err)
		}
		value, err := parseDecimal(remaining)
		if err != nil {
			return nil, err
		}
		bucket.Unit = billing.UnitSettleFromDB(unitRaw)
		bucket.Fallback = billing.FallbackFromDB(fallback)
		bucket.Remaining = value
		if expiresAt.Valid {
			bucket.ExpiresAt = &expiresAt.Time
		}
		if unitRate.Valid {
			rate, err := parseDecimal(unitRate.String)
			if err != nil {
				return nil, err
			}
			bucket.UnitRate = rate
		}
		buckets = append(buckets, bucket)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 account_bucket 行失败: %w", err)
	}
	return buckets, nil
}

const updateBucketRemainingSQL = "UPDATE account_bucket SET remaining = ? WHERE id = ?"

// UpdateBucketRemaining 更新账本存量；允许把 currency 账本写成负数（后付透支）。
func (t *Tx) UpdateBucketRemaining(ctx context.Context, bucketID uint64, remaining decimal.Decimal) error {
	if _, err := t.tx.ExecContext(ctx, updateBucketRemainingSQL, remaining.String(), bucketID); err != nil {
		return fmt.Errorf("store: 更新 account_bucket 失败: %w", err)
	}
	return nil
}

const insertSettledUsageSQL = "INSERT INTO billing_usage " +
	"(merchant_id, account_id, channel_id, model, `usage`, pricing_id, pricing_snapshot, gross_amount, multiplier, settlement) " +
	"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

// InsertUsage 写一条带结算字段的用量流水。
//
// 结算字段由 internal/settlement 计算后原样写入：store 不参与算术，
// 也不改写金额口径，保证流水与结算函数给出一致的结果。
func (t *Tx) InsertUsage(ctx context.Context, row settlement.Usage) (uint64, error) {
	if err := validateUsageInput(row.MerchantID, row.AccountID, row.ChannelID, row.Model, row.Metrics); err != nil {
		return 0, err
	}
	payload, err := encodeUsage(row.Metrics)
	if err != nil {
		return 0, err
	}
	res, err := t.tx.ExecContext(ctx, insertSettledUsageSQL,
		row.MerchantID, row.AccountID, row.ChannelID, row.Model, payload,
		row.PricingID, nullableJSON(row.PricingSnapshot), row.GrossAmount.String(),
		row.Multiplier.String(), nullableJSON(row.Settlement))
	return insertID(res, err, "billing_usage")
}

// nullableJSON 把空快照 / 空明细转成 SQL NULL。
func nullableJSON(payload []byte) any {
	if len(payload) == 0 {
		return nil
	}
	return []byte(payload)
}
