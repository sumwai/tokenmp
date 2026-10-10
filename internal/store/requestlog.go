package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 本文件是 0009 迁移引入的请求记录表的读写入口。
//
// 写入分两组、互不覆盖：归属与用量由记账路径先写，终态字段由访问日志路径后写。
// 两组都走 `INSERT ... ON DUPLICATE KEY UPDATE`，只更新自己负责的列 —— 一次请求
// 的两次写入各占一半事实，谁先到都能落库，谁也不会把对方的列写回零值。
//
// created_at 只在首次插入时写入，不在更新列表里：两次写入的时刻差值属毫秒级，
// 但「请求时刻」只能有一个取值，后到的写入无权改写它。
//
// 账户面的读取一律带 account_id 条件：作用域来自会话，不给「按 request_id 直查」留越权面。
// 管理面（ops）的读路径允许省略账户，用于跨账户排障；越权面仍由会话推导的作用域收敛，
// 不带账户的入口因此只挂在管理面。

// RequestAttempt 是一条尝试时间线记录。
type RequestAttempt struct {
	RequestID string
	// Attempt 是尝试序号，从 1 起。
	Attempt int
	// Outcome 是本次尝试的结果：ok | failed | cancelled | skipped。
	Outcome string
	// UpstreamStatus 是本次尝试取得的上游状态码；0 表示未取得。
	UpstreamStatus int
	FailureClass   string
	ErrorCode      string
	// ClientProtocol / UpstreamProtocol 是本次尝试两侧的线协议方言。
	ClientProtocol   string
	UpstreamProtocol string
	CrossProtocol    bool
	// DurationMS 是本次上游调用的耗时，不含向客户端写出的耗时。
	DurationMS     int64
	RequestedModel string
	UpstreamModel  string
	// RewrittenParts 是网关对报文做过的改写标注，原始 JSON 数组。
	RewrittenParts json.RawMessage
	CreatedAt      time.Time
}

// InsertRequestAttempt 写一条尝试记录。
//
// 同一 (request_id, attempt) 重复写入按覆盖处理：同一序号只可能来自同一次尝试的
// 重复上报，保留最后一次上报的事实比插入失败更有用。
func (s *Store) InsertRequestAttempt(ctx context.Context, a RequestAttempt) error {
	if strings.TrimSpace(a.RequestID) == "" {
		return errors.New("store: request_attempt.request_id 不能为空")
	}
	if a.Attempt < 1 {
		return errors.New("store: request_attempt.attempt 必须从 1 起")
	}
	if a.CreatedAt.IsZero() {
		return errors.New("store: request_attempt.created_at 不能为零值")
	}
	_, err := s.db.ExecContext(ctx, insertRequestAttemptSQL,
		a.RequestID, a.Attempt, a.Outcome, nullableInt(a.UpstreamStatus), nullableString(a.FailureClass),
		nullableString(a.ErrorCode), nullableString(a.ClientProtocol), nullableString(a.UpstreamProtocol),
		a.CrossProtocol, a.DurationMS, nullableString(a.RequestedModel), nullableString(a.UpstreamModel),
		nullableJSON(a.RewrittenParts), a.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: 写 request_attempt 失败: %w", err)
	}
	return nil
}

const insertRequestAttemptSQL = `INSERT INTO request_attempt (request_id, attempt, outcome, upstream_status,
failure_class, error_code, client_protocol, upstream_protocol, cross_protocol, duration_ms,
requested_model, upstream_model, rewritten_parts, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE outcome = VALUES(outcome), upstream_status = VALUES(upstream_status),
failure_class = VALUES(failure_class), error_code = VALUES(error_code),
client_protocol = VALUES(client_protocol), upstream_protocol = VALUES(upstream_protocol),
cross_protocol = VALUES(cross_protocol), duration_ms = VALUES(duration_ms),
requested_model = VALUES(requested_model), upstream_model = VALUES(upstream_model),
rewritten_parts = VALUES(rewritten_parts), created_at = VALUES(created_at)`

// RequestUsage 是请求记录里由记账路径写入的那部分：归属与用量。
type RequestUsage struct {
	RequestID  string
	MerchantID uint64
	AccountID  uint64
	APIKeyID   uint64
	CreatedAt  time.Time
	// Usage 是取得的 token 用量，原始 JSON；未取得时为 nil。
	Usage json.RawMessage
}

// UpsertRequestUsage 写入请求记录的归属与用量。
//
// 这一组字段在回写客户端之前就能确定，因此先于终态落库：流式请求的用量在流结束时
// 才取得，此时请求已经写过一次，靠 ON DUPLICATE KEY UPDATE 补列而不是另起一行。
func (s *Store) UpsertRequestUsage(ctx context.Context, u RequestUsage) error {
	if strings.TrimSpace(u.RequestID) == "" {
		return errors.New("store: request_log.request_id 不能为空")
	}
	if u.AccountID == 0 {
		return errors.New("store: request_log.account_id 不能为 0")
	}
	if u.CreatedAt.IsZero() {
		return errors.New("store: request_log.created_at 不能为零值")
	}
	_, err := s.db.ExecContext(ctx, upsertRequestUsageSQL,
		u.RequestID, u.MerchantID, u.AccountID, u.APIKeyID, u.CreatedAt, nullableJSON(u.Usage))
	if err != nil {
		return fmt.Errorf("store: 写 request_log 用量失败: %w", err)
	}
	return nil
}

// 用量写入只更新 usage：其余列由终态写入负责，先到的写入不越界。
const upsertRequestUsageSQL = "INSERT INTO request_log (request_id, merchant_id, account_id, api_key_id, created_at, `usage`)" +
	` VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE ` + "`usage`" + ` = VALUES(` + "`usage`" + `)`

// RequestOutcome 是请求记录里由终态写入的那部分。
type RequestOutcome struct {
	RequestID  string
	MerchantID uint64
	AccountID  uint64
	APIKeyID   uint64
	CreatedAt  time.Time
	// Status 是终态：success | failed | cancelled。
	Status string
	// HTTPStatus 是返回给客户端的 HTTP 状态码。
	HTTPStatus int
	// UpstreamStatus 是最后一次上游尝试的状态码；0 表示未取得。
	UpstreamStatus int
	FailureClass   string
	ErrorCode      string
	DurationMS     int64
	// RequestedModel / UpstreamModel 是客户端请求的模型名与实际发往上游的模型名。
	RequestedModel   string
	UpstreamModel    string
	Protocol         string
	UpstreamProtocol string
	CrossProtocol    bool
	Stream           bool
	WrittenBytes     int64
	// ClientIP / UserAgent 是排障线索，可被客户端影响，不参与任何判定。
	ClientIP       string
	UserAgent      string
	RewrittenParts json.RawMessage
}

// UpsertRequestOutcome 写入请求记录的终态字段。
func (s *Store) UpsertRequestOutcome(ctx context.Context, o RequestOutcome) error {
	if strings.TrimSpace(o.RequestID) == "" {
		return errors.New("store: request_log.request_id 不能为空")
	}
	if o.AccountID == 0 {
		return errors.New("store: request_log.account_id 不能为 0")
	}
	if !validRequestStatus(o.Status) {
		return fmt.Errorf("store: request_log.status 取值非法: %q", o.Status)
	}
	if o.CreatedAt.IsZero() {
		return errors.New("store: request_log.created_at 不能为零值")
	}
	_, err := s.db.ExecContext(ctx, upsertRequestOutcomeSQL,
		o.RequestID, o.MerchantID, o.AccountID, o.APIKeyID, o.CreatedAt, o.Status, o.HTTPStatus,
		nullableInt(o.UpstreamStatus), nullableString(o.FailureClass), nullableString(o.ErrorCode),
		o.DurationMS, nullableString(o.RequestedModel), nullableString(o.UpstreamModel),
		nullableString(o.Protocol), nullableString(o.UpstreamProtocol), o.CrossProtocol, o.Stream,
		o.WrittenBytes, o.ClientIP, o.UserAgent, nullableJSON(o.RewrittenParts))
	if err != nil {
		return fmt.Errorf("store: 写 request_log 终态失败: %w", err)
	}
	return nil
}

const upsertRequestOutcomeSQL = `INSERT INTO request_log (request_id, merchant_id, account_id, api_key_id,
created_at, status, http_status, upstream_status, failure_class, error_code, duration_ms,
requested_model, upstream_model, protocol, upstream_protocol, cross_protocol, stream, written_bytes,
client_ip, user_agent, rewritten_parts)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE merchant_id = VALUES(merchant_id), account_id = VALUES(account_id),
api_key_id = VALUES(api_key_id), status = VALUES(status), http_status = VALUES(http_status),
upstream_status = VALUES(upstream_status), failure_class = VALUES(failure_class),
error_code = VALUES(error_code), duration_ms = VALUES(duration_ms),
requested_model = VALUES(requested_model), upstream_model = VALUES(upstream_model),
protocol = VALUES(protocol), upstream_protocol = VALUES(upstream_protocol),
cross_protocol = VALUES(cross_protocol), stream = VALUES(stream),
written_bytes = VALUES(written_bytes), client_ip = VALUES(client_ip),
user_agent = VALUES(user_agent), rewritten_parts = VALUES(rewritten_parts)`

// requestStatuses 是请求终态的取值集合，与契约的枚举一致。
var requestStatuses = map[string]struct{}{
	"success":   {},
	"failed":    {},
	"cancelled": {},
}

// validRequestStatus 报告终态取值是否在枚举内。
func validRequestStatus(status string) bool {
	_, ok := requestStatuses[status]
	return ok
}

// LastRequestAttempt 读一条请求的最后一次尝试。
//
// 用途是补齐终态写入不带的字段（上游模型、上游协议与失败分类都只出现在尝试记录里）。
// 没有尝试时返回 sql.ErrNoRows：请求可能在选路之前就失败了，那属于正常情形。
func (s *Store) LastRequestAttempt(ctx context.Context, requestID string) (*RequestAttempt, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, errors.New("store: request_attempt.request_id 不能为空")
	}
	row := s.db.QueryRowContext(ctx, lastRequestAttemptSQL, requestID)
	attempt, err := scanRequestAttempt(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("store: 查询 request_attempt 失败: %w", err)
	}
	return attempt, nil
}

const requestAttemptColumns = `request_id, attempt, outcome, upstream_status, failure_class, error_code,
client_protocol, upstream_protocol, cross_protocol, duration_ms, requested_model, upstream_model,
rewritten_parts, created_at`

const lastRequestAttemptSQL = `SELECT ` + requestAttemptColumns + `
FROM request_attempt WHERE request_id = ? ORDER BY attempt DESC LIMIT 1`

const requestAttemptsSQL = `SELECT ` + requestAttemptColumns + `
FROM request_attempt WHERE request_id = ? ORDER BY attempt`

// scanRequestAttempt 解析一行尝试记录；scan 由 QueryRow 或 Rows 提供。
func scanRequestAttempt(scan func(dest ...any) error) (*RequestAttempt, error) {
	var (
		a                RequestAttempt
		upstreamStatus   sql.NullInt64
		failureClass     sql.NullString
		errorCode        sql.NullString
		clientProtocol   sql.NullString
		upstreamProtocol sql.NullString
		requestedModel   sql.NullString
		upstreamModel    sql.NullString
		rewrittenParts   []byte
		createdAt        scanTime
	)
	if err := scan(&a.RequestID, &a.Attempt, &a.Outcome, &upstreamStatus, &failureClass, &errorCode,
		&clientProtocol, &upstreamProtocol, &a.CrossProtocol, &a.DurationMS, &requestedModel,
		&upstreamModel, &rewrittenParts, &createdAt); err != nil {
		return nil, err
	}
	a.UpstreamStatus = int(upstreamStatus.Int64)
	a.FailureClass = failureClass.String
	a.ErrorCode = errorCode.String
	a.ClientProtocol = clientProtocol.String
	a.UpstreamProtocol = upstreamProtocol.String
	a.RequestedModel = requestedModel.String
	a.UpstreamModel = upstreamModel.String
	a.RewrittenParts = json.RawMessage(rewrittenParts)
	a.CreatedAt = createdAt.Time
	return &a, nil
}

// RequestAttempts 按尝试序号读一条请求的全部尝试。
func (s *Store) RequestAttempts(ctx context.Context, requestID string) ([]RequestAttempt, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, errors.New("store: request_attempt.request_id 不能为空")
	}
	rows, err := s.db.QueryContext(ctx, requestAttemptsSQL, requestID)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 request_attempt 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var attempts []RequestAttempt
	for rows.Next() {
		attempt, err := scanRequestAttempt(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("store: 解析 request_attempt 行失败: %w", err)
		}
		attempts = append(attempts, *attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 request_attempt 行失败: %w", err)
	}
	return attempts, nil
}

// UpsertRequestStatsDaily 把一次请求的计数累加进按天缓存。
//
// 只累加、不回算：明细超期后本表是唯一依据，因此它必须随每次写入即时更新，
// 而不是定期从明细重算（重算会在明细归档后把历史区间算少）。
func (s *Store) UpsertRequestStatsDaily(ctx context.Context, accountID uint64, day time.Time,
	model string, apiKeyID uint64, status string) error {
	if accountID == 0 {
		return errors.New("store: request_stats_daily.account_id 不能为 0")
	}
	if day.IsZero() {
		return errors.New("store: request_stats_daily.day 不能为零值")
	}
	if !validRequestStatus(status) {
		return fmt.Errorf("store: request_stats_daily.status 取值非法: %q", status)
	}
	_, err := s.db.ExecContext(ctx, upsertRequestStatsSQL, accountID, day, model, apiKeyID, status)
	if err != nil {
		return fmt.Errorf("store: 累加 request_stats_daily 失败: %w", err)
	}
	return nil
}

const upsertRequestStatsSQL = "INSERT INTO request_stats_daily (account_id, `day`, model, api_key_id, status, total)" +
	" VALUES (?, ?, ?, ?, ?, 1) ON DUPLICATE KEY UPDATE total = total + 1"

// nullableInt 把 0 转成 SQL NULL：状态码与耗时的 0 表示「未取得」，
// 与「取得的是 0」是两种事实，写进同一列会让展示层无法区分。
func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// RequestLogFilter 是请求记录列表的过滤条件。
type RequestLogFilter struct {
	// AccountID 是记录的归属账户；AllAccounts 为假时必填，传 0 会被拒绝。
	AccountID uint64
	// AllAccounts 为真时跨账户读取，不带 account_id 条件。
	//
	// 用显式开关而不是「AccountID 为 0 即不限」：零值同时代表「没设置」，
	// 账户面漏传账户时会静默变成全平台查询。只有管理面清单置位本字段。
	AllAccounts bool
	// Since / Until 是请求时刻的闭区间；零值表示该侧不限。
	Since time.Time
	Until time.Time
	// RequestedModel 按客户端请求的模型名精确匹配；空串表示不过滤。
	RequestedModel string
	// APIKeyID 按签发本次调用的密钥过滤；0 表示不过滤。
	APIKeyID uint64
	// Status 按终态过滤；空串表示不过滤。
	Status string
	// RequestID 按请求标识精确匹配，用于定位单条记录；空串表示不过滤。
	RequestID string
	// Limit / Offset 是偏移分页参数；Limit 必须为正。
	Limit  int
	Offset int
}

// RequestLogRow 是 request_log 的一行。
type RequestLogRow struct {
	ID        uint64
	RequestID string
	// AccountID 是记录的归属账户；跨账户清单据此分行。
	AccountID uint64
	// APIKeyID 是签发本次调用的密钥，供页面按密钥过滤与展示。
	APIKeyID   uint64
	CreatedAt  time.Time
	Status     string
	HTTPStatus int
	// UpstreamStatus 是最后一次上游尝试的状态码；0 表示未取得。
	UpstreamStatus int
	FailureClass   string
	ErrorCode      string
	DurationMS     int64
	// RequestedModel 是客户端请求的模型名，UpstreamModel 是实际发往上游的模型名。
	RequestedModel   string
	UpstreamModel    string
	Protocol         string
	UpstreamProtocol string
	CrossProtocol    bool
	Stream           bool
	WrittenBytes     int64
	ClientIP         string
	UserAgent        string
	// Usage 是取得的 token 用量，原始 JSON；未取得时为 nil。
	Usage json.RawMessage
	// PayloadAvailable 报告本次是否存在可取的脱敏报文。
	PayloadAvailable bool
	// RequestShape / UpstreamRequestShape / ErrorResponseShape 是脱敏后的报文结构；
	// 无报文时为 nil。三者在列表查询里不读取（列表不展示报文）。
	RequestShape         json.RawMessage
	UpstreamRequestShape json.RawMessage
	ErrorResponseShape   json.RawMessage
	// RewrittenParts 是网关对报文做过的改写标注，原始 JSON 数组。
	RewrittenParts json.RawMessage
}

// payloadWindowDays 是脱敏报文的保留窗口。
//
// 窗口在读取侧生效，而不是靠定时任务改写存量行：清理任务跑之前与跑之后，「超期」
// 会给出两种答案，而展示口径不该取决于清理任务的节奏。
const payloadWindowDays = 7

// 报文一律走这两个表达式：保留窗口与可用标志同源，列表与详情不会一个说可用、
// 另一个给空报文。窗口长度用数据库时钟计算，进程时钟与数据库时钟不一致时以数据侧为准。
var (
	payloadWindowCond    = "created_at >= DATE_SUB(NOW(), INTERVAL " + strconv.Itoa(payloadWindowDays) + " DAY)"
	payloadAvailableExpr = "(payload_available AND " + payloadWindowCond + ") AS payload_available"
)

// 列表不读报文列：报文只在详情端点展示，列表带上它们会把每页响应放大到报文体量。
var requestLogListColumns = `id, request_id, account_id, api_key_id, created_at, status, http_status, upstream_status,
failure_class, error_code, duration_ms, requested_model, upstream_model, protocol, upstream_protocol,
cross_protocol, stream, written_bytes, client_ip, user_agent, ` + "`usage`" + `, ` +
	payloadAvailableExpr + `, rewritten_parts`

var requestLogDetailColumns = requestLogListColumns +
	`, IF(` + payloadWindowCond + `, request_shape, NULL) AS request_shape` +
	`, IF(` + payloadWindowCond + `, upstream_request_shape, NULL) AS upstream_request_shape` +
	`, IF(` + payloadWindowCond + `, error_response_shape, NULL) AS error_response_shape`

// statusNotNullCondition 是「请求已进入终态」的谓词：归属与用量可能先于终态落库，
// 没有终态的行表示这次请求中断在写终态之前，既不可展示也不该计入聚合。
const statusNotNullCondition = "status IS NOT NULL"

// requestLogWhere 组装请求记录列表的 WHERE 子句与参数。
//
// status IS NOT NULL 固定带上：归属与用量可能先于终态落库，没有终态的行表示这次请求
// 中断在写终态之前，不是一条可展示的事实，也不能计入聚合。
func requestLogWhere(f RequestLogFilter) (string, []any) {
	conditions := []string{statusNotNullCondition}
	var args []any
	// 全平台清单不带账户条件；其余调用方一律带账户，缺账户在 ListRequestLogs
	// 入口就被拒绝，不会走到这里退化成全平台查询。
	if !f.AllAccounts {
		conditions = append(conditions, accountIDCondition)
		args = append(args, f.AccountID)
	}
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
	if f.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, f.Status)
	}
	if f.RequestID != "" {
		conditions = append(conditions, "request_id = ?")
		args = append(args, f.RequestID)
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

// ListRequestLogs 按过滤条件分页列出请求记录，返回当页行与满足条件的总数。
//
// 排序按主键倒序：同一秒内的多条请求也有确定顺序，翻页不会重复或漏行。
func (s *Store) ListRequestLogs(ctx context.Context, f RequestLogFilter) ([]RequestLogRow, int, error) {
	if !f.AllAccounts && f.AccountID == 0 {
		return nil, 0, errors.New("store: request_log.account_id 不能为 0")
	}
	if f.Limit <= 0 {
		return nil, 0, errors.New("store: 分页条数必须为正")
	}
	if f.Offset < 0 {
		return nil, 0, errors.New("store: 分页偏移不能为负")
	}
	if f.Status != "" && !validRequestStatus(f.Status) {
		return nil, 0, fmt.Errorf("store: request_log.status 取值非法: %q", f.Status)
	}
	where, args := requestLogWhere(f)

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM request_log"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: 统计 request_log 失败: %w", err)
	}
	//nolint:gosec // G202：拼进去的是列清单与由常量组成的谓词，取值一律走问号占位符。
	query := "SELECT " + requestLogListColumns + " FROM request_log" + where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	rows, err := s.db.QueryContext(ctx, query, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: 查询 request_log 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []RequestLogRow
	for rows.Next() {
		row, err := scanRequestLogRow(rows.Scan, false)
		if err != nil {
			return nil, 0, fmt.Errorf("store: 解析 request_log 行失败: %w", err)
		}
		records = append(records, *row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: 遍历 request_log 行失败: %w", err)
	}
	return records, total, nil
}

// RequestLogByRequestID 按请求标识读一条属于某账户的请求记录。
//
// 不带账户的查询会让 request_id 成为探测他人记录的入口，因此 account_id 是必填条件；
// 不匹配返回 sql.ErrNoRows，由调用方折成「不存在」。
func (s *Store) RequestLogByRequestID(ctx context.Context, accountID uint64, requestID string) (*RequestLogRow, error) {
	if accountID == 0 {
		return nil, errors.New("store: request_log.account_id 不能为 0")
	}
	if strings.TrimSpace(requestID) == "" {
		return nil, errors.New("store: request_log.request_id 不能为空")
	}
	//nolint:gosec // G202：拼进去的是列清单与由常量组成的谓词，取值一律走问号占位符。
	query := "SELECT " + requestLogDetailColumns +
		" FROM request_log WHERE account_id = ? AND request_id = ? AND status IS NOT NULL"
	row := s.db.QueryRowContext(ctx, query, accountID, requestID)
	record, err := scanRequestLogRow(row.Scan, true)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("store: 查询 request_log 失败: %w", err)
	}
	return record, nil
}

// RequestLogByRequestIDUnscoped 按请求标识读任意账户的一条请求记录。
//
// 管理面排障要定位不属于自己的请求，因此不带账户条件；账户面走 RequestLogByRequestID，
// 那里的 account_id 是必填条件，request_id 不构成越权入口。
func (s *Store) RequestLogByRequestIDUnscoped(ctx context.Context, requestID string) (*RequestLogRow, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, errors.New("store: request_log.request_id 不能为空")
	}
	//nolint:gosec // G202：拼进去的是列清单与由常量组成的谓词，取值一律走问号占位符。
	query := "SELECT " + requestLogDetailColumns +
		" FROM request_log WHERE request_id = ? AND status IS NOT NULL"
	row := s.db.QueryRowContext(ctx, query, requestID)
	record, err := scanRequestLogRow(row.Scan, true)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("store: 查询 request_log 失败: %w", err)
	}
	return record, nil
}

// scanRequestLogRow 解析一行请求记录；withPayload 为真时读报文三列。
func scanRequestLogRow(scan func(dest ...any) error, withPayload bool) (*RequestLogRow, error) {
	var (
		r                RequestLogRow
		httpStatus       sql.NullInt64
		upstreamStatus   sql.NullInt64
		failureClass     sql.NullString
		errorCode        sql.NullString
		durationMS       sql.NullInt64
		requestedModel   sql.NullString
		upstreamModel    sql.NullString
		protocol         sql.NullString
		upstreamProtocol sql.NullString
		writtenBytes     sql.NullInt64
		usage            []byte
		rewrittenParts   []byte
		createdAt        scanTime
	)
	// 报文三列先扫进 []byte 再转 json.RawMessage：JSON 列可为 NULL，
	// 而 json.RawMessage 不接受 NULL（与 usage / rewritten_parts 同一处理）。
	var requestShape, upstreamRequestShape, errorResponseShape []byte
	dest := []any{&r.ID, &r.RequestID, &r.AccountID, &r.APIKeyID, &createdAt, &r.Status, &httpStatus, &upstreamStatus,
		&failureClass, &errorCode, &durationMS, &requestedModel, &upstreamModel, &protocol,
		&upstreamProtocol, &r.CrossProtocol, &r.Stream, &writtenBytes, &r.ClientIP, &r.UserAgent,
		&usage, &r.PayloadAvailable, &rewrittenParts}
	if withPayload {
		dest = append(dest, &requestShape, &upstreamRequestShape, &errorResponseShape)
	}
	if err := scan(dest...); err != nil {
		return nil, err
	}
	r.CreatedAt = createdAt.Time
	r.RequestShape = json.RawMessage(requestShape)
	r.UpstreamRequestShape = json.RawMessage(upstreamRequestShape)
	r.ErrorResponseShape = json.RawMessage(errorResponseShape)
	r.HTTPStatus = int(httpStatus.Int64)
	r.UpstreamStatus = int(upstreamStatus.Int64)
	r.FailureClass = failureClass.String
	r.ErrorCode = errorCode.String
	r.DurationMS = durationMS.Int64
	r.RequestedModel = requestedModel.String
	r.UpstreamModel = upstreamModel.String
	r.Protocol = protocol.String
	r.UpstreamProtocol = upstreamProtocol.String
	r.WrittenBytes = writtenBytes.Int64
	r.Usage = json.RawMessage(usage)
	r.RewrittenParts = json.RawMessage(rewrittenParts)
	return &r, nil
}

// RequestStatsQuery 是按维度聚合请求计数的条件。
type RequestStatsQuery struct {
	// AccountID 是聚合的归属账户；AllAccounts 为假时必填，传 0 会被拒绝。
	AccountID uint64
	// AllAccounts 为真时跨账户聚合，不带 account_id 条件；只由管理面置位。
	AllAccounts bool
	// Since / Until 是请求时刻的闭区间，按天取整；零值表示该侧不限。
	Since time.Time
	Until time.Time
	// RequestedModel 按客户端请求的模型名精确匹配；空串表示不过滤。
	RequestedModel string
	// APIKeyID 按签发本次调用的密钥过滤；0 表示不过滤。
	APIKeyID uint64
	// GroupBy 是分组维度：day | model | status。
	GroupBy string
}

// RequestStatsItem 是一个分组的计数。
type RequestStatsItem struct {
	// Key 是分组值：日期、模型名或终态。
	Key       string
	Total     int64
	Success   int64
	Failed    int64
	Cancelled int64
}

// groupByAccount 是请求记录聚合的账户维度：管理面按账户看全平台分布。
const groupByAccount = "account"

// requestStatsKeys 是聚合维度的取值集合，也是「分组表达式」的唯一出处。
//
// 表达式来自本表而不是调用方拼串：分组维度是列映射，让调用方拼 SQL 片段等于
// 把注入面开在查询条件上。
var requestStatsKeys = map[string]string{
	groupByDay:     "DATE_FORMAT(`day`, '%Y-%m-%d')",
	groupByModel:   "model",
	groupByStatus:  "status",
	groupByAccount: "CAST(account_id AS CHAR)",
}

// RequestStats 按维度聚合账户的请求计数，按 key 升序返回。
func (s *Store) RequestStats(ctx context.Context, q RequestStatsQuery) ([]RequestStatsItem, error) {
	if !q.AllAccounts && q.AccountID == 0 {
		return nil, errors.New("store: request_stats_daily.account_id 不能为 0")
	}
	keyExpr, ok := requestStatsKeys[q.GroupBy]
	if !ok {
		return nil, fmt.Errorf("store: 未知的聚合维度 %q", q.GroupBy)
	}
	conditions := []string{}
	var args []any
	// 全平台聚合不带账户条件，与 ListRequestLogs 同一口径。
	if !q.AllAccounts {
		conditions = append(conditions, accountIDCondition)
		args = append(args, q.AccountID)
	}
	if !q.Since.IsZero() {
		conditions = append(conditions, "`day` >= DATE(?)")
		args = append(args, q.Since)
	}
	if !q.Until.IsZero() {
		conditions = append(conditions, "`day` <= DATE(?)")
		args = append(args, q.Until)
	}
	if q.RequestedModel != "" {
		conditions = append(conditions, "model = ?")
		args = append(args, q.RequestedModel)
	}
	if q.APIKeyID != 0 {
		conditions = append(conditions, "api_key_id = ?")
		args = append(args, q.APIKeyID)
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	//nolint:gosec // G202：拼进去的是映射到白名单表达式的分组列与常量谓词，取值一律走问号占位符。
	// 分项计数按状态条件求和而不是数行数：同一 (日期, 模型, 密钥) 在每个状态下各占一行，
	// 行数恒为「有几种状态」，只有按 total 条件求和才回答「成功了多少次」。
	query := "SELECT " + keyExpr + ` AS k, SUM(total),
SUM(IF(status = 'success', total, 0)), SUM(IF(status = 'failed', total, 0)),
SUM(IF(status = 'cancelled', total, 0))
FROM request_stats_daily` + where + " GROUP BY k ORDER BY k"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 request_stats_daily 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []RequestStatsItem
	for rows.Next() {
		var item RequestStatsItem
		if err := rows.Scan(&item.Key, &item.Total, &item.Success, &item.Failed, &item.Cancelled); err != nil {
			return nil, fmt.Errorf("store: 解析 request_stats_daily 行失败: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 request_stats_daily 行失败: %w", err)
	}
	return items, nil
}
