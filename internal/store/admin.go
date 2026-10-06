package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件是 admin 管理面所需的读写入口：商家、渠道、凭据、模型映射、账户、密钥、
// 账本、商品、购买、定价、规则、日历、流水与调账。
//
// 与 billing.go / route.go 的分工一致：写入先过白名单与必填校验，再参数化 SQL；
// 查询一律参数化，错误包上表名。业务分支（版本号推进、购买派生、密钥生成）不在这里，
// 存储层只提供可验证的单步动作与少量必须落在事务里的组合动作。
//
// 管理面是运维工具，不是高频路径：这里的查询按可读性优先，不做分页与投影裁剪。

// StatusActive、StatusDisabled 是 merchant.status 与 account.status 的取值。
//
// 这两列用字符串而不是布尔：状态后续可能扩展（如 suspended），字符串列加取值
// 与 DDL 注释同步即可，布尔列则会迫使再开一列。
const (
	// StatusActive 是启用状态。
	StatusActive = "active"
	// StatusDisabled 是停用状态。
	StatusDisabled = "disabled"
)

// MerchantKind 是商家类型，对应 merchant.kind。
//
//	platform  平台自营
//	partner   入驻商家
//
// 新增类型：本 const 块加一个取值，并在识别该类型的分支（如分账口径）里处理。
type MerchantKind string

const (
	// MerchantKindPlatform 是平台自营。
	MerchantKindPlatform MerchantKind = "platform"
	// MerchantKindPartner 是入驻商家。
	MerchantKindPartner MerchantKind = "partner"
)

// knownMerchantKinds 是写入白名单。
var knownMerchantKinds = map[MerchantKind]struct{}{
	MerchantKindPlatform: {},
	MerchantKindPartner:  {},
}

// Known 报告该商家类型是否在写入白名单内。
func (k MerchantKind) Known() bool {
	_, ok := knownMerchantKinds[k]
	return ok
}

// ValidateMerchantKind 是写入口校验：未知商家类型直接拒绝。
func ValidateMerchantKind(k MerchantKind) error {
	if !k.Known() {
		return fmt.Errorf("store: 未知的商家类型 %q", string(k))
	}
	return nil
}

// MerchantKindFromDB 把数据库列值转成 MerchantKind，未知值原样返回。
func MerchantKindFromDB(raw string) MerchantKind {
	return MerchantKind(raw)
}

// mysqlDuplicateEntry 是 MySQL 唯一键冲突的错误码。
const mysqlDuplicateEntry = 1062

// describeWriteError 把驱动错误转成可读错误；唯一键冲突单独给出口径。
//
// 管理面的写操作要求「幂等或明确报错」：冲突必须让人一眼看出是同键已存在，
// 而不是把 1062 原样抛出让人去查文档。
func describeWriteError(table string, err error) error {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateEntry {
		return fmt.Errorf("store: 写入 %s 失败：唯一键冲突，同键记录已存在: %w", table, err)
	}
	return fmt.Errorf("store: 写入 %s 失败: %w", table, err)
}

// Merchant 是 merchant 的一行。
type Merchant struct {
	ID        uint64       `json:"id"`
	Code      string       `json:"code"`
	Name      string       `json:"name"`
	Kind      MerchantKind `json:"kind"`
	Status    string       `json:"status"`
	CreatedAt time.Time    `json:"created_at"`
}

const insertMerchantSQL = `INSERT INTO merchant (code, name, kind, status) VALUES (?, ?, ?, ?)`

// insertMerchant 写一个商家；状态由调用方给出，create 路径一律 active。
func insertMerchant(ctx context.Context, ex executor, m Merchant) (uint64, error) {
	if strings.TrimSpace(m.Code) == "" {
		return 0, errors.New("store: merchant.code 不能为空")
	}
	if strings.TrimSpace(m.Name) == "" {
		return 0, errors.New("store: merchant.name 不能为空")
	}
	if err := ValidateMerchantKind(m.Kind); err != nil {
		return 0, err
	}
	status := m.Status
	if status == "" {
		status = StatusActive
	}
	res, err := ex.ExecContext(ctx, insertMerchantSQL, m.Code, m.Name, m.Kind, status)
	if err != nil {
		return 0, describeWriteError("merchant", err)
	}
	return insertID(res, nil, "merchant")
}

// InsertMerchant 写一个商家，返回新行 id。
func (s *Store) InsertMerchant(ctx context.Context, m Merchant) (uint64, error) {
	return insertMerchant(ctx, s.db, m)
}

const listMerchantsSQL = `SELECT id, code, name, kind, status, created_at FROM merchant ORDER BY id`

// listMerchants 列出全部商家。
func listMerchants(ctx context.Context, q querier) ([]Merchant, error) {
	rows, err := q.QueryContext(ctx, listMerchantsSQL)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 merchant 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var merchants []Merchant
	for rows.Next() {
		var (
			m         Merchant
			kindRaw   string
			createdAt scanTime
		)
		if err := rows.Scan(&m.ID, &m.Code, &m.Name, &kindRaw, &m.Status, &createdAt); err != nil {
			return nil, fmt.Errorf("store: 解析 merchant 行失败: %w", err)
		}
		m.Kind = MerchantKindFromDB(kindRaw)
		m.CreatedAt = createdAt.Time
		merchants = append(merchants, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 merchant 行失败: %w", err)
	}
	return merchants, nil
}

// ListMerchants 列出全部商家。
func (s *Store) ListMerchants(ctx context.Context) ([]Merchant, error) {
	return listMerchants(ctx, dbQuerier{db: s.db})
}

const setMerchantStatusSQL = "UPDATE merchant SET status = ? WHERE id = ?"

// setMerchantStatus 置位商家状态。
func setMerchantStatus(ctx context.Context, ex executor, id uint64, status string) error {
	if id == 0 {
		return errors.New("store: merchant.id 不能为 0")
	}
	if _, err := ex.ExecContext(ctx, setMerchantStatusSQL, status, id); err != nil {
		return fmt.Errorf("store: 更新 merchant.status 失败: %w", err)
	}
	return nil
}

// SetMerchantStatus 置位商家状态。
func (s *Store) SetMerchantStatus(ctx context.Context, id uint64, status string) error {
	return setMerchantStatus(ctx, s.db, id, status)
}

// Channel 是 upstream_channel 的一行。
//
// Config 是渠道级扩展配置（JSON），探针声明就写在这里。它带 `json:"-"`：
// 探针 headers 里可能有鉴权 token，管理面列表输出不回显配置原文。
type Channel struct {
	ID         uint64      `json:"id"`
	MerchantID uint64      `json:"merchant_id"`
	Name       string      `json:"name"`
	Vendor     string      `json:"vendor"`
	Type       ChannelType `json:"type"`
	CredGroup  string      `json:"cred_group"`
	BaseURL    string      `json:"base_url"`
	Priority   int         `json:"priority"`
	Weight     int         `json:"weight"`
	Enabled    bool        `json:"enabled"`
	// Config 是渠道级扩展配置（JSON），凭据注入形态与探针声明都写在这里。
	// 带 `json:"-"`：探针 headers 里可能有鉴权 token，管理面列表输出不回显配置原文。
	// 用 json.RawMessage 而不是 []byte：与 config 列的原义 JSON 同形，读取方按对象取键。
	Config json.RawMessage `json:"-"`
}

const insertChannelSQL = `INSERT INTO upstream_channel
  (merchant_id, name, vendor, type, cred_group, base_url, priority, weight, enabled, config)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`

// insertChannel 写一条渠道。新建一律启用，停用是独立动作。
func insertChannel(ctx context.Context, ex executor, c Channel) (uint64, error) {
	if c.MerchantID == 0 {
		return 0, errors.New("store: upstream_channel.merchant_id 不能为 0")
	}
	if strings.TrimSpace(c.Name) == "" {
		return 0, errors.New("store: upstream_channel.name 不能为空")
	}
	if err := ValidateChannelType(c.Type); err != nil {
		return 0, err
	}
	if strings.TrimSpace(c.CredGroup) == "" {
		return 0, errors.New("store: upstream_channel.cred_group 不能为空")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return 0, errors.New("store: upstream_channel.base_url 不能为空")
	}
	config, err := encodeChannelConfig(c.Config)
	if err != nil {
		return 0, err
	}
	res, err := ex.ExecContext(ctx, insertChannelSQL,
		c.MerchantID, c.Name, c.Vendor, c.Type, c.CredGroup, c.BaseURL, c.Priority, c.Weight, config)
	if err != nil {
		return 0, describeWriteError("upstream_channel", err)
	}
	return insertID(res, nil, "upstream_channel")
}

// encodeChannelConfig 把渠道扩展配置收敛成 SQL 参数。
//
// 空配置写成 NULL；非空必须是合法 JSON 对象。不是对象（数组、裸字符串）的配置
// 不该落库：读取方按对象取键，落库一个数组只会把错误推到很久以后的采集日志里。
func encodeChannelConfig(raw []byte) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, fmt.Errorf("store: upstream_channel.config 必须是 JSON 对象: %w", err)
	}
	if object == nil {
		return nil, errors.New("store: upstream_channel.config 必须是 JSON 对象")
	}
	return trimmed, nil
}

// InsertChannel 写一条渠道，返回新行 id。
func (s *Store) InsertChannel(ctx context.Context, c Channel) (uint64, error) {
	return insertChannel(ctx, s.db, c)
}

const listChannelsSQL = `SELECT id, merchant_id, name, vendor, type, cred_group, base_url, priority, weight, enabled, config
FROM upstream_channel ORDER BY id`

// listChannels 列出全部渠道。
func listChannels(ctx context.Context, q querier) ([]Channel, error) {
	rows, err := q.QueryContext(ctx, listChannelsSQL)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 upstream_channel 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var channels []Channel
	for rows.Next() {
		var (
			c       Channel
			typeRaw string
		)
		if err := rows.Scan(&c.ID, &c.MerchantID, &c.Name, &c.Vendor, &typeRaw,
			&c.CredGroup, &c.BaseURL, &c.Priority, &c.Weight, &c.Enabled, &c.Config); err != nil {
			return nil, fmt.Errorf("store: 解析 upstream_channel 行失败: %w", err)
		}
		c.Type = ChannelTypeFromDB(typeRaw)
		channels = append(channels, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 upstream_channel 行失败: %w", err)
	}
	return channels, nil
}

// ListChannels 列出全部渠道。
func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	return listChannels(ctx, dbQuerier{db: s.db})
}

const setChannelEnabledSQL = "UPDATE upstream_channel SET enabled = ? WHERE id = ?"

// setChannelEnabled 置位渠道启用标志。
func setChannelEnabled(ctx context.Context, ex executor, id uint64, enabled bool) error {
	if id == 0 {
		return errors.New("store: upstream_channel.id 不能为 0")
	}
	if _, err := ex.ExecContext(ctx, setChannelEnabledSQL, enabled, id); err != nil {
		return fmt.Errorf("store: 更新 upstream_channel.enabled 失败: %w", err)
	}
	return nil
}

// SetChannelEnabled 置位渠道启用标志。
func (s *Store) SetChannelEnabled(ctx context.Context, id uint64, enabled bool) error {
	return setChannelEnabled(ctx, s.db, id, enabled)
}

// CredentialRow 是 upstream_credential 的一行。
//
// Secret 带 `json:"-"`：它只在读取时用于脱敏成前缀，不进入任何 JSON 输出，
// 也不落日志。
type CredentialRow struct {
	ID         uint64 `json:"id"`
	MerchantID uint64 `json:"merchant_id"`
	CredGroup  string `json:"cred_group"`
	Name       string `json:"name"`
	Secret     []byte `json:"-"`
	Enabled    bool   `json:"enabled"`
}

//nolint:gosec // G101：这是 SQL 语句；命中的是表名与列名里的 credential / secret，不是凭据原文。
const insertCredentialSQL = `INSERT INTO upstream_credential (merchant_id, cred_group, name, secret, enabled)
VALUES (?, ?, ?, ?, 1)`

// insertCredential 写一行上游凭据。
//
// secret 由调用方序列化为 JSON：存储层不解释厂商专属字段，只保证它是合法 JSON。
func insertCredential(ctx context.Context, ex executor, c CredentialRow) (uint64, error) {
	if c.MerchantID == 0 {
		return 0, errors.New("store: upstream_credential.merchant_id 不能为 0")
	}
	if strings.TrimSpace(c.CredGroup) == "" {
		return 0, errors.New("store: upstream_credential.cred_group 不能为空")
	}
	if len(c.Secret) == 0 || !json.Valid(c.Secret) {
		return 0, errors.New("store: upstream_credential.secret 必须是合法 JSON")
	}
	res, err := ex.ExecContext(ctx, insertCredentialSQL, c.MerchantID, c.CredGroup, c.Name, c.Secret)
	if err != nil {
		return 0, describeWriteError("upstream_credential", err)
	}
	return insertID(res, nil, "upstream_credential")
}

// InsertCredential 写一行上游凭据，返回新行 id。
func (s *Store) InsertCredential(ctx context.Context, c CredentialRow) (uint64, error) {
	return insertCredential(ctx, s.db, c)
}

//nolint:gosec // G101：这是 SQL 语句；命中的是表名与列名里的 credential，不是凭据原文。
const listCredentialsSQL = `SELECT id, merchant_id, cred_group, name, secret, enabled
FROM upstream_credential ORDER BY id`

// listCredentials 列出全部上游凭据，含 secret 原文供调用方脱敏。
func listCredentials(ctx context.Context, q querier) ([]CredentialRow, error) {
	rows, err := q.QueryContext(ctx, listCredentialsSQL)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 upstream_credential 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var credentials []CredentialRow
	for rows.Next() {
		var c CredentialRow
		if err := rows.Scan(&c.ID, &c.MerchantID, &c.CredGroup, &c.Name, &c.Secret, &c.Enabled); err != nil {
			return nil, fmt.Errorf("store: 解析 upstream_credential 行失败: %w", err)
		}
		credentials = append(credentials, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 upstream_credential 行失败: %w", err)
	}
	return credentials, nil
}

// ListCredentials 列出全部上游凭据，含 secret 原文供调用方脱敏。
func (s *Store) ListCredentials(ctx context.Context) ([]CredentialRow, error) {
	return listCredentials(ctx, dbQuerier{db: s.db})
}

const setCredentialEnabledSQL = "UPDATE upstream_credential SET enabled = ? WHERE id = ?"

// setCredentialEnabled 置位凭据启用标志。
func setCredentialEnabled(ctx context.Context, ex executor, id uint64, enabled bool) error {
	if id == 0 {
		return errors.New("store: upstream_credential.id 不能为 0")
	}
	if _, err := ex.ExecContext(ctx, setCredentialEnabledSQL, enabled, id); err != nil {
		return fmt.Errorf("store: 更新 upstream_credential.enabled 失败: %w", err)
	}
	return nil
}

// SetCredentialEnabled 置位凭据启用标志。
func (s *Store) SetCredentialEnabled(ctx context.Context, id uint64, enabled bool) error {
	return setCredentialEnabled(ctx, s.db, id, enabled)
}

// ModelMap 是 upstream_model_map 的一行。
type ModelMap struct {
	ID               uint64          `json:"id"`
	ChannelID        uint64          `json:"channel_id"`
	Model            string          `json:"model"`
	UpstreamModel    string          `json:"upstream_model"`
	PriceMultiplier  string          `json:"price_multiplier"`
	RequestOverrides json.RawMessage `json:"request_overrides,omitempty"`
	Enabled          bool            `json:"enabled"`
}

// upsertModelMapSQL 用 ON DUPLICATE KEY UPDATE 实现 set 语义。
//
// set 是幂等动作：同一 (channel_id, model) 重复写入只覆盖取值，不产生第二行。
// 唯一键 uk_model_map 保证冲突判定在数据库侧完成，并发下不会写出两行。
const upsertModelMapSQL = `INSERT INTO upstream_model_map
  (channel_id, model, upstream_model, price_multiplier, request_overrides, enabled)
VALUES (?, ?, ?, ?, ?, 1)
ON DUPLICATE KEY UPDATE upstream_model = VALUES(upstream_model),
  price_multiplier = VALUES(price_multiplier), request_overrides = VALUES(request_overrides), enabled = 1`

// upsertModelMap 写入或覆盖一条渠道模型映射。
func upsertModelMap(ctx context.Context, ex executor, m ModelMap) (uint64, error) {
	if m.ChannelID == 0 {
		return 0, errors.New("store: upstream_model_map.channel_id 不能为 0")
	}
	if strings.TrimSpace(m.Model) == "" {
		return 0, errors.New("store: upstream_model_map.model 不能为空")
	}
	if strings.TrimSpace(m.UpstreamModel) == "" {
		return 0, errors.New("store: upstream_model_map.upstream_model 不能为空")
	}
	if strings.TrimSpace(m.PriceMultiplier) == "" {
		return 0, errors.New("store: upstream_model_map.price_multiplier 不能为空")
	}
	if len(m.RequestOverrides) > 0 && !json.Valid(m.RequestOverrides) {
		return 0, errors.New("store: upstream_model_map.request_overrides 必须是合法 JSON")
	}
	res, err := ex.ExecContext(ctx, upsertModelMapSQL,
		m.ChannelID, m.Model, m.UpstreamModel, m.PriceMultiplier, nullableJSON(m.RequestOverrides))
	if err != nil {
		return 0, describeWriteError("upstream_model_map", err)
	}
	// upsert 命中已有行时 LastInsertId 不可靠，返回行 id 由调用方按 (channel, model) 查询；
	// 这里给出 LastInsertId 仅用于新建路径的展示。
	// upsert 命中已有行时 LastInsertId 不可靠（MySQL 对 ON DUPLICATE KEY UPDATE 的更新
	// 路径返回 0），返回值只供新建路径展示，取不到不算错误。
	id, _ := res.LastInsertId()
	if id < 0 {
		return 0, nil
	}
	return uint64(id), nil
}

// UpsertModelMap 写入或覆盖一条渠道模型映射。
func (s *Store) UpsertModelMap(ctx context.Context, m ModelMap) (uint64, error) {
	return upsertModelMap(ctx, s.db, m)
}

const listModelMapsSQL = `SELECT id, channel_id, model, upstream_model, price_multiplier, request_overrides, enabled
FROM upstream_model_map ORDER BY id`

// listModelMaps 列出全部模型映射。
func listModelMaps(ctx context.Context, q querier) ([]ModelMap, error) {
	rows, err := q.QueryContext(ctx, listModelMapsSQL)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 upstream_model_map 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var maps []ModelMap
	for rows.Next() {
		var (
			m         ModelMap
			overrides []byte
		)
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.Model, &m.UpstreamModel,
			&m.PriceMultiplier, &overrides, &m.Enabled); err != nil {
			return nil, fmt.Errorf("store: 解析 upstream_model_map 行失败: %w", err)
		}
		m.RequestOverrides = json.RawMessage(overrides)
		maps = append(maps, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 upstream_model_map 行失败: %w", err)
	}
	return maps, nil
}

// ListModelMaps 列出全部模型映射。
func (s *Store) ListModelMaps(ctx context.Context) ([]ModelMap, error) {
	return listModelMaps(ctx, dbQuerier{db: s.db})
}

const setModelMapEnabledSQL = "UPDATE upstream_model_map SET enabled = ? WHERE id = ?"

// setModelMapEnabled 置位模型映射启用标志。
func setModelMapEnabled(ctx context.Context, ex executor, id uint64, enabled bool) error {
	if id == 0 {
		return errors.New("store: upstream_model_map.id 不能为 0")
	}
	if _, err := ex.ExecContext(ctx, setModelMapEnabledSQL, enabled, id); err != nil {
		return fmt.Errorf("store: 更新 upstream_model_map.enabled 失败: %w", err)
	}
	return nil
}

// SetModelMapEnabled 置位模型映射启用标志。
func (s *Store) SetModelMapEnabled(ctx context.Context, id uint64, enabled bool) error {
	return setModelMapEnabled(ctx, s.db, id, enabled)
}

// Account 是 account 的一行。
type Account struct {
	ID                uint64  `json:"id"`
	Code              string  `json:"code"`
	Name              string  `json:"name"`
	DefaultMerchantID *uint64 `json:"default_merchant_id"`
	PriceMultiplier   string  `json:"price_multiplier"`
	Status            string  `json:"status"`
}

const insertAccountSQL = `INSERT INTO account (code, name, default_merchant_id, price_multiplier, status)
VALUES (?, ?, ?, ?, ?)`

// insertAccount 写一个账户。
func insertAccount(ctx context.Context, ex executor, a Account) (uint64, error) {
	if strings.TrimSpace(a.Code) == "" {
		return 0, errors.New("store: account.code 不能为空")
	}
	if strings.TrimSpace(a.Name) == "" {
		return 0, errors.New("store: account.name 不能为空")
	}
	if strings.TrimSpace(a.PriceMultiplier) == "" {
		return 0, errors.New("store: account.price_multiplier 不能为空")
	}
	status := a.Status
	if status == "" {
		status = StatusActive
	}
	var merchant any
	if a.DefaultMerchantID != nil {
		merchant = *a.DefaultMerchantID
	}
	res, err := ex.ExecContext(ctx, insertAccountSQL, a.Code, a.Name, merchant, a.PriceMultiplier, status)
	if err != nil {
		return 0, describeWriteError("account", err)
	}
	return insertID(res, nil, "account")
}

// InsertAccount 写一个账户，返回新行 id。
func (s *Store) InsertAccount(ctx context.Context, a Account) (uint64, error) {
	return insertAccount(ctx, s.db, a)
}

const listAccountsSQL = `SELECT id, code, name, default_merchant_id, price_multiplier, status
FROM account ORDER BY id`

// listAccounts 列出全部账户。
func listAccounts(ctx context.Context, q querier) ([]Account, error) {
	rows, err := q.QueryContext(ctx, listAccountsSQL)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 account 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var accounts []Account
	for rows.Next() {
		var (
			a        Account
			merchant sql.NullInt64
		)
		if err := rows.Scan(&a.ID, &a.Code, &a.Name, &merchant, &a.PriceMultiplier, &a.Status); err != nil {
			return nil, fmt.Errorf("store: 解析 account 行失败: %w", err)
		}
		if merchant.Valid && merchant.Int64 > 0 {
			v := uint64(merchant.Int64)
			a.DefaultMerchantID = &v
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 account 行失败: %w", err)
	}
	return accounts, nil
}

// ListAccounts 列出全部账户。
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	return listAccounts(ctx, dbQuerier{db: s.db})
}

const accountByIDSQL = `SELECT id, code, name, default_merchant_id, price_multiplier, status
FROM account WHERE id = ?`

// Account 按 id 查账户；无匹配时错误可用 errors.Is(err, sql.ErrNoRows) 判断。
//
// 自助查询端点只用得到 id 与 code，但仍返回整行：账户行本身没有敏感字段，
// 按端点裁剪列会让同一张表出现两份列清单，加列时容易只改一处。
func (s *Store) Account(ctx context.Context, id uint64) (*Account, error) {
	if id == 0 {
		return nil, errors.New("store: account.id 不能为 0")
	}
	var (
		a        Account
		merchant sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, accountByIDSQL, id).Scan(
		&a.ID, &a.Code, &a.Name, &merchant, &a.PriceMultiplier, &a.Status)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 account 失败: %w", err)
	}
	if merchant.Valid && merchant.Int64 > 0 {
		v := uint64(merchant.Int64)
		a.DefaultMerchantID = &v
	}
	return &a, nil
}

const setAccountStatusSQL = "UPDATE account SET status = ? WHERE id = ?"
const setAccountMultiplierSQL = "UPDATE account SET price_multiplier = ? WHERE id = ?"
const setAccountMerchantSQL = "UPDATE account SET default_merchant_id = ? WHERE id = ?"

// setAccountStatus 置位账户状态。
func setAccountStatus(ctx context.Context, ex executor, id uint64, status string) error {
	if id == 0 {
		return errors.New("store: account.id 不能为 0")
	}
	if _, err := ex.ExecContext(ctx, setAccountStatusSQL, status, id); err != nil {
		return fmt.Errorf("store: 更新 account.status 失败: %w", err)
	}
	return nil
}

// SetAccountStatus 置位账户状态。
func (s *Store) SetAccountStatus(ctx context.Context, id uint64, status string) error {
	return setAccountStatus(ctx, s.db, id, status)
}

// setAccountMultiplier 置位账户倍率。
func setAccountMultiplier(ctx context.Context, ex executor, id uint64, multiplier string) error {
	if id == 0 {
		return errors.New("store: account.id 不能为 0")
	}
	if strings.TrimSpace(multiplier) == "" {
		return errors.New("store: account.price_multiplier 不能为空")
	}
	if _, err := ex.ExecContext(ctx, setAccountMultiplierSQL, multiplier, id); err != nil {
		return fmt.Errorf("store: 更新 account.price_multiplier 失败: %w", err)
	}
	return nil
}

// SetAccountMultiplier 置位账户倍率。
func (s *Store) SetAccountMultiplier(ctx context.Context, id uint64, multiplier string) error {
	return setAccountMultiplier(ctx, s.db, id, multiplier)
}

// setAccountMerchant 置位账户默认商家。
func setAccountMerchant(ctx context.Context, ex executor, id, merchantID uint64) error {
	if id == 0 {
		return errors.New("store: account.id 不能为 0")
	}
	if merchantID == 0 {
		return errors.New("store: account.default_merchant_id 不能为 0")
	}
	if _, err := ex.ExecContext(ctx, setAccountMerchantSQL, merchantID, id); err != nil {
		return fmt.Errorf("store: 更新 account.default_merchant_id 失败: %w", err)
	}
	return nil
}

// SetAccountMerchant 置位账户默认商家。
func (s *Store) SetAccountMerchant(ctx context.Context, id, merchantID uint64) error {
	return setAccountMerchant(ctx, s.db, id, merchantID)
}

// APIKey 是 account_api_key 的一行。
//
// KeyHash 带 `json:"-"`：列表只展示 KeyPrefix，哈希不外显。
type APIKey struct {
	ID         uint64     `json:"id"`
	AccountID  uint64     `json:"account_id"`
	MerchantID *uint64    `json:"merchant_id"`
	Name       string     `json:"name"`
	KeyHash    string     `json:"-"`
	KeyPrefix  string     `json:"key_prefix"`
	Enabled    bool       `json:"enabled"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

//nolint:gosec // G101：这是 SQL 语句；命中的是表名与列名里的 key，不是凭据原文。
const insertAPIKeySQL = `INSERT INTO account_api_key (account_id, merchant_id, name, key_hash, key_prefix, enabled, expires_at)
VALUES (?, ?, ?, ?, ?, 1, ?)`

// insertAPIKey 写一行客户端密钥；只存哈希与前缀，明文由调用方一次性输出。
func insertAPIKey(ctx context.Context, ex executor, k APIKey) (uint64, error) {
	if k.AccountID == 0 {
		return 0, errors.New("store: account_api_key.account_id 不能为 0")
	}
	if strings.TrimSpace(k.KeyHash) == "" {
		return 0, errors.New("store: account_api_key.key_hash 不能为空")
	}
	if strings.TrimSpace(k.KeyPrefix) == "" {
		return 0, errors.New("store: account_api_key.key_prefix 不能为空")
	}
	var merchant any
	if k.MerchantID != nil {
		merchant = *k.MerchantID
	}
	res, err := ex.ExecContext(ctx, insertAPIKeySQL,
		k.AccountID, merchant, k.Name, k.KeyHash, k.KeyPrefix, timeArg(k.ExpiresAt))
	if err != nil {
		return 0, describeWriteError("account_api_key", err)
	}
	return insertID(res, nil, "account_api_key")
}

// InsertAPIKey 写一行客户端密钥，返回新行 id。
func (s *Store) InsertAPIKey(ctx context.Context, k APIKey) (uint64, error) {
	return insertAPIKey(ctx, s.db, k)
}

//nolint:gosec // G101：这是 SQL 语句；命中的是表名与列名里的 key，不是凭据原文。
const listAPIKeysSQL = `SELECT id, account_id, merchant_id, name, key_prefix, enabled, expires_at, last_used_at
FROM account_api_key ORDER BY id`

// listAPIKeys 列出全部客户端密钥，只含前缀不含哈希。
func listAPIKeys(ctx context.Context, q querier) ([]APIKey, error) {
	rows, err := q.QueryContext(ctx, listAPIKeysSQL)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 account_api_key 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var keys []APIKey
	for rows.Next() {
		var (
			k          APIKey
			merchant   sql.NullInt64
			expiresAt  scanTime
			lastUsedAt scanTime
		)
		if err := rows.Scan(&k.ID, &k.AccountID, &merchant, &k.Name, &k.KeyPrefix,
			&k.Enabled, &expiresAt, &lastUsedAt); err != nil {
			return nil, fmt.Errorf("store: 解析 account_api_key 行失败: %w", err)
		}
		if merchant.Valid && merchant.Int64 > 0 {
			v := uint64(merchant.Int64)
			k.MerchantID = &v
		}
		if expiresAt.Valid {
			k.ExpiresAt = &expiresAt.Time
		}
		if lastUsedAt.Valid {
			k.LastUsedAt = &lastUsedAt.Time
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 account_api_key 行失败: %w", err)
	}
	return keys, nil
}

// ListAPIKeys 列出全部客户端密钥，只含前缀不含哈希。
func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	return listAPIKeys(ctx, dbQuerier{db: s.db})
}

const setAPIKeyEnabledSQL = "UPDATE account_api_key SET enabled = ? WHERE id = ?"

// setAPIKeyEnabled 置位密钥启用标志；revoke 即置 false。
func setAPIKeyEnabled(ctx context.Context, ex executor, id uint64, enabled bool) error {
	if id == 0 {
		return errors.New("store: account_api_key.id 不能为 0")
	}
	if _, err := ex.ExecContext(ctx, setAPIKeyEnabledSQL, enabled, id); err != nil {
		return fmt.Errorf("store: 更新 account_api_key.enabled 失败: %w", err)
	}
	return nil
}

// SetAPIKeyEnabled 置位密钥启用标志。
func (s *Store) SetAPIKeyEnabled(ctx context.Context, id uint64, enabled bool) error {
	return setAPIKeyEnabled(ctx, s.db, id, enabled)
}

// BucketRow 是 account_bucket 的一行，供管理面列表与发放使用。
//
// UnitRate 为 nil 即 SQL NULL，表示该账本没有锁定的折算率（赠送、手工充值）；
// 购买生成的账本写入 price / qty。NULL 与 0 在扣减时同义，见 internal/settlement。
type BucketRow struct {
	ID         uint64             `json:"id"`
	AccountID  uint64             `json:"account_id"`
	MerchantID uint64             `json:"merchant_id"`
	Unit       billing.UnitSettle `json:"unit"`
	Total      string             `json:"total"`
	Remaining  string             `json:"remaining"`
	ExpiresAt  *time.Time         `json:"expires_at"`
	Fallback   billing.Fallback   `json:"fallback"`
	Source     billing.Source     `json:"source"`
	Priority   int                `json:"priority"`
	UnitRate   *string            `json:"unit_rate"`
}

const insertBucketSQL = `INSERT INTO account_bucket
  (account_id, merchant_id, unit, total, remaining, expires_at, fallback, source, priority, unit_rate)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// insertBucket 写一行账本；remaining 初值等于 total。
func insertBucket(ctx context.Context, ex executor, b BucketRow) (uint64, error) {
	if b.AccountID == 0 {
		return 0, errors.New("store: account_bucket.account_id 不能为 0")
	}
	if b.MerchantID == 0 {
		return 0, errors.New("store: account_bucket.merchant_id 不能为 0")
	}
	if err := billing.ValidateUnitSettle(b.Unit); err != nil {
		return 0, err
	}
	if strings.TrimSpace(b.Total) == "" || strings.TrimSpace(b.Remaining) == "" {
		return 0, errors.New("store: account_bucket.total / remaining 不能为空")
	}
	if err := billing.ValidateFallback(b.Fallback); err != nil {
		return 0, err
	}
	if err := billing.ValidateSource(b.Source); err != nil {
		return 0, err
	}
	res, err := ex.ExecContext(ctx, insertBucketSQL,
		b.AccountID, b.MerchantID, b.Unit, b.Total, b.Remaining, timeArg(b.ExpiresAt),
		b.Fallback, b.Source, b.Priority, optionalStringArg(b.UnitRate))
	if err != nil {
		return 0, describeWriteError("account_bucket", err)
	}
	return insertID(res, nil, "account_bucket")
}

// InsertBucket 写一行账本，返回新行 id。
func (s *Store) InsertBucket(ctx context.Context, b BucketRow) (uint64, error) {
	return insertBucket(ctx, s.db, b)
}

const listBucketsSQL = `SELECT id, account_id, merchant_id, unit, total, remaining, expires_at, fallback, source, priority, unit_rate
FROM account_bucket`

const listBucketsByAccountSQL = listBucketsSQL + " WHERE account_id = ? ORDER BY id"

// listBuckets 列出账本；accountID 为 0 时列出全部。
func listBuckets(ctx context.Context, q querier, accountID uint64) ([]BucketRow, error) {
	query := listBucketsSQL + " ORDER BY id"
	args := []any(nil)
	if accountID != 0 {
		query = listBucketsByAccountSQL
		args = append(args, accountID)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 account_bucket 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var buckets []BucketRow
	for rows.Next() {
		var (
			b           BucketRow
			unitRaw     string
			fallbackRaw string
			sourceRaw   string
			expiresAt   scanTime
			unitRate    sql.NullString
		)
		if err := rows.Scan(&b.ID, &b.AccountID, &b.MerchantID, &unitRaw, &b.Total, &b.Remaining,
			&expiresAt, &fallbackRaw, &sourceRaw, &b.Priority, &unitRate); err != nil {
			return nil, fmt.Errorf("store: 解析 account_bucket 行失败: %w", err)
		}
		b.Unit = billing.UnitSettleFromDB(unitRaw)
		b.Fallback = billing.FallbackFromDB(fallbackRaw)
		b.Source = billing.SourceFromDB(sourceRaw)
		if expiresAt.Valid {
			b.ExpiresAt = &expiresAt.Time
		}
		if unitRate.Valid {
			v := unitRate.String
			b.UnitRate = &v
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 account_bucket 行失败: %w", err)
	}
	return buckets, nil
}

// ListBuckets 列出账本；accountID 为 0 时列出全部。
func (s *Store) ListBuckets(ctx context.Context, accountID uint64) ([]BucketRow, error) {
	return listBuckets(ctx, dbQuerier{db: s.db}, accountID)
}

const listProductsSQL = `SELECT id, merchant_id, name, unit, qty, price, model_scope, validity_days
FROM merchant_product ORDER BY id`

// listProducts 列出全部商品档位。
func listProducts(ctx context.Context, q querier) ([]Product, error) {
	rows, err := q.QueryContext(ctx, listProductsSQL)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 merchant_product 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var products []Product
	for rows.Next() {
		var (
			p        Product
			unitRaw  string
			scopeRaw []byte
		)
		if err := rows.Scan(&p.ID, &p.MerchantID, &p.Name, &unitRaw, &p.Qty, &p.Price, &scopeRaw, &p.ValidityDays); err != nil {
			return nil, fmt.Errorf("store: 解析 merchant_product 行失败: %w", err)
		}
		p.Unit = billing.UnitSettleFromDB(unitRaw)
		p.ModelScope = scopeRaw
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 merchant_product 行失败: %w", err)
	}
	return products, nil
}

// ListProducts 列出全部商品档位。
func (s *Store) ListProducts(ctx context.Context) ([]Product, error) {
	return listProducts(ctx, dbQuerier{db: s.db})
}

const listPurchasesSQL = `SELECT id, account_id, merchant_id, product_id, qty, price_paid, purchased_at
FROM account_purchase`

const listPurchasesByAccountSQL = listPurchasesSQL + " WHERE account_id = ? ORDER BY id"

// listPurchases 列出购买记录；accountID 为 0 时列出全部。
func listPurchases(ctx context.Context, q querier, accountID uint64) ([]Purchase, error) {
	query := listPurchasesSQL + " ORDER BY id"
	args := []any(nil)
	if accountID != 0 {
		query = listPurchasesByAccountSQL
		args = append(args, accountID)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 account_purchase 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var purchases []Purchase
	for rows.Next() {
		var (
			p           Purchase
			purchasedAt scanTime
		)
		if err := rows.Scan(&p.ID, &p.AccountID, &p.MerchantID, &p.ProductID, &p.Qty, &p.PricePaid, &purchasedAt); err != nil {
			return nil, fmt.Errorf("store: 解析 account_purchase 行失败: %w", err)
		}
		p.PurchasedAt = purchasedAt.Time
		purchases = append(purchases, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 account_purchase 行失败: %w", err)
	}
	return purchases, nil
}

// ListPurchases 列出购买记录；accountID 为 0 时列出全部。
func (s *Store) ListPurchases(ctx context.Context, accountID uint64) ([]Purchase, error) {
	return listPurchases(ctx, dbQuerier{db: s.db}, accountID)
}

// CreatePurchaseAndBucket 在同一事务内写购买记录与派生账本，返回购买行 id 与账本行 id。
//
// 购买是「记一笔事实 + 发一份存量」的两步写入，拆在事务外会在中途失败时留下
// 已付款却没有账本的记录。存储层因此提供这个组合动作，业务口径（数量、金额、
// 有效期怎么算）仍由调用方在进入本方法前算好。
func (s *Store) CreatePurchaseAndBucket(ctx context.Context, p Purchase, b BucketRow) (uint64, uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("store: 开启购买事务失败: %w", err)
	}
	purchaseID, err := insertPurchase(ctx, tx, p)
	if err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	bucketID, err := insertBucket(ctx, tx, b)
	if err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("store: 提交购买事务失败: %w", err)
	}
	return purchaseID, bucketID, nil
}

const listPricingSQL = `SELECT id, merchant_id, model, version, effective_at, retired_at
FROM billing_pricing`

// listPricing 列出定价版本；merchantID 或 model 为 0 / 空时不做该维度过滤。
func listPricing(ctx context.Context, q querier, merchantID uint64, model string) ([]Pricing, error) {
	query := listPricingSQL
	var (
		conditions []string
		args       []any
	)
	if merchantID != 0 {
		conditions = append(conditions, "merchant_id = ?")
		args = append(args, merchantID)
	}
	if strings.TrimSpace(model) != "" {
		conditions = append(conditions, "model = ?")
		args = append(args, model)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY merchant_id, model, version"

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 billing_pricing 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var versions []Pricing
	for rows.Next() {
		var (
			p           Pricing
			effectiveAt scanTime
			retiredAt   scanTime
		)
		if err := rows.Scan(&p.ID, &p.MerchantID, &p.Model, &p.Version, &effectiveAt, &retiredAt); err != nil {
			return nil, fmt.Errorf("store: 解析 billing_pricing 行失败: %w", err)
		}
		p.EffectiveAt = effectiveAt.Time
		if retiredAt.Valid {
			p.RetiredAt = &retiredAt.Time
		}
		versions = append(versions, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 billing_pricing 行失败: %w", err)
	}
	return versions, nil
}

// ListPricing 列出定价版本；merchantID 或 model 为 0 / 空时不做该维度过滤。
func (s *Store) ListPricing(ctx context.Context, merchantID uint64, model string) ([]Pricing, error) {
	return listPricing(ctx, dbQuerier{db: s.db}, merchantID, model)
}

// maxPricingVersionSQL 取当前最大版本号；聚合与 FOR UPDATE 同用会锁住命中的行。
const maxPricingVersionSQL = `SELECT COALESCE(MAX(version), 0) FROM billing_pricing WHERE merchant_id = ? AND model = ? FOR UPDATE`

// PublishPricing 发布一个定价版本：置位旧 active、分配新版本号、写分量。
//
// 版本号在事务内取 MAX+1 而不是调用方算：并发发布同一模型时由行锁串行化，
// 不会出现两个版本号相同的行。置位与插行在同一事务里，避免留下无 active 定价的窗口。
// effectiveAt 同时用作旧版本的 retired_at：新旧版本在新版生效时刻完成交替。
func (s *Store) PublishPricing(ctx context.Context, merchantID uint64, model string, effectiveAt time.Time, components []PriceComponent) (*Pricing, error) {
	if merchantID == 0 {
		return nil, errors.New("store: billing_pricing.merchant_id 不能为 0")
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("store: billing_pricing.model 不能为空")
	}
	if effectiveAt.IsZero() {
		return nil, errors.New("store: billing_pricing.effective_at 不能为零值")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: 开启定价发布事务失败: %w", err)
	}
	var current int
	if queryErr := tx.QueryRowContext(ctx, maxPricingVersionSQL, merchantID, model).Scan(&current); queryErr != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("store: 读取 billing_pricing 当前版本失败: %w", queryErr)
	}
	version := current + 1
	if _, execErr := tx.ExecContext(ctx, retireActivePricingSQL, effectiveAt, merchantID, model); execErr != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("store: 置位 billing_pricing.retired_at 失败: %w", execErr)
	}
	pricingID, err := insertPricing(ctx, tx, Pricing{
		MerchantID: merchantID, Model: model, Version: version, EffectiveAt: effectiveAt,
	})
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	for i := range components {
		components[i].PricingID = pricingID
	}
	if err := insertPriceComponents(ctx, tx, components); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: 提交定价发布事务失败: %w", err)
	}
	return &Pricing{
		ID: pricingID, MerchantID: merchantID, Model: model, Version: version, EffectiveAt: effectiveAt,
	}, nil
}

const listCalendarDaysSQL = "SELECT `date`, day_kind FROM sys_calendar WHERE calendar = ? ORDER BY `date`"

// listCalendarDays 列出某日历的全部日期。
func listCalendarDays(ctx context.Context, q querier, calendar string) ([]CalendarDay, error) {
	if strings.TrimSpace(calendar) == "" {
		return nil, errors.New("store: sys_calendar.calendar 不能为空")
	}
	rows, err := q.QueryContext(ctx, listCalendarDaysSQL, calendar)
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

// ListCalendarDays 列出某日历的全部日期。
func (s *Store) ListCalendarDays(ctx context.Context, calendar string) ([]CalendarDay, error) {
	return listCalendarDays(ctx, dbQuerier{db: s.db}, calendar)
}

const listUsageSQL = `SELECT id, merchant_id, account_id, channel_id, model, ` + "`usage`" +
	`, gross_amount, multiplier, settlement, created_at
FROM billing_usage`

// UsageListRow 是 billing_usage 的一行，供管理面列表使用。
//
// Usage 与 Settlement 保持原始 JSON：管理面只展示，不解释其结构。
type UsageListRow struct {
	ID          uint64          `json:"id"`
	MerchantID  uint64          `json:"merchant_id"`
	AccountID   uint64          `json:"account_id"`
	ChannelID   uint64          `json:"channel_id"`
	Model       string          `json:"model"`
	Usage       json.RawMessage `json:"usage"`
	GrossAmount string          `json:"gross_amount"`
	Multiplier  string          `json:"multiplier"`
	Settlement  json.RawMessage `json:"settlement,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// listUsage 列出用量流水；accountID 为 0 时列出全部，since 为零值时不做时间过滤。
func listUsage(ctx context.Context, q querier, accountID uint64, since time.Time) ([]UsageListRow, error) {
	query := listUsageSQL
	var (
		conditions []string
		args       []any
	)
	if accountID != 0 {
		conditions = append(conditions, "account_id = ?")
		args = append(args, accountID)
	}
	if !since.IsZero() {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, since)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY id"

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 billing_usage 失败: %w", err)
	}
	return scanUsageRows(rows)
}

// scanUsageRows 解析流水结果集；列表查询与最近流水查询共用同一份行解析。
func scanUsageRows(rows rowIter) ([]UsageListRow, error) {
	defer func() { _ = rows.Close() }()

	var records []UsageListRow
	for rows.Next() {
		var (
			r          UsageListRow
			usageRaw   []byte
			settlement []byte
			createdAt  scanTime
		)
		if err := rows.Scan(&r.ID, &r.MerchantID, &r.AccountID, &r.ChannelID, &r.Model,
			&usageRaw, &r.GrossAmount, &r.Multiplier, &settlement, &createdAt); err != nil {
			return nil, fmt.Errorf("store: 解析 billing_usage 行失败: %w", err)
		}
		r.Usage = json.RawMessage(usageRaw)
		r.Settlement = json.RawMessage(settlement)
		r.CreatedAt = createdAt.Time
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 billing_usage 行失败: %w", err)
	}
	return records, nil
}

// ListUsage 列出用量流水；accountID 为 0 时列出全部，since 为零值时不做时间过滤。
func (s *Store) ListUsage(ctx context.Context, accountID uint64, since time.Time) ([]UsageListRow, error) {
	return listUsage(ctx, dbQuerier{db: s.db}, accountID, since)
}

const recentUsageSQL = listUsageSQL + " WHERE account_id = ? ORDER BY id DESC LIMIT ?"

// RecentUsage 按 id 倒序读账户最近的若干条流水。
//
// limit <= 0 时返回空集：调用方传 0 表示不需要流水，不必到数据库空跑一次。
// 金额与倍率保持库中文本，由调用方决定怎么折算成付费金额。
func (s *Store) RecentUsage(ctx context.Context, accountID uint64, limit int) ([]UsageListRow, error) {
	if accountID == 0 {
		return nil, errors.New("store: billing_usage.account_id 不能为 0")
	}
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, recentUsageSQL, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 billing_usage 失败: %w", err)
	}
	return scanUsageRows(rows)
}

const listAdjustmentsSQL = `SELECT id, account_id, delta_amount, reason, operator, created_at FROM billing_adjustment`

const listAdjustmentsByAccountSQL = listAdjustmentsSQL + " WHERE account_id = ? ORDER BY id"

// listAdjustments 列出调账记录；accountID 为 0 时列出全部。
func listAdjustments(ctx context.Context, q querier, accountID uint64) ([]Adjustment, error) {
	query := listAdjustmentsSQL + " ORDER BY id"
	args := []any(nil)
	if accountID != 0 {
		query = listAdjustmentsByAccountSQL
		args = append(args, accountID)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 billing_adjustment 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var adjustments []Adjustment
	for rows.Next() {
		var (
			a         Adjustment
			createdAt scanTime
		)
		if err := rows.Scan(&a.ID, &a.AccountID, &a.DeltaAmount, &a.Reason, &a.Operator, &createdAt); err != nil {
			return nil, fmt.Errorf("store: 解析 billing_adjustment 行失败: %w", err)
		}
		a.CreatedAt = createdAt.Time
		adjustments = append(adjustments, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 billing_adjustment 行失败: %w", err)
	}
	return adjustments, nil
}

// ListAdjustments 列出调账记录；accountID 为 0 时列出全部。
func (s *Store) ListAdjustments(ctx context.Context, accountID uint64) ([]Adjustment, error) {
	return listAdjustments(ctx, dbQuerier{db: s.db}, accountID)
}
