package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/quota"
)

// 本文件是窗口限额的读取、聚合与删除入口。
//
// 聚合口径只有这里一处：窗口起点由 internal/quota 的纯函数算出后传入，本层负责
// 把它与 account_quota_event 的最近 reset 基准取较大者，再按 usage JSON 的 metric
// 键求和。判定规则（窗口边界、超限比较）不在这里，store 只做参数化 SQL。
//
// 写入入口（InsertQuota / InsertQuotaEvent）在 billing.go：限额定义与重置事件是
// 同一张计费 schema 的写路径。

// 编译期断言：存储层实现限额判定声明的数据面。
var _ quota.Repo = (*Store)(nil)

// quotaEpoch 是「无窗口下界」的替身时刻。
//
// SQL 里不能传 Go 的零值时间（0001-01-01，早于 MySQL DATETIME 的下界 1000-01-01），
// 用 Unix 纪元代替：billing_usage.created_at 由 DEFAULT CURRENT_TIMESTAMP 生成，
// 不会早于它。
var quotaEpoch = time.Unix(0, 0).UTC()

const quotaColumns = "id, scope, scope_id, metric, window_kind, period, limit_amount, action"

const listQuotasSQL = "SELECT " + quotaColumns + " FROM account_quota ORDER BY id"

const listQuotasByScopeSQL = "SELECT " + quotaColumns + " FROM account_quota WHERE scope = ? AND scope_id = ? ORDER BY id"

// Quotas 读限额定义；scopeID 为 0 时读全部。
func (s *Store) Quotas(ctx context.Context, scope billing.Scope, scopeID uint64) ([]quota.Limit, error) {
	query := listQuotasSQL
	args := []any(nil)
	if scopeID != 0 {
		if err := billing.ValidateScope(scope); err != nil {
			return nil, err
		}
		query = listQuotasByScopeSQL
		args = append(args, scope, scopeID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 account_quota 失败: %w", err)
	}
	return scanQuotaLimits(rows)
}

// scanQuotaLimits 解析限额行，金额按 decimal 解析。
func scanQuotaLimits(rows *sql.Rows) ([]quota.Limit, error) {
	defer func() { _ = rows.Close() }()

	var limits []quota.Limit
	for rows.Next() {
		var (
			limit                           quota.Limit
			scopeRaw, metricRaw             string
			windowRaw, periodRaw, actionRaw string
			limitRaw                        string
		)
		if err := rows.Scan(&limit.ID, &scopeRaw, &limit.ScopeID, &metricRaw,
			&windowRaw, &periodRaw, &limitRaw, &actionRaw); err != nil {
			return nil, fmt.Errorf("store: 解析 account_quota 行失败: %w", err)
		}
		amount, err := parseDecimal(limitRaw)
		if err != nil {
			return nil, err
		}
		limit.Scope = billing.ScopeFromDB(scopeRaw)
		limit.Metric = billing.MetricFromDB(metricRaw)
		limit.WindowKind = billing.WindowKindFromDB(windowRaw)
		limit.Period = billing.PeriodFromDB(periodRaw)
		limit.Action = billing.ActionFromDB(actionRaw)
		limit.LimitAmount = amount
		limits = append(limits, limit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 account_quota 行失败: %w", err)
	}
	return limits, nil
}

// quotaUsageSQL 是窗口已用量的聚合语句，维度列由 quotaScopeColumn 从白名单给出后拼入。
//
// 口径：
//   - metric 取 usage JSON 的对应键，路径以参数传入，不拼字符串；
//   - 求和统一按 DECIMAL(24,8) 精确聚合：limit_amount 是 DECIMAL，整数计数在
//     DECIMAL 下同样精确，因此不需要为整数指标单开一条路径，也不引入浮点；
//   - 下界取「窗口起点」与「最近 reset 基准」的较大者，reset 只截断不回退；
//   - created_at > 下界为严格大于，与 account_quota_event.baseline_at 的注释一致。
const quotaUsageSQL = "SELECT COALESCE(SUM(CAST(JSON_EXTRACT(`usage`, ?) AS DECIMAL(24,8))), 0) " +
	"FROM billing_usage WHERE %s = ? " +
	"AND created_at > GREATEST(?, COALESCE((SELECT MAX(baseline_at) FROM account_quota_event WHERE quota_id = ?), ?))"

// quotaScopeColumn 返回 scope 在 billing_usage 上的维度列。
//
// account / channel 有对应列；api_key / plan 在流水上没有维度，无法聚合，直接报错
// 而不是按全账户口径静默放大或缩小用量。
func quotaScopeColumn(scope billing.Scope) (string, error) {
	switch scope {
	case billing.ScopeAccount:
		return "account_id", nil
	case billing.ScopeChannel:
		return "channel_id", nil
	default:
		return "", fmt.Errorf("store: scope %q 在 billing_usage 上没有对应维度，无法聚合用量", string(scope))
	}
}

// Usage 聚合一条限额在窗口内的已用量。
//
// 单账户单 metric 单窗口一次聚合，走 billing_usage 的 (account_id, created_at) 索引；
// 本层不做缓存，缓存位置见 internal/quota 的判定入口注释。
func (s *Store) Usage(ctx context.Context, q quota.UsageQuery) (decimal.Decimal, error) {
	column, err := quotaScopeColumn(q.Scope)
	if err != nil {
		return decimal.Zero, err
	}
	if q.ScopeID == 0 {
		return decimal.Zero, errors.New("store: 限额聚合的 scope_id 不能为 0")
	}
	if metricErr := billing.ValidateMetric(q.Metric); metricErr != nil {
		return decimal.Zero, metricErr
	}
	since := q.Since
	if since.IsZero() {
		since = quotaEpoch
	}
	//nolint:gosec // G201：维度列来自 quotaScopeColumn 的白名单，metric 与其余取值都走参数占位符。
	query := fmt.Sprintf(quotaUsageSQL, column)
	var raw string
	err = s.db.QueryRowContext(ctx, query, "$."+string(q.Metric), q.ScopeID, since, q.QuotaID, since).Scan(&raw)
	if err != nil {
		return decimal.Zero, fmt.Errorf("store: 聚合 account_quota %d 的用量失败: %w", q.QuotaID, err)
	}
	return parseDecimal(raw)
}

const deleteQuotaSQL = "DELETE FROM account_quota WHERE id = ?"

// DeleteQuota 按 id 删除限额定义。
//
// 不连带删除 account_quota_event：重置事件是 append-only 的审计事实，删定义不改写
// 历史。事件按 quota_id 孤立保留，重新使用同一 id 的情形不存在（自增主键）。
func (s *Store) DeleteQuota(ctx context.Context, id uint64) error {
	if id == 0 {
		return errors.New("store: account_quota.id 不能为 0")
	}
	if _, err := s.db.ExecContext(ctx, deleteQuotaSQL, id); err != nil {
		return fmt.Errorf("store: 删除 account_quota 失败: %w", err)
	}
	return nil
}
