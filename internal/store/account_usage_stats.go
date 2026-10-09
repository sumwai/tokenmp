package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件实现账户面用量聚合的读路径：按维度把 billing_usage 的流水求和。
//
// 与明细的关系是「同一份事实的两种读法」：谓词与明细共用 usagePredicate，合计由
// 同一个 JSON 键集合逐指标求和，因此同一区间上的合计与逐行相加自洽。聚合在库里完成
// 而不是取回明细再相加：账户的流水是持续增长的集合，取回全部行只为了求和，代价随
// 用量线性增长，而页面要的只是几十行分组结果。

// UsageStatsQuery 是账户面用量聚合的过滤条件与分组维度。
type UsageStatsQuery struct {
	// AccountID 是流水归属账户，必填：作用域只能来自会话。
	AccountID uint64
	// Since / Until 是写入时刻的闭区间；零值表示该侧不限。
	Since time.Time
	Until time.Time
	// RequestedModel 按客户端请求的模型名精确匹配；空串表示不过滤。
	RequestedModel string
	// APIKeyID 按签发本次调用的密钥过滤；0 表示不过滤。
	APIKeyID uint64
	// GroupBy 是分组维度：day | model | api_key。
	GroupBy string
}

// UsageStatsItem 是一个分组的用量合计。
type UsageStatsItem struct {
	// Key 是分组值：日期（YYYY-MM-DD）、模型名或密钥 id 的十进制文本。
	Key string
	// Calls 是区间内的流水行数：一次成功履约的请求一行。
	Calls int64
	// Tokens 是各计量指标在该分组内的合计，键与 billing.Metric 取值一致；
	// 该分组内从未出现的指标不进 map（读出为 0，与明细的缺省口径一致）。
	Tokens map[string]int64
	// ChargedAmount 是应扣量合计（逐行 基础价 × 倍率 相加），数据库文本。
	//
	// 逐行折算再相加而不是「合计基础价 × 平均倍率」：倍率逐行解析，先平均再造和
	// 会让合计与逐行相加的明细对不上。
	ChargedAmount string
}

// 聚合维度的取值标识：请求记录聚合与用量聚合各自的白名单共用这几个字面量，
// 两处各写一遍字符串迟早会漂开（goconst 也会拦）。
const (
	// groupByDay 是日期维度。
	groupByDay = "day"
	// groupByModel 是模型维度，两族聚合都支持。
	groupByModel = "model"
	// groupByAPIKey 是用量聚合的密钥维度。
	groupByAPIKey = "api_key"
	// groupByStatus 是请求记录聚合的终态维度。
	groupByStatus = "status"
)

// usageStatsGroupKeys 是分组维度的取值集合，也是「分组表达式」的唯一出处。
//
// 表达式来自本表而不是调用方拼串：分组维度是列映射，让调用方拼 SQL 片段等于把
// 注入面开在查询条件上。
//
//nolint:gosec // G101：本表是按维度到列表达式的映射，键名里的 key 指聚合维度，不是凭据。
var usageStatsGroupKeys = map[string]string{
	groupByDay:    "DATE_FORMAT(created_at, '%Y-%m-%d')",
	groupByModel:  "requested_model",
	groupByAPIKey: "CAST(api_key_id AS CHAR)",
}

// usageStatsMetrics 是聚合逐项求和的指标：billing 的写入白名单去掉 request。
//
// request 分量与 Calls 同义（一行即一次请求），不重复求和。白名单取自 billing，
// 新增指标时聚合侧自动跟上，不必在这里再维护一份清单。
func usageStatsMetrics() []billing.Metric {
	all := billing.Metrics()
	out := make([]billing.Metric, 0, len(all))
	for _, m := range all {
		if m == billing.MetricRequest {
			continue
		}
		out = append(out, m)
	}
	return out
}

// usageMetricSumExpr 生成一个指标在该分组内的求和表达式。
//
// 取 JSON 里的键并转成无符号整数：token 计数非负，用整数而非定点数求和，读回时不
// 必再从「12.00000000」这样的定点文本里剥小数。键缺失或为空的行按 NULL 处理，
// SUM 自动忽略，分组内全缺时为 NULL。指标名是编译期常量，不来自调用方。
func usageMetricSumExpr(m billing.Metric) string {
	return "SUM(CAST(`usage` ->> '$." + string(m) + "' AS UNSIGNED))"
}

// AccountUsageStats 按维度聚合账户的用量，按 key 升序返回。
func (s *Store) AccountUsageStats(ctx context.Context, q UsageStatsQuery) ([]UsageStatsItem, error) {
	if q.AccountID == 0 {
		return nil, errors.New("store: billing_usage.account_id 不能为 0")
	}
	keyExpr, ok := usageStatsGroupKeys[q.GroupBy]
	if !ok {
		return nil, fmt.Errorf("store: 未知的聚合维度 %q", q.GroupBy)
	}
	where, args := usagePredicate{
		AccountID:      q.AccountID,
		Since:          q.Since,
		Until:          q.Until,
		RequestedModel: q.RequestedModel,
		APIKeyID:       q.APIKeyID,
	}.where()

	metrics := usageStatsMetrics()
	selects := []string{keyExpr + " AS k", "COUNT(*)", "SUM(gross_amount * multiplier)"}
	for _, m := range metrics {
		selects = append(selects, usageMetricSumExpr(m))
	}
	//nolint:gosec // G202：拼进去的是白名单分组表达式与常量指标名，取值一律走问号占位符。
	query := "SELECT " + strings.Join(selects, ", ") + " FROM billing_usage" + where + " GROUP BY k ORDER BY k"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 聚合 billing_usage 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []UsageStatsItem
	for rows.Next() {
		var (
			item    UsageStatsItem
			charged sql.NullString
			sums    = make([]sql.NullInt64, len(metrics))
		)
		dest := []any{&item.Key, &item.Calls, &charged}
		for i := range sums {
			dest = append(dest, &sums[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("store: 解析 billing_usage 聚合行失败: %w", err)
		}
		item.Tokens = make(map[string]int64, len(metrics))
		for i, m := range metrics {
			if sums[i].Valid {
				item.Tokens[string(m)] = sums[i].Int64
			}
		}
		// 分组内金额为 NULL 只可能出现在所有行的倍率都是 NULL 的情形；存量事实里
		// 两列都有默认值，因此这里只在类型层面兜底，读成 0 而不是让整条查询失败。
		item.ChargedAmount = charged.String
		if !charged.Valid {
			item.ChargedAmount = "0"
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 billing_usage 聚合行失败: %w", err)
	}
	return items, nil
}
