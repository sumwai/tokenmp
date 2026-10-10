package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是请求记录的管理面读路径。
//
// 与管理面其余清单不同，这些读法不带账户作用域：排障要定位任意账户的请求。作用域
// 收敛在入口（HTTPS 管理面要求 ops 能力，CLI 是本地运维工具），因此数据面允许
// accountID 为 0 表示不限账户。
//
// 对外形状（Request*View）在本包收敛：CLI 的 --json 与管理面清单共用同一份字段，
// 页面与命令行不会各算一套。

// requestStatuses 是请求终态的取值集合，与存储层和契约一致。
var requestStatuses = map[string]struct{}{
	"success":   {},
	"failed":    {},
	"cancelled": {},
}

// requestStatsGroups 是聚合分组维度的取值集合。
var requestStatsGroups = map[string]struct{}{
	"day":     {},
	"model":   {},
	"status":  {},
	"account": {},
}

// RequestLogReader 是请求记录读路径依赖的数据面。
//
// 与 Store 分开声明：只读清单的测试替身不必实现整套管理面动作；真实存储层满足它，
// 由 Service 在运行时断言取用，与网关装配处的能力断言同一口径。
type RequestLogReader interface {
	ListRequestLogs(ctx context.Context, f store.RequestLogFilter) ([]store.RequestLogRow, int, error)
	RequestLogByRequestIDUnscoped(ctx context.Context, requestID string) (*store.RequestLogRow, error)
	RequestAttempts(ctx context.Context, requestID string) ([]store.RequestAttempt, error)
	RequestStats(ctx context.Context, q store.RequestStatsQuery) ([]store.RequestStatsItem, error)
}

// requestLogReader 取回请求记录读路径的数据面。
func (s *Service) requestLogReader() (RequestLogReader, error) {
	reader, ok := s.store.(RequestLogReader)
	if !ok {
		return nil, errors.New("admin: 存储层未实现请求记录读取")
	}
	return reader, nil
}

// RequestView 是请求记录列表的一行，字段与契约的 AdminRequestItem 和
// `tokenmp admin requests list --json` 的行一致。
type RequestView struct {
	ID        uint64    `json:"id"`
	RequestID string    `json:"request_id"`
	AccountID uint64    `json:"account_id"`
	APIKeyID  uint64    `json:"api_key_id"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"`
	// HTTPStatus 是返回给客户端的 HTTP 状态码。
	HTTPStatus int `json:"http_status"`
	// UpstreamStatus 是最后一次上游尝试的状态码；未取得时为 null。
	UpstreamStatus *int    `json:"upstream_status"`
	FailureClass   *string `json:"failure_class"`
	ErrorCode      *string `json:"error_code"`
	DurationMS     int64   `json:"duration_ms"`
	// RequestedModel 是客户端请求的模型名，UpstreamModel 是实际发往上游的模型名。
	RequestedModel   string          `json:"model"`
	UpstreamModel    string          `json:"upstream_model"`
	Protocol         string          `json:"protocol"`
	UpstreamProtocol string          `json:"upstream_protocol"`
	CrossProtocol    bool            `json:"cross_protocol"`
	Stream           bool            `json:"stream"`
	WrittenBytes     int64           `json:"written_bytes"`
	Usage            json.RawMessage `json:"usage"`
	PayloadAvailable bool            `json:"payload_available"`
}

// RequestAttemptView 是一次上游尝试，字段与契约的 RequestAttempt 一致。
type RequestAttemptView struct {
	Attempt        int     `json:"attempt"`
	Outcome        string  `json:"outcome"`
	UpstreamStatus *int    `json:"upstream_status"`
	FailureClass   *string `json:"failure_class"`
	ErrorCode      *string `json:"error_code"`
	CrossProtocol  bool    `json:"cross_protocol"`
	DurationMS     int64   `json:"duration_ms"`
}

// RequestDetailView 是一条请求记录的完整视图。
type RequestDetailView struct {
	Request              RequestView          `json:"request"`
	Attempts             []RequestAttemptView `json:"attempts"`
	RequestShape         json.RawMessage      `json:"request_shape"`
	UpstreamRequestShape json.RawMessage      `json:"upstream_request_shape"`
	ErrorResponseShape   json.RawMessage      `json:"error_response_shape"`
	RewrittenParts       []string             `json:"rewritten_parts"`
	ClientIP             string               `json:"client_ip"`
	UserAgent            string               `json:"user_agent"`
}

// RequestStatsView 是一个分组的请求计数，字段与契约的 RequestStatsItem 一致。
type RequestStatsView struct {
	Key       string `json:"key"`
	Total     int64  `json:"total"`
	Success   int64  `json:"success"`
	Failed    int64  `json:"failed"`
	Cancelled int64  `json:"cancelled"`
}

// RequestListQuery 是请求记录列表的过滤与分页条件。
//
// 各过滤项与存储层同名同义：AccountID 为 0 表示不限账户；时间区间为闭区间；
// 空串与 0 表示不过滤。
type RequestListQuery struct {
	AccountID      uint64
	Since          time.Time
	Until          time.Time
	RequestedModel string
	RequestID      string
	Status         string
	APIKeyID       uint64
	Limit          int
	Offset         int
}

// ListRequests 分页列出请求记录，账户为 0 表示跨账户。
//
// 分页与终态在业务层再校验一次：服务层可被其它调用方直接使用，不能假设调用方
// 一定来自 CLI。
func (s *Service) ListRequests(ctx context.Context, q RequestListQuery) ([]RequestView, int, error) {
	if q.Limit <= 0 {
		return nil, 0, invalidf("admin: 分页条数必须为正")
	}
	if q.Offset < 0 {
		return nil, 0, invalidf("admin: 分页偏移不能为负")
	}
	if q.Status != "" {
		if _, ok := requestStatuses[q.Status]; !ok {
			return nil, 0, invalidf("admin: 终态取值必须是 success / failed / cancelled")
		}
	}
	reader, err := s.requestLogReader()
	if err != nil {
		return nil, 0, err
	}
	records, total, err := reader.ListRequestLogs(ctx, store.RequestLogFilter{
		AccountID:      q.AccountID,
		AllAccounts:    q.AccountID == 0,
		Since:          q.Since,
		Until:          q.Until,
		RequestedModel: q.RequestedModel,
		RequestID:      q.RequestID,
		Status:         q.Status,
		APIKeyID:       q.APIKeyID,
		Limit:          q.Limit,
		Offset:         q.Offset,
	})
	if err != nil {
		return nil, 0, err
	}
	views := make([]RequestView, 0, len(records))
	for i := range records {
		views = append(views, newRequestView(&records[i]))
	}
	return views, total, nil
}

// RequestDetail 读一条请求记录及其尝试时间线，不带账户作用域。
//
// 不存在与不属于任何账户都是同一件事：管理面看的是全平台，没有越权面可分。
func (s *Service) RequestDetail(ctx context.Context, requestID string) (*RequestDetailView, error) {
	if err := requireString("请求标识", requestID); err != nil {
		return nil, err
	}
	reader, err := s.requestLogReader()
	if err != nil {
		return nil, err
	}
	row, err := reader.RequestLogByRequestIDUnscoped(ctx, requestID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundf("请求记录不存在")
		}
		return nil, err
	}
	attempts, err := reader.RequestAttempts(ctx, requestID)
	if err != nil {
		return nil, err
	}
	attemptViews := make([]RequestAttemptView, 0, len(attempts))
	for i := range attempts {
		attemptViews = append(attemptViews, newRequestAttemptView(&attempts[i]))
	}
	return &RequestDetailView{
		Request:              newRequestView(row),
		Attempts:             attemptViews,
		RequestShape:         row.RequestShape,
		UpstreamRequestShape: row.UpstreamRequestShape,
		ErrorResponseShape:   row.ErrorResponseShape,
		RewrittenParts:       requestRewrittenParts(row.RewrittenParts),
		ClientIP:             row.ClientIP,
		UserAgent:            row.UserAgent,
	}, nil
}

// RequestStatsQuery 是请求计数聚合的过滤与分组条件。
//
// GroupBy 缺省为 day；account 维度只对管理面有意义（账户面恒为单账户）。
type RequestStatsQuery struct {
	AccountID      uint64
	Since          time.Time
	Until          time.Time
	RequestedModel string
	APIKeyID       uint64
	GroupBy        string
}

// RequestStats 按维度聚合请求计数，账户为 0 表示跨账户。
func (s *Service) RequestStats(ctx context.Context, q RequestStatsQuery) ([]RequestStatsView, error) {
	if q.GroupBy == "" {
		q.GroupBy = "day"
	}
	if _, ok := requestStatsGroups[q.GroupBy]; !ok {
		return nil, invalidf("admin: 分组维度必须是 day / model / status / account")
	}
	reader, err := s.requestLogReader()
	if err != nil {
		return nil, err
	}
	items, err := reader.RequestStats(ctx, store.RequestStatsQuery{
		AccountID:      q.AccountID,
		AllAccounts:    q.AccountID == 0,
		Since:          q.Since,
		Until:          q.Until,
		RequestedModel: q.RequestedModel,
		APIKeyID:       q.APIKeyID,
		GroupBy:        q.GroupBy,
	})
	if err != nil {
		return nil, err
	}
	views := make([]RequestStatsView, 0, len(items))
	for _, item := range items {
		views = append(views, RequestStatsView{
			Key:       item.Key,
			Total:     item.Total,
			Success:   item.Success,
			Failed:    item.Failed,
			Cancelled: item.Cancelled,
		})
	}
	return views, nil
}

// newRequestView 把存储行映射为对外形状。
//
// 未取得的取值映射为 null 而不是零值：状态码 0、空模型名与「没有上游」是两回事。
func newRequestView(row *store.RequestLogRow) RequestView {
	return RequestView{
		ID:               row.ID,
		RequestID:        row.RequestID,
		AccountID:        row.AccountID,
		APIKeyID:         row.APIKeyID,
		CreatedAt:        row.CreatedAt,
		Status:           row.Status,
		HTTPStatus:       row.HTTPStatus,
		UpstreamStatus:   requestNullableInt(row.UpstreamStatus),
		FailureClass:     requestNullableString(row.FailureClass),
		ErrorCode:        requestNullableString(row.ErrorCode),
		DurationMS:       row.DurationMS,
		RequestedModel:   row.RequestedModel,
		UpstreamModel:    row.UpstreamModel,
		Protocol:         row.Protocol,
		UpstreamProtocol: row.UpstreamProtocol,
		CrossProtocol:    row.CrossProtocol,
		Stream:           row.Stream,
		WrittenBytes:     row.WrittenBytes,
		Usage:            row.Usage,
		PayloadAvailable: row.PayloadAvailable,
	}
}

// newRequestAttemptView 把尝试记录映射为对外形状。
func newRequestAttemptView(row *store.RequestAttempt) RequestAttemptView {
	return RequestAttemptView{
		Attempt:        row.Attempt,
		Outcome:        row.Outcome,
		UpstreamStatus: requestNullableInt(row.UpstreamStatus),
		FailureClass:   requestNullableString(row.FailureClass),
		ErrorCode:      requestNullableString(row.ErrorCode),
		CrossProtocol:  row.CrossProtocol,
		DurationMS:     row.DurationMS,
	}
}

// requestRewrittenParts 把改写标注 JSON 数组映射为字符串切片；无标注时回空数组。
//
// 回空数组而不是 null：契约的 rewritten_parts 是数组，未改写时为空数组。
func requestRewrittenParts(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err != nil {
		return []string{}
	}
	return parts
}

// requestNullableInt 把 0 映射为 nil：状态码的 0 表示未取得。
func requestNullableInt(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}

// requestNullableString 把空串映射为 nil：空串在库中表示「该事实不存在」。
func requestNullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
