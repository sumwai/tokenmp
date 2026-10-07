package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 本文件是 0001 迁移引入的路由相关表的读入口：客户端密钥鉴权、候选渠道查询与上游凭据读取。
//
// 与 billing.go 的分工一致：校验在触达驱动之前完成，查询一律参数化，错误统一包上表名。
// 返回值都是只读快照，不携带任何运行期状态。

// platformMerchantID 是「生效商家」解析链的兜底：api_key 与 account 都没指定时归平台自营。
// 该 id 由 0001 迁移固定写入，不是可变数据。
const platformMerchantID uint64 = 1

// rowIter 是查询结果的最小迭代面，*sql.Rows 满足它。
//
// 单测需要断言 SQL 与参数，而 *sql.Rows 无法脱离驱动伪造；把读路径收敛到本接口后，
// 测试注入假实现即可覆盖查询组装与行解析，不必连数据库。
type rowIter interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// querier 是读路径依赖的最小执行面。
//
// *sql.DB 的 QueryContext 返回 *sql.Rows，与本接口的返回类型不同，故由 dbQuerier 适配；
// 这层适配也让「读」与 billing.go 的 executor（只写）互不牵扯。
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (rowIter, error)
}

// dbQuerier 把 *sql.DB 适配成 querier。
type dbQuerier struct{ db *sql.DB }

// QueryContext 执行查询；出错时返回 nil，而不是把 (*sql.Rows)(nil) 装进接口 ——
// 后者会让调用方拿到一个非空接口里的空指针，判定「有没有结果集」时踩空。
func (q dbQuerier) QueryContext(ctx context.Context, query string, args ...any) (rowIter, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// APIKeyAuth 是一次客户端密钥鉴权命中的事实。
//
// MerchantID 已按「api_key.merchant_id → account.default_merchant_id → 平台自营」解析完毕，
// 调用方直接用它选路，不必再看两个可空列。
type APIKeyAuth struct {
	APIKeyID      uint64
	AccountID     uint64
	AccountStatus string
	MerchantID    uint64
}

//nolint:gosec // G101：这是 SQL 语句，不是凭据；命中启发式只是因为常量名里有 key。
const lookupAPIKeySQL = `SELECT k.id, k.account_id, a.status, k.merchant_id, a.default_merchant_id
FROM account_api_key k
JOIN account a ON a.id = k.account_id
WHERE k.key_hash = ? AND k.enabled = 1 AND (k.expires_at IS NULL OR k.expires_at > ?)`

// lookupAPIKey 按密钥哈希查一条启用且未过期的客户端密钥。
//
// now 由调用方传入而不是用数据库的 NOW()：过期判定依赖数据库时钟会让结果不可复现，
// 无法为补算或回放场景指定时刻（同 billing.go 的 ActivePricing）。
// 无匹配时返回的错误可用 errors.Is(err, sql.ErrNoRows) 判断。
func lookupAPIKey(ctx context.Context, q querier, keyHash string, now time.Time) (*APIKeyAuth, error) {
	if strings.TrimSpace(keyHash) == "" {
		return nil, errors.New("store: account_api_key.key_hash 不能为空")
	}
	rows, err := q.QueryContext(ctx, lookupAPIKeySQL, keyHash, now)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 account_api_key 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("store: 遍历 account_api_key 行失败: %w", err)
		}
		return nil, fmt.Errorf("store: 查询 account_api_key 失败: %w", sql.ErrNoRows)
	}

	var (
		auth            APIKeyAuth
		keyMerchant     sql.NullInt64
		accountMerchant sql.NullInt64
	)
	if err := rows.Scan(&auth.APIKeyID, &auth.AccountID, &auth.AccountStatus, &keyMerchant, &accountMerchant); err != nil {
		return nil, fmt.Errorf("store: 解析 account_api_key 行失败: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 account_api_key 行失败: %w", err)
	}
	auth.MerchantID = resolveMerchantID(keyMerchant, accountMerchant)
	return &auth, nil
}

// resolveMerchantID 按优先级解析生效商家：api_key 绑定优先，其次账户默认，最后平台自营。
//
// 两个入参都是可空列；非正值与非 NULL 同样按「未设置」处理，避免脏数据把商家解析成 0。
func resolveMerchantID(keyMerchant, accountMerchant sql.NullInt64) uint64 {
	if keyMerchant.Valid && keyMerchant.Int64 > 0 {
		return uint64(keyMerchant.Int64)
	}
	if accountMerchant.Valid && accountMerchant.Int64 > 0 {
		return uint64(accountMerchant.Int64)
	}
	return platformMerchantID
}

// LookupAPIKey 按密钥哈希查一条启用且未过期的客户端密钥。
//
// 返回值中的 MerchantID 已解析为生效商家。
func (s *Store) LookupAPIKey(ctx context.Context, keyHash string, now time.Time) (*APIKeyAuth, error) {
	return lookupAPIKey(ctx, dbQuerier{db: s.db}, keyHash, now)
}

// RouteCandidate 是一条候选渠道与它命中的模型映射。
//
// 它是库表事实的直出：BaseURL 只到端点段之前，协议端点段由装配层按本次协议拼接；
// Config 与 RequestOverrides 是原始 JSON，存储层不解释其结构。
type RouteCandidate struct {
	ChannelID uint64
	// ChannelType 是该渠道自身的协议方言。同协议查询下它恒等于查询用的方言；
	// 不限协议查询下它就是本条候选实际使用的上游协议，装配层据此判定是否需要重建。
	// 取值经 ChannelTypeFromDB 宽容转换：读到本版本不认识的方言时原样返回，由上层决定跳过。
	ChannelType ChannelType
	BaseURL     string
	CredGroup   string
	// Vendor 是渠道的厂商标签（`channel create --vendor`），由运营命名。
	// 它与 ChannelID 分工不同：后者是环境事实（重建库就换号），本字段是可以写进配置、
	// 可以跨环境复现的可读标识，中间件作用域就靠它认定 provider。
	Vendor string
	// Priority 是渠道的路由排序值，值大者优先；同优先级内由选路策略决定顺序。
	// 存储层按它降序返回，但同优先级内的加权随机不在存储层完成，故把取值交给调用方。
	Priority             int
	Weight               int
	RateLimitQPS         int
	RateLimitConcurrency int
	Config               []byte
	UpstreamModel        string
	RequestOverrides     []byte
}

// routeCandidateColumns 是两条候选查询共用的列清单与连接条件。
//
// 同协议查询与不限协议查询只差一个 type 谓词：共用同一段前缀使两条查询的列序、
// 排序口径与扫描目标必然一致，跨协议补段时的行解析不必另写一份。
const routeCandidateColumns = `SELECT c.id, c.type, c.base_url, c.cred_group, c.vendor, c.priority, c.weight, c.rate_limit_qps, c.rate_limit_concurrency, c.config, m.upstream_model, m.request_overrides
FROM upstream_channel c
JOIN upstream_model_map m ON m.channel_id = c.id
WHERE `

const routeCandidatesSQL = routeCandidateColumns + `c.type = ? AND c.enabled = 1 AND c.merchant_id = ? AND m.model = ? AND m.enabled = 1
ORDER BY c.priority DESC, c.id`

// routeCandidatesAnyTypeSQL 不限制渠道协议方言，供跨协议补段使用。
const routeCandidatesAnyTypeSQL = routeCandidateColumns + `c.enabled = 1 AND c.merchant_id = ? AND m.model = ? AND m.enabled = 1
ORDER BY c.priority DESC, c.id`

// routeCandidates 查某商家下、某协议方言与某模型命中的候选渠道。
//
// 只按 priority 降序返回，同优先级内按渠道 id 稳定排序；同优先级内的加权随机属选路策略，
// 不放在存储层 —— 存储层只回答「有哪些候选」，因此把 priority 一并交给调用方分组。
func routeCandidates(ctx context.Context, q querier, channelType ChannelType, model string, merchantID uint64) ([]RouteCandidate, error) {
	if err := ValidateChannelType(channelType); err != nil {
		return nil, err
	}
	if err := validateRouteQuery(model, merchantID); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, routeCandidatesSQL, channelType, merchantID, model)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 upstream_channel 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRouteCandidates(rows)
}

// routeCandidatesAnyType 查某商家下、任意协议方言与某模型命中的候选渠道。
//
// 除不限定 c.type 外与 routeCandidates 完全同构：列清单、过滤项与排序都来自同一份定义，
// 因此同一商家同一模型下两条查询的结果集只差协议方言这一维，调用方可以按渠道 id 直接去重。
func routeCandidatesAnyType(ctx context.Context, q querier, model string, merchantID uint64) ([]RouteCandidate, error) {
	if err := validateRouteQuery(model, merchantID); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, routeCandidatesAnyTypeSQL, merchantID, model)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 upstream_channel 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRouteCandidates(rows)
}

// validateRouteQuery 校验两条候选查询共用的入参：模型名与商家。
func validateRouteQuery(model string, merchantID uint64) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("store: upstream_model_map.model 不能为空")
	}
	if merchantID == 0 {
		return errors.New("store: upstream_channel.merchant_id 不能为 0")
	}
	return nil
}

// scanRouteCandidates 解析候选查询的行集。
func scanRouteCandidates(rows rowIter) ([]RouteCandidate, error) {
	var candidates []RouteCandidate
	for rows.Next() {
		var (
			c           RouteCandidate
			channelType string
		)
		if err := rows.Scan(&c.ChannelID, &channelType, &c.BaseURL, &c.CredGroup, &c.Vendor, &c.Priority, &c.Weight,
			&c.RateLimitQPS, &c.RateLimitConcurrency, &c.Config, &c.UpstreamModel, &c.RequestOverrides); err != nil {
			return nil, fmt.Errorf("store: 解析 upstream_channel 行失败: %w", err)
		}
		c.ChannelType = ChannelTypeFromDB(channelType)
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 upstream_channel 行失败: %w", err)
	}
	return candidates, nil
}

// RouteCandidates 查某商家下、某协议方言与某模型命中的候选渠道。
func (s *Store) RouteCandidates(ctx context.Context, channelType ChannelType, model string, merchantID uint64) ([]RouteCandidate, error) {
	return routeCandidates(ctx, dbQuerier{db: s.db}, channelType, model, merchantID)
}

// RouteCandidatesAnyType 查某商家下、任意协议方言与某模型命中的候选渠道。
//
// 结果里同一渠道的协议方言由 RouteCandidate.ChannelType 给出；本方法不按方言过滤，
// 是否把某条候选当作跨协议回退由装配层的选路决定。
func (s *Store) RouteCandidatesAnyType(ctx context.Context, model string, merchantID uint64) ([]RouteCandidate, error) {
	return routeCandidatesAnyType(ctx, dbQuerier{db: s.db}, model, merchantID)
}

// Credential 是 upstream_credential 的一行：同组内用于轮换的一份上游凭据。
type Credential struct {
	// ID 是凭据行主键，供续期写回定位到具体行。
	ID uint64
	// Name 是凭据名。
	Name string
	// Secret 是凭据原文 JSON，结构由渠道协议与厂商约定，存储层不解释。
	Secret []byte
}

//nolint:gosec // G101：这是 SQL 语句；命中的是表名与列名里的 credential，不是凭据原文。
const credentialsByGroupSQL = `SELECT id, name, secret
FROM upstream_credential
WHERE merchant_id = ? AND cred_group = ? AND enabled = 1
ORDER BY id`

// credentialsByGroup 读某商家某分组下启用中的全部凭据，按 id 升序。
//
// 返回多行而不是一行：同组多行用于轮换，取用策略（取哪一行、是否轮换）属装配层，
// 存储层只按「谁在前」给出确定的顺序。
func credentialsByGroup(ctx context.Context, q querier, credGroup string, merchantID uint64) ([]Credential, error) {
	if strings.TrimSpace(credGroup) == "" {
		return nil, errors.New("store: upstream_credential.cred_group 不能为空")
	}
	if merchantID == 0 {
		return nil, errors.New("store: upstream_credential.merchant_id 不能为 0")
	}
	rows, err := q.QueryContext(ctx, credentialsByGroupSQL, merchantID, credGroup)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 upstream_credential 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var credentials []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.Name, &c.Secret); err != nil {
			return nil, fmt.Errorf("store: 解析 upstream_credential 行失败: %w", err)
		}
		credentials = append(credentials, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 upstream_credential 行失败: %w", err)
	}
	return credentials, nil
}

// CredentialsByGroup 读某商家某分组下启用中的全部凭据，按 id 升序。
func (s *Store) CredentialsByGroup(ctx context.Context, credGroup string, merchantID uint64) ([]Credential, error) {
	return credentialsByGroup(ctx, dbQuerier{db: s.db}, credGroup, merchantID)
}
