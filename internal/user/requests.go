package user

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件实现账户面的请求记录列表与详情。
//
// 记录只覆盖摘要层与归属层字段：渠道标识、上游标识、商家标识与上游错误详情不对用户
// 暴露，它们是商家面与管理面的统计口径。作用域由会话推导，端点不接受账户参数。

const (
	// statusQueryParam 是按终态过滤的参数。
	statusQueryParam = "status"
	// requestIDQueryParam 是按请求标识精确匹配的参数。
	requestIDQueryParam = "request_id"
	// requestStatsSuffix 是聚合端点的路径尾段：/api/v1/user/requests/stats。
	requestStatsSuffix = "/stats"
)

// requestStatuses 是终态过滤的取值集合，与契约的枚举一致。
var requestStatuses = map[string]struct{}{
	"success":   {},
	"failed":    {},
	"cancelled": {},
}

// errInvalidRequestStatus 是终态取值非法时的错误，文案直接写回信封。
var errInvalidRequestStatus = errors.New("status 取值必须是 success / failed / cancelled")

// usageTokensView 与契约 UsageTokens 对齐的用量分量在 usage.go 里声明。

// attemptView 是一次上游尝试，字段与契约 RequestAttempt 对齐。
type attemptView struct {
	Attempt        int     `json:"attempt"`
	Outcome        string  `json:"outcome"`
	UpstreamStatus *int    `json:"upstream_status"`
	FailureClass   *string `json:"failure_class"`
	ErrorCode      *string `json:"error_code"`
	CrossProtocol  bool    `json:"cross_protocol"`
	DurationMS     int64   `json:"duration_ms"`
}

// requestItemView 是一条请求记录的摘要与归属层字段，与契约 RequestItem 对齐。
type requestItemView struct {
	RequestID        string           `json:"request_id"`
	CreatedAt        time.Time        `json:"created_at"`
	Status           string           `json:"status"`
	HTTPStatus       int              `json:"http_status"`
	UpstreamStatus   *int             `json:"upstream_status"`
	FailureClass     *string          `json:"failure_class"`
	ErrorCode        *string          `json:"error_code"`
	DurationMS       int64            `json:"duration_ms"`
	Model            string           `json:"model"`
	UpstreamModel    string           `json:"upstream_model"`
	Protocol         string           `json:"protocol"`
	UpstreamProtocol string           `json:"upstream_protocol"`
	CrossProtocol    bool             `json:"cross_protocol"`
	APIKeyID         uint64           `json:"api_key_id"`
	Usage            *usageTokensView `json:"usage"`
	PayloadAvailable bool             `json:"payload_available"`
	Stream           bool             `json:"stream"`
	WrittenBytes     int64            `json:"written_bytes"`
}

// requestDetailView 是一条请求记录的完整视图，与契约 RequestDetail 对齐。
type requestDetailView struct {
	Request              requestItemView `json:"request"`
	Attempts             []attemptView   `json:"attempts"`
	RequestShape         json.RawMessage `json:"request_shape"`
	UpstreamRequestShape json.RawMessage `json:"upstream_request_shape"`
	ErrorResponseShape   json.RawMessage `json:"error_response_shape"`
	RewrittenParts       []string        `json:"rewritten_parts"`
	ClientIP             string          `json:"client_ip"`
	UserAgent            string          `json:"user_agent"`
}

// handleRequests 按偏移分页返回当前账户的请求记录。
func (h *Handler) handleRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	account, ok := h.currentAccount(w, r)
	if !ok {
		return
	}
	page, size, err := parsePaging(r.URL.Query())
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	filter, err := parseRequestFilter(r.URL.Query(), account.ID, size, (page-1)*size)
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	rows, total, err := h.store.ListRequestLogs(r.Context(), filter)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "请求记录查询失败")
		return
	}
	items := make([]requestItemView, 0, len(rows))
	for i := range rows {
		item, err := newRequestItemView(&rows[i])
		if err != nil {
			// 用量 JSON 损坏说明这一行已不可读；按 500 处理而不是跳过该行：
			// 静默少一行会让用户以为那次调用没有发生。
			webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "请求记录解析失败")
			return
		}
		items = append(items, item)
	}
	webapi.WritePage(w, map[string]any{itemsKey: items}, page, size, total)
}

// handleRequestDetail 返回一条属于当前账户的请求记录。
func (h *Handler) handleRequestDetail(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	account, ok := h.currentAccount(w, r)
	if !ok {
		return
	}
	row, err := h.store.RequestLogByRequestID(r.Context(), account.ID, requestID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 不属于当前账户的记录与不存在的记录回同一个码：以 403 区分会让
			// request_id 成为探测他人请求的入口。
			webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "请求记录不存在")
			return
		}
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "请求记录查询失败")
		return
	}
	attemptRows, err := h.store.RequestAttempts(r.Context(), requestID)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "请求记录查询失败")
		return
	}
	item, err := newRequestItemView(row)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "请求记录解析失败")
		return
	}
	attempts := make([]attemptView, 0, len(attemptRows))
	for i := range attemptRows {
		attempts = append(attempts, newAttemptView(&attemptRows[i]))
	}
	webapi.WriteOK(w, requestDetailView{
		Request:              item,
		Attempts:             attempts,
		RequestShape:         nullablePayload(row.RequestShape),
		UpstreamRequestShape: nullablePayload(row.UpstreamRequestShape),
		ErrorResponseShape:   nullablePayload(row.ErrorResponseShape),
		RewrittenParts:       rewriteParts(row.RewrittenParts),
		ClientIP:             row.ClientIP,
		UserAgent:            row.UserAgent,
	})
}

// parseRequestFilter 解析请求记录列表的过滤条件；账户与分页由调用方给定。
func parseRequestFilter(query url.Values, accountID uint64, limit, offset int) (store.RequestLogFilter, error) {
	filter := store.RequestLogFilter{AccountID: accountID, Limit: limit, Offset: offset}
	since, err := parseMoment(query.Get(sinceQueryParam))
	if err != nil {
		return store.RequestLogFilter{}, err
	}
	until, err := parseMoment(query.Get(untilQueryParam))
	if err != nil {
		return store.RequestLogFilter{}, err
	}
	filter.Since, filter.Until = since, until
	filter.RequestedModel = strings.TrimSpace(query.Get(modelQueryParam))
	filter.RequestID = strings.TrimSpace(query.Get(requestIDQueryParam))
	status := strings.TrimSpace(query.Get(statusQueryParam))
	if status != "" {
		if _, ok := requestStatuses[status]; !ok {
			return store.RequestLogFilter{}, errInvalidRequestStatus
		}
		filter.Status = status
	}
	apiKeyID, err := parseAPIKeyID(query.Get(apiKeyQueryParam))
	if err != nil {
		return store.RequestLogFilter{}, err
	}
	filter.APIKeyID = apiKeyID
	return filter, nil
}

// parseAPIKeyID 解析 api_key_id 过滤参数；空串表示不过滤。
func parseAPIKeyID(raw string) (uint64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	keyID, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil || keyID == 0 {
		return 0, errors.New("api_key_id 必须是正整数")
	}
	return keyID, nil
}

// newRequestItemView 把存储行映射为对外形状。
//
// 未取得的取值映射为 null 而不是零值：状态码 0、空模型名与「没有上游」是两回事，
// 前端据 null 显示「未取得」，据 0 显示 0。
func newRequestItemView(row *store.RequestLogRow) (requestItemView, error) {
	usage, err := decodeRequestUsage(row.Usage)
	if err != nil {
		return requestItemView{}, err
	}
	return requestItemView{
		RequestID:        row.RequestID,
		CreatedAt:        row.CreatedAt,
		Status:           row.Status,
		HTTPStatus:       row.HTTPStatus,
		UpstreamStatus:   nullableIntPtr(row.UpstreamStatus),
		FailureClass:     nullableStringPtr(row.FailureClass),
		ErrorCode:        nullableStringPtr(row.ErrorCode),
		DurationMS:       row.DurationMS,
		Model:            row.RequestedModel,
		UpstreamModel:    row.UpstreamModel,
		Protocol:         row.Protocol,
		UpstreamProtocol: row.UpstreamProtocol,
		CrossProtocol:    row.CrossProtocol,
		APIKeyID:         row.APIKeyID,
		Usage:            usage,
		PayloadAvailable: row.PayloadAvailable,
		Stream:           row.Stream,
		WrittenBytes:     row.WrittenBytes,
	}, nil
}

// newAttemptView 把尝试记录映射为对外形状。
func newAttemptView(row *store.RequestAttempt) attemptView {
	return attemptView{
		Attempt:        row.Attempt,
		Outcome:        row.Outcome,
		UpstreamStatus: nullableIntPtr(row.UpstreamStatus),
		FailureClass:   nullableStringPtr(row.FailureClass),
		ErrorCode:      nullableStringPtr(row.ErrorCode),
		CrossProtocol:  row.CrossProtocol,
		DurationMS:     row.DurationMS,
	}
}

// decodeRequestUsage 把落库的用量 JSON 映射为对外的分量字段；未取得用量时返回 nil。
//
// 键名与 domain.Usage 的字段一致（落库即该结构），因此这里直接反序列化而不是
// 逐键取值：字段增减只改一处，读路径不会漏掉新增分量。
func decodeRequestUsage(raw json.RawMessage) (*usageTokensView, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var usage domain.Usage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil, err
	}
	return &usageTokensView{
		InputTokens:        usage.InputTokens,
		OutputTokens:       usage.OutputTokens,
		CacheReadTokens:    usage.CacheReadTokens,
		CacheWriteTokens:   usage.CacheWriteTokens,
		CacheWrite5mTokens: usage.CacheWrite5mTokens,
		CacheWrite1hTokens: usage.CacheWrite1hTokens,
		ReasoningTokens:    usage.ReasoningTokens,
		ServerToolUses:     usage.ServerToolUses,
	}, nil
}

// nullablePayload 把空报文列映射为 JSON null；有报文时原样透出脱敏结构。
func nullablePayload(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// rewriteParts 把改写标注 JSON 数组映射为字符串切片；无标注时回空数组。
//
// 回空数组而不是 null：契约的 rewritten_parts 是数组，未改写时为空数组，
// 前端据此判断「没有改写」而不必再分支一次 null。
func rewriteParts(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err != nil {
		return []string{}
	}
	return parts
}

// parseRequestIDPath 解析 /api/v1/user/requests/{request_id} 里的标识。
//
// 标识按不透明字符串处理：它由网关注入，取值形态不是本层的判据，只要求非空且不含
// 路径分隔符；空段与多段路径都不匹配，避免把 /requests/ 或 /requests/a/b 当成标识。
func parseRequestIDPath(path string) (string, bool) {
	rest, found := strings.CutPrefix(path, RequestsPath+"/")
	if !found || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

// nullableIntPtr 把 0 映射为 nil：状态码与耗时的 0 表示未取得，与「取得的是 0」不同。
func nullableIntPtr(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}

// nullableStringPtr 把空串映射为 nil：空串在库中表示「该事实不存在」。
func nullableStringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
