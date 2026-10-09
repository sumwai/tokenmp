package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 本文件是商家域（页面 /api/v1/partner/*）的数据面：上游渠道与凭据的读、启停，以及
// 本商家名下的用量聚合。
//
// 与管理面（admin.go）的分工：管理面是运维工具，读全表、按 id 写；商家域的作用域只能
// 来自会话推导出的商家，因此这里每一处读写都带 merchant_id 条件 —— 越权在存储层就被
// 挡住，不靠调用方记得先校验归属。命中 0 行即「不存在或不属于本商家」，由调用方翻成
// 404，不以 403 区分存在性。

// MerchantByOwner 返回登录主体名下绑定的商家；未绑定时返回 sql.ErrNoRows。
func (s *Store) MerchantByOwner(ctx context.Context, userID uint64) (*Merchant, error) {
	if userID == 0 {
		return nil, errors.New("store: merchant.owner_user_id 不能为 0")
	}
	var (
		m         Merchant
		kindRaw   string
		createdAt scanTime
	)
	err := s.db.QueryRowContext(ctx, merchantByOwnerSQL, userID).
		Scan(&m.ID, &m.Code, &m.Name, &kindRaw, &m.Status, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("store: 查询 merchant 失败: %w", err)
	}
	owner := userID
	m.Kind = MerchantKindFromDB(kindRaw)
	m.CreatedAt = createdAt.Time
	m.OwnerUserID = &owner
	return &m, nil
}

const merchantByOwnerSQL = `SELECT id, code, name, kind, status, created_at
FROM merchant WHERE owner_user_id = ?`

const setMerchantOwnerSQL = "UPDATE merchant SET owner_user_id = ? WHERE id = ?"

// SetMerchantOwner 把商家绑定到一个登录主体上；商家域的作用域由此推导。
//
// 归属只能有一处：owner_user_id 上有唯一键，重复绑定会被写入冲突拦下（由调用方
// 翻成可读错误）。绑定不影响已在跑的转发，只决定谁能从页面自助管理这个商家。
func (s *Store) SetMerchantOwner(ctx context.Context, id, userID uint64) error {
	if id == 0 {
		return errors.New("store: merchant.id 不能为 0")
	}
	if userID == 0 {
		return errors.New("store: merchant.owner_user_id 不能为 0")
	}
	if _, err := s.db.ExecContext(ctx, setMerchantOwnerSQL, userID, id); err != nil {
		return describeWriteError("merchant", err)
	}
	return nil
}

// partnerChannelFilter 是商家面渠道列表的过滤条件（不含分页，分页由调用方直接给）。
type partnerChannelFilter struct {
	merchantID uint64
	// Enabled 为 nil 表示不过滤启用状态。
	Enabled *bool
}

// channelsByMerchantWhere 组装商家面渠道列表的 WHERE 子句与参数。
//
// 单独抽出来是为了让计数与取页必然同源：两处各拼一次谓词，任一处漏条件都会让 total
// 与当页对不上，而那种偏差只在特定过滤组合下出现。
func channelsByMerchantWhere(f partnerChannelFilter) (string, []any) {
	conditions := []string{merchantIDCondition}
	args := []any{f.merchantID}
	if f.Enabled != nil {
		conditions = append(conditions, "enabled = ?")
		args = append(args, *f.Enabled)
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

const channelsByMerchantColumns = `SELECT id, merchant_id, name, vendor, type, cred_group,
  base_url, priority, weight, enabled FROM upstream_channel`

// ChannelsByMerchant 按商家分页列出渠道，返回当页行与满足条件的总数。
//
// 不回显 config：渠道级扩展配置里可能有上游请求头，商家面只需要看到自己登记了什么。
// 排序按主键升序：与管理面列表同序，同一个商家在两处的行序一致。
func (s *Store) ChannelsByMerchant(ctx context.Context, merchantID uint64, enabled *bool, limit, offset int) ([]Channel, int, error) {
	if merchantID == 0 {
		return nil, 0, errors.New("store: upstream_channel.merchant_id 不能为 0")
	}
	if limit <= 0 {
		return nil, 0, errors.New("store: 分页条数必须为正")
	}
	if offset < 0 {
		return nil, 0, errors.New("store: 分页偏移不能为负")
	}
	where, args := channelsByMerchantWhere(partnerChannelFilter{merchantID: merchantID, Enabled: enabled})

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM upstream_channel"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: 统计 upstream_channel 失败: %w", err)
	}

	//nolint:gosec // G202：拼进去的是列清单与由常量组成的谓词，取值一律走问号占位符。
	query := channelsByMerchantColumns + where + " ORDER BY id LIMIT ? OFFSET ?"
	rows, err := s.db.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: 查询 upstream_channel 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var channels []Channel
	for rows.Next() {
		var (
			c       Channel
			typeRaw string
		)
		if err := rows.Scan(&c.ID, &c.MerchantID, &c.Name, &c.Vendor, &typeRaw,
			&c.CredGroup, &c.BaseURL, &c.Priority, &c.Weight, &c.Enabled); err != nil {
			return nil, 0, fmt.Errorf("store: 解析 upstream_channel 行失败: %w", err)
		}
		c.Type = ChannelTypeFromDB(typeRaw)
		channels = append(channels, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: 遍历 upstream_channel 行失败: %w", err)
	}
	return channels, total, nil
}

//nolint:gosec // G101：这是 SQL 语句；命中的是表名与列名里的 credential / secret，不是凭据原文。
const credentialsByMerchantColumns = `SELECT id, merchant_id, cred_group, name, secret, enabled
FROM upstream_credential`

// CredentialsByMerchant 按商家分页列出凭据，返回当页行与满足条件的总数。
//
// 含 secret 原文：商家面的列表要按凭据前缀展示，脱敏由调用方用 credential.MaskSecret
// 完成 —— 存储层不解释 secret 结构，也不决定展示口径。
func (s *Store) CredentialsByMerchant(ctx context.Context, merchantID uint64, limit, offset int) ([]CredentialRow, int, error) {
	if merchantID == 0 {
		return nil, 0, errors.New("store: upstream_credential.merchant_id 不能为 0")
	}
	if limit <= 0 {
		return nil, 0, errors.New("store: 分页条数必须为正")
	}
	if offset < 0 {
		return nil, 0, errors.New("store: 分页偏移不能为负")
	}
	where, args := " WHERE "+merchantIDCondition, []any{merchantID}

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM upstream_credential"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: 统计 upstream_credential 失败: %w", err)
	}

	//nolint:gosec // G202：拼进去的是列清单与由常量组成的谓词，取值一律走问号占位符。
	query := credentialsByMerchantColumns + where + " ORDER BY id LIMIT ? OFFSET ?"
	rows, err := s.db.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: 查询 upstream_credential 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var credentials []CredentialRow
	for rows.Next() {
		var c CredentialRow
		if err := rows.Scan(&c.ID, &c.MerchantID, &c.CredGroup, &c.Name, &c.Secret, &c.Enabled); err != nil {
			return nil, 0, fmt.Errorf("store: 解析 upstream_credential 行失败: %w", err)
		}
		credentials = append(credentials, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: 遍历 upstream_credential 行失败: %w", err)
	}
	return credentials, total, nil
}

const setChannelEnabledForMerchantSQL = "UPDATE upstream_channel SET enabled = ? WHERE id = ? AND merchant_id = ?"

// SetChannelEnabledForMerchant 置位本商家某条渠道的启用标志；返回是否命中本商家的行。
//
// 归属条件写在 UPDATE 的谓词里而不是先查后写：先查后写会在两条语句之间留出归属被改的
// 窗口，而「这条渠道是不是本商家的」正是这里的判定本身。
func (s *Store) SetChannelEnabledForMerchant(ctx context.Context, id, merchantID uint64, enabled bool) (bool, error) {
	if id == 0 || merchantID == 0 {
		return false, errors.New("store: upstream_channel.id 与 merchant_id 都不能为 0")
	}
	res, err := s.db.ExecContext(ctx, setChannelEnabledForMerchantSQL, enabled, id, merchantID)
	if err != nil {
		return false, fmt.Errorf("store: 更新 upstream_channel.enabled 失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: 读取 upstream_channel 影响行数失败: %w", err)
	}
	return enabledScopedHit(ctx, s.db, affected, channelExistsSQL, id, merchantID)
}

const setCredentialEnabledForMerchantSQL = "UPDATE upstream_credential SET enabled = ? WHERE id = ? AND merchant_id = ?"

// SetCredentialEnabledForMerchant 置位本商家某条凭据的启用标志；返回是否命中本商家的行。
func (s *Store) SetCredentialEnabledForMerchant(ctx context.Context, id, merchantID uint64, enabled bool) (bool, error) {
	if id == 0 || merchantID == 0 {
		return false, errors.New("store: upstream_credential.id 与 merchant_id 都不能为 0")
	}
	res, err := s.db.ExecContext(ctx, setCredentialEnabledForMerchantSQL, enabled, id, merchantID)
	if err != nil {
		return false, fmt.Errorf("store: 更新 upstream_credential.enabled 失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: 读取 upstream_credential 影响行数失败: %w", err)
	}
	return enabledScopedHit(ctx, s.db, affected, credentialExistsSQL, id, merchantID)
}

const channelExistsSQL = "SELECT 1 FROM upstream_channel WHERE id = ? AND merchant_id = ?"

const credentialExistsSQL = "SELECT 1 FROM upstream_credential WHERE id = ? AND merchant_id = ?"

// enabledScopedHit 把作用域更新语句的影响行数翻成「是否命中本商家的行」。
//
// 不能只看 RowsAffected：MySQL 在取值没有变化时报 0 行受影响，「重复停用」会被误判成
// 不存在。受影响为 0 时再按同一作用域确认一次行是否存在，两者都不认为行存在才回 false。
func enabledScopedHit(ctx context.Context, q session, affected int64, existsSQL string, id, merchantID uint64) (bool, error) {
	if affected > 0 {
		return true, nil
	}
	var one int
	err := q.QueryRowContext(ctx, existsSQL, id, merchantID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: 确认作用域行失败: %w", err)
	}
	return true, nil
}

// MerchantUsageStatsQuery 是商家面用量聚合的过滤条件与分组维度。
//
// 字段口径与账户面的 UsageStatsQuery 逐一对应，只是作用域维度换成商家：同一区间、
// 同一维度上，商家看到的行是名下渠道的流水。
type MerchantUsageStatsQuery struct {
	// MerchantID 是渠道归属的商家，必填：作用域只能来自会话。
	MerchantID uint64
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

// MerchantUsageStats 按维度聚合本商家名下的用量，按 key 升序返回。
//
// 与账户面共用同一份聚合实现（usageStatsBy）：指标集合、分组表达式与逐行折算只有一处，
// 两条入口在同一区间上给出的合计因此必然一致。
func (s *Store) MerchantUsageStats(ctx context.Context, q MerchantUsageStatsQuery) ([]UsageStatsItem, error) {
	if q.MerchantID == 0 {
		return nil, errors.New("store: billing_usage.merchant_id 不能为 0")
	}
	return s.usageStatsBy(ctx, usagePredicate{
		MerchantID:     q.MerchantID,
		Since:          q.Since,
		Until:          q.Until,
		RequestedModel: q.RequestedModel,
		APIKeyID:       q.APIKeyID,
	}, q.GroupBy)
}
