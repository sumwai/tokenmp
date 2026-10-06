package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/plan"
	"github.com/sumwai/tokenmp/internal/quota"
)

// 本文件是 0005 迁移引入的上游套餐与配额的读写入口。
//
// 与 account_quota 的分工：那张表的数据面是 billing_usage 的聚合，这张表的数据面是
// 探针采集回来的快照。窗口语义复用同一套纯函数（internal/quota），本文件只做参数化 SQL。

// 编译期断言：存储层同时实现采集侧与消费侧的数据面。
var (
	_ plan.Repo   = (*Store)(nil)
	_ plan.Reader = (*Store)(nil)
)

const upstreamPlanColumns = "id, merchant_id, cred_group, name, multiplier, valid_from, valid_to, last_snapshot, last_checked_at"

const listPlansSQL = "SELECT " + upstreamPlanColumns + " FROM upstream_plan ORDER BY id"

const listPlansByMerchantSQL = "SELECT " + upstreamPlanColumns + " FROM upstream_plan WHERE merchant_id = ? ORDER BY id"

const planByCredGroupSQL = "SELECT " + upstreamPlanColumns + " FROM upstream_plan WHERE merchant_id = ? AND cred_group = ?"

const upstreamPlanQuotaColumns = "id, plan_id, metric, window_kind, period, limit_amount, last_used, last_checked_at"

// Plans 读某商家的全部套餐与限额行；merchantID 为 0 时读全部。
func (s *Store) Plans(ctx context.Context, merchantID uint64) ([]plan.UpstreamPlan, error) {
	query := listPlansSQL
	args := []any(nil)
	if merchantID != 0 {
		query = listPlansByMerchantSQL
		args = append(args, merchantID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 upstream_plan 失败: %w", err)
	}
	plans, err := scanUpstreamPlans(rows)
	if err != nil {
		return nil, err
	}
	return s.attachQuotas(ctx, plans)
}

// PlansByCredGroup 读某商家某凭据分组下的套餐与限额行。
func (s *Store) PlansByCredGroup(ctx context.Context, merchantID uint64, credGroup string) (*plan.UpstreamPlan, error) {
	if merchantID == 0 {
		return nil, errors.New("store: upstream_plan.merchant_id 不能为 0")
	}
	if strings.TrimSpace(credGroup) == "" {
		return nil, errors.New("store: upstream_plan.cred_group 不能为空")
	}
	rows, err := s.db.QueryContext(ctx, planByCredGroupSQL, merchantID, credGroup)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 upstream_plan 失败: %w", err)
	}
	plans, err := scanUpstreamPlans(rows)
	if err != nil {
		return nil, err
	}
	if len(plans) == 0 {
		return nil, fmt.Errorf("store: 查询 upstream_plan 失败: %w", sql.ErrNoRows)
	}
	withQuotas, err := s.attachQuotas(ctx, plans)
	if err != nil {
		return nil, err
	}
	return &withQuotas[0], nil
}

// attachQuotas 为一批套餐补上限额行，避免每条套餐各查一次。
func (s *Store) attachQuotas(ctx context.Context, plans []plan.UpstreamPlan) ([]plan.UpstreamPlan, error) {
	if len(plans) == 0 {
		return plans, nil
	}
	ids := make([]uint64, 0, len(plans))
	for _, p := range plans {
		ids = append(ids, p.ID)
	}
	quotas, err := s.quotasByPlanIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range plans {
		plans[i].Quotas = quotas[plans[i].ID]
	}
	return plans, nil
}

// quotasByPlanIDs 一次读回多个套餐的限额行。
func (s *Store) quotasByPlanIDs(ctx context.Context, planIDs []uint64) (map[uint64][]plan.Quota, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(planIDs)), ",")
	//nolint:gosec // G201：只把问号占位符拼进 SQL，取值仍走参数绑定。
	query := "SELECT " + upstreamPlanQuotaColumns + " FROM upstream_plan_quota WHERE plan_id IN (" + placeholders + ") ORDER BY id"
	args := make([]any, 0, len(planIDs))
	for _, id := range planIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 upstream_plan_quota 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[uint64][]plan.Quota, len(planIDs))
	for rows.Next() {
		q, err := scanPlanQuotaRow(rows)
		if err != nil {
			return nil, err
		}
		result[q.PlanID] = append(result[q.PlanID], q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 upstream_plan_quota 行失败: %w", err)
	}
	return result, nil
}

// scanUpstreamPlans 解析套餐行集。
func scanUpstreamPlans(rows *sql.Rows) ([]plan.UpstreamPlan, error) {
	defer func() { _ = rows.Close() }()

	var plans []plan.UpstreamPlan
	for rows.Next() {
		var (
			p             plan.UpstreamPlan
			multiplierRaw string
			validFrom     scanTime
			validTo       scanTime
			lastSnapshot  []byte
			checkedAt     scanTime
		)
		if err := rows.Scan(&p.ID, &p.MerchantID, &p.CredGroup, &p.Name, &multiplierRaw,
			&validFrom, &validTo, &lastSnapshot, &checkedAt); err != nil {
			return nil, fmt.Errorf("store: 解析 upstream_plan 行失败: %w", err)
		}
		multiplier, err := parseDecimal(multiplierRaw)
		if err != nil {
			return nil, err
		}
		p.Multiplier = multiplier
		if validFrom.Valid {
			value := validFrom.Time
			p.ValidFrom = &value
		}
		if validTo.Valid {
			value := validTo.Time
			p.ValidTo = &value
		}
		p.LastSnapshot = lastSnapshot
		if checkedAt.Valid {
			p.LastCheckedAt = checkedAt.Time
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 upstream_plan 行失败: %w", err)
	}
	return plans, nil
}

// scanPlanQuotaRow 解析一行套餐限额。
func scanPlanQuotaRow(rows *sql.Rows) (plan.Quota, error) {
	var (
		q                     plan.Quota
		metricRaw             string
		windowRaw, periodRaw  string
		limitRaw, lastUsedRaw string
		checkedAt             scanTime
	)
	if err := rows.Scan(&q.ID, &q.PlanID, &metricRaw, &windowRaw, &periodRaw,
		&limitRaw, &lastUsedRaw, &checkedAt); err != nil {
		return plan.Quota{}, fmt.Errorf("store: 解析 upstream_plan_quota 行失败: %w", err)
	}
	limit, err := parseDecimal(limitRaw)
	if err != nil {
		return plan.Quota{}, err
	}
	lastUsed, err := parseDecimal(lastUsedRaw)
	if err != nil {
		return plan.Quota{}, err
	}
	q.Metric = billing.MetricFromDB(metricRaw)
	q.WindowKind = billing.WindowKindFromDB(windowRaw)
	q.Period = billing.PeriodFromDB(periodRaw)
	q.LimitAmount = limit
	q.LastUsed = lastUsed
	if checkedAt.Valid {
		q.LastCheckedAt = checkedAt.Time
	}
	return q, nil
}

const probeTargetsSQL = `SELECT id, merchant_id, cred_group, config
FROM upstream_channel
WHERE enabled = 1 AND config IS NOT NULL
ORDER BY id`

// ProbeTargets 读全部声明了探针的启用渠道。
//
// 不过滤 config 内容：是否声明了 probe、声明是否合法由 internal/plan 解析后判定，
// 存储层不解释 JSON 结构。
func (s *Store) ProbeTargets(ctx context.Context) ([]plan.ProbeTarget, error) {
	rows, err := s.db.QueryContext(ctx, probeTargetsSQL)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 upstream_channel 探针失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var targets []plan.ProbeTarget
	for rows.Next() {
		var t plan.ProbeTarget
		if err := rows.Scan(&t.ChannelID, &t.MerchantID, &t.CredGroup, &t.Config); err != nil {
			return nil, fmt.Errorf("store: 解析 upstream_channel 探针行失败: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 upstream_channel 探针行失败: %w", err)
	}
	return targets, nil
}

const insertUpstreamPlanSQL = `INSERT INTO upstream_plan
  (merchant_id, cred_group, name, multiplier, valid_from, valid_to)
VALUES (?, ?, ?, ?, ?, ?)`

const insertPlanQuotaSQL = `INSERT INTO upstream_plan_quota
  (plan_id, metric, window_kind, period, limit_amount)
VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE limit_amount = VALUES(limit_amount)`

// InsertUpstreamPlan 写入一条套餐及其限额行，返回套餐 id。
//
// 套餐与限额行在同一事务里写：中途失败会留下一条没有限额行的套餐，
// 那会被路由当成「无从判定」而永远放行，问题不会自己暴露。
func (s *Store) InsertUpstreamPlan(ctx context.Context, p plan.UpstreamPlan, quotas []plan.Quota) (uint64, error) {
	if err := validateUpstreamPlan(p); err != nil {
		return 0, err
	}
	for _, q := range quotas {
		if err := validatePlanQuota(q); err != nil {
			return 0, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: 开启 upstream_plan 事务失败: %w", err)
	}
	planID, err := insertUpstreamPlanTx(ctx, tx, p)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	for _, q := range quotas {
		q.PlanID = planID
		if _, err := insertPlanQuotaTx(ctx, tx, q); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: 提交 upstream_plan 事务失败: %w", err)
	}
	return planID, nil
}

// insertUpstreamPlanTx 在事务内写一条套餐。
func insertUpstreamPlanTx(ctx context.Context, tx *sql.Tx, p plan.UpstreamPlan) (uint64, error) {
	res, err := tx.ExecContext(ctx, insertUpstreamPlanSQL,
		p.MerchantID, p.CredGroup, p.Name, p.Multiplier.String(), timeArg(p.ValidFrom), timeArg(p.ValidTo))
	return insertID(res, err, "upstream_plan")
}

// insertPlanQuotaTx 在事务内写一条套餐限额。
func insertPlanQuotaTx(ctx context.Context, tx *sql.Tx, q plan.Quota) (uint64, error) {
	res, err := tx.ExecContext(ctx, insertPlanQuotaSQL,
		q.PlanID, q.Metric, q.WindowKind, q.Period, q.LimitAmount.String())
	return insertID(res, err, "upstream_plan_quota")
}

// validateUpstreamPlan 校验套餐行。
func validateUpstreamPlan(p plan.UpstreamPlan) error {
	if p.MerchantID == 0 {
		return errors.New("store: upstream_plan.merchant_id 不能为 0")
	}
	if strings.TrimSpace(p.CredGroup) == "" {
		return errors.New("store: upstream_plan.cred_group 不能为空")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("store: upstream_plan.name 不能为空")
	}
	if !p.Multiplier.IsPositive() {
		return fmt.Errorf("store: upstream_plan.multiplier 必须为正，得到 %s", p.Multiplier.String())
	}
	return nil
}

// validatePlanQuota 校验套餐限额行。
func validatePlanQuota(q plan.Quota) error {
	if err := billing.ValidateMetric(q.Metric); err != nil {
		return err
	}
	if err := quota.ValidWindow(q.WindowKind, q.Period); err != nil {
		return err
	}
	if !q.LimitAmount.IsPositive() {
		return fmt.Errorf("store: upstream_plan_quota.limit_amount 必须为正，得到 %s", q.LimitAmount.String())
	}
	return nil
}

const updatePlanSnapshotSQL = `UPDATE upstream_plan SET last_snapshot = ?, last_checked_at = ? WHERE id = ?`

const updatePlanQuotaUsedSQL = `UPDATE upstream_plan_quota SET last_used = ?, last_checked_at = ? WHERE id = ? AND plan_id = ?`

// SaveProbeResult 保存一次采集结果：套餐快照与各限额行的已用量。
//
// 快照与已用量在同一事务里写：分开写会出现「快照已是新窗口、已用量还是旧窗口」的中间态，
// 而路由正好可能在这个窗口里读到它。
func (s *Store) SaveProbeResult(ctx context.Context, planID uint64, snapshot []byte, checkedAt time.Time, used []plan.QuotaUsed) error {
	if planID == 0 {
		return errors.New("store: upstream_plan.id 不能为 0")
	}
	if checkedAt.IsZero() {
		return errors.New("store: upstream_plan.last_checked_at 不能为零值")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开启采集结果事务失败: %w", err)
	}
	var snapshotArg any
	if len(snapshot) > 0 {
		snapshotArg = snapshot
	}
	if _, err := tx.ExecContext(ctx, updatePlanSnapshotSQL, snapshotArg, checkedAt, planID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: 更新 upstream_plan 快照失败: %w", err)
	}
	for _, item := range used {
		if item.QuotaID == 0 {
			_ = tx.Rollback()
			return errors.New("store: upstream_plan_quota.id 不能为 0")
		}
		if _, err := tx.ExecContext(ctx, updatePlanQuotaUsedSQL,
			item.Used.String(), checkedAt, item.QuotaID, planID); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: 更新 upstream_plan_quota 已用量失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交采集结果事务失败: %w", err)
	}
	return nil
}
