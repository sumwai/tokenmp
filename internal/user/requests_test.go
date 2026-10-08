package user

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖请求记录的列表、详情与聚合：作用域推导、过滤参数、行映射与错误分支。
// 数据访问用替身，全链不经数据库。

// requestRow 构造一条替身请求记录。
func requestRow() store.RequestLogRow {
	return store.RequestLogRow{
		ID:               7,
		RequestID:        "req-1",
		APIKeyID:         9,
		CreatedAt:        time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
		Status:           "failed",
		HTTPStatus:       429,
		UpstreamStatus:   429,
		FailureClass:     "rate_limit",
		ErrorCode:        "upstream_rate_limited",
		DurationMS:       120,
		RequestedModel:   "client-model",
		UpstreamModel:    "up-model",
		Protocol:         "openai_chat",
		UpstreamProtocol: "anthropic_messages",
		CrossProtocol:    true,
		Stream:           true,
		WrittenBytes:     2048,
		ClientIP:         "203.0.113.7",
		UserAgent:        "client/1.0",
		Usage: json.RawMessage(`{"source":"upstream","input_tokens":10,"output_tokens":4,` +
			`"cache_read_tokens":3,"cache_write_5m_tokens":2,"reasoning_tokens":1,"server_tool_uses":1}`),
		PayloadAvailable: true,
		RewrittenParts:   json.RawMessage(`["model","protocol"]`),
	}
}

// TestRequestsListMapping 断言对外形状：模型名两侧互换、未取得的取值映射为 null。
func TestRequestsListMapping(t *testing.T) {
	e := newTestEnv()
	e.store.requestRows = []store.RequestLogRow{requestRow()}
	e.store.requestTotal = 1

	status, env := e.do(t, http.MethodGet, RequestsPath, "token", "?page=2&size=5")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.requestFilter.AccountID != 42 {
		t.Errorf("账户 = %d，期望会话推导出的 42", e.store.requestFilter.AccountID)
	}
	if e.store.requestFilter.Limit != 5 || e.store.requestFilter.Offset != 5 {
		t.Errorf("分页 = limit %d offset %d，期望 5 / 5",
			e.store.requestFilter.Limit, e.store.requestFilter.Offset)
	}
	var data struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	if len(data.Items) != 1 {
		t.Fatalf("items 条数 = %d，期望 1", len(data.Items))
	}
	item := data.Items[0]
	want := map[string]string{
		"request_id":        `"req-1"`,
		"status":            `"failed"`,
		"http_status":       `429`,
		"upstream_status":   `429`,
		"failure_class":     `"rate_limit"`,
		"error_code":        `"upstream_rate_limited"`,
		"duration_ms":       `120`,
		"model":             `"client-model"`,
		"upstream_model":    `"up-model"`,
		"protocol":          `"openai_chat"`,
		"upstream_protocol": `"anthropic_messages"`,
		"cross_protocol":    `true`,
		"api_key_id":        `9`,
		"stream":            `true`,
		"written_bytes":     `2048`,
		"payload_available": `true`,
	}
	for key, expected := range want {
		if got := string(item[key]); got != expected {
			t.Errorf("%s = %s，期望 %s", key, got, expected)
		}
	}
	var usage usageTokensView
	if err := json.Unmarshal(item["usage"], &usage); err != nil {
		t.Fatalf("解析 usage: %v", err)
	}
	wantUsage := usageTokensView{
		InputTokens: 10, OutputTokens: 4, CacheReadTokens: 3,
		CacheWrite5mTokens: 2, ReasoningTokens: 1, ServerToolUses: 1,
	}
	if usage != wantUsage {
		t.Errorf("usage = %+v，期望 %+v", usage, wantUsage)
	}
}

// TestRequestsListNullMapping 断言未取得的取值序列化为 null 而不是零值：
// 前端据 null 显示「未取得」，据 0 显示 0，两者不能折叠。
func TestRequestsListNullMapping(t *testing.T) {
	e := newTestEnv()
	row := requestRow()
	row.Status = "success"
	row.HTTPStatus = 200
	row.UpstreamStatus = 0
	row.FailureClass = ""
	row.ErrorCode = ""
	row.Usage = nil
	row.PayloadAvailable = false
	e.store.requestRows = []store.RequestLogRow{row}
	e.store.requestTotal = 1

	status, env := e.do(t, http.MethodGet, RequestsPath, "token", "")
	if status != http.StatusOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	var data struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	item := data.Items[0]
	for _, key := range []string{"upstream_status", "failure_class", "error_code", "usage"} {
		if got := string(item[key]); got != "null" {
			t.Errorf("未取得的 %s = %s，期望 null", key, got)
		}
	}
	if got := string(item["http_status"]); got != "200" {
		t.Errorf("http_status = %s，期望 200", got)
	}
}

// TestRequestsListFilters 断言过滤参数按契约映射到存储条件。
func TestRequestsListFilters(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodGet, RequestsPath, "token",
		"?since=2026-10-01T00:00:00Z&until=2026-10-08T00:00:00Z&model=client-model&api_key_id=9"+
			"&status=failed&request_id=req-1")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	f := e.store.requestFilter
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); !f.Since.Equal(want) {
		t.Errorf("since = %s，期望 %s", f.Since, want)
	}
	if want := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC); !f.Until.Equal(want) {
		t.Errorf("until = %s，期望 %s", f.Until, want)
	}
	if f.RequestedModel != "client-model" || f.APIKeyID != 9 || f.Status != "failed" || f.RequestID != "req-1" {
		t.Errorf("过滤条件 = %+v，与请求不符", f)
	}
}

// TestRequestsRejectsBadParams 断言非法参数回 400 且不触达存储层。
func TestRequestsRejectsBadParams(t *testing.T) {
	tests := []struct{ name, query string }{
		{name: "status 非枚举", query: "?status=ok"},
		{name: "since 非 RFC3339", query: "?since=2026-10-01"},
		{name: "api_key_id 非正整数", query: "?api_key_id=0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv()
			status, env := e.do(t, http.MethodGet, RequestsPath, "token", tt.query)
			if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
				t.Fatalf("应 400: %d %s", status, env)
			}
			if e.store.requestFilter.AccountID != 0 {
				t.Errorf("非法参数不应触达存储层，得到 filter %+v", e.store.requestFilter)
			}
		})
	}
}

// TestRequestDetail 断言详情返回单条记录、尝试时间线与改写标注。
func TestRequestDetail(t *testing.T) {
	e := newTestEnv()
	row := requestRow()
	row.RequestShape = json.RawMessage(`{"model":"client-model","messages":{"__redacted":"array","len":1}}`)
	e.store.detailRow = &row
	e.store.attempts = []store.RequestAttempt{
		{RequestID: "req-1", Attempt: 1, Outcome: "failed", UpstreamStatus: 429,
			FailureClass: "rate_limit", ErrorCode: "upstream_rate_limited", DurationMS: 80},
		{RequestID: "req-1", Attempt: 2, Outcome: "ok", UpstreamStatus: 200,
			UpstreamProtocol: "anthropic_messages", CrossProtocol: true, DurationMS: 40},
	}

	status, env := e.do(t, http.MethodGet, RequestsPath+"/req-1", "token", "")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.detailAccount != 42 || e.store.detailID != "req-1" {
		t.Errorf("详情入参 = 账户 %d / 标识 %q，期望 42 / req-1", e.store.detailAccount, e.store.detailID)
	}
	var detail requestDetailView
	if err := json.Unmarshal(env["data"], &detail); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	if detail.Request.RequestID != "req-1" || detail.Request.Model != "client-model" {
		t.Errorf("request = %+v，与替身返回不符", detail.Request)
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempts 条数 = %d，期望 2", len(detail.Attempts))
	}
	if detail.Attempts[0].UpstreamStatus == nil || *detail.Attempts[0].UpstreamStatus != 429 {
		t.Errorf("首次尝试状态码 = %v，期望 429", detail.Attempts[0].UpstreamStatus)
	}
	if detail.Attempts[1].CrossProtocol != true || detail.Attempts[1].DurationMS != 40 {
		t.Errorf("第二次尝试 = %+v，与替身返回不符", detail.Attempts[1])
	}
	if len(detail.RewrittenParts) != 2 {
		t.Errorf("rewritten_parts = %v，期望两项", detail.RewrittenParts)
	}
	if string(detail.RequestShape) == "" || string(detail.RequestShape) == "null" {
		t.Errorf("request_shape 应透出脱敏报文，得到 %s", detail.RequestShape)
	}
	if string(detail.UpstreamRequestShape) != "null" || string(detail.ErrorResponseShape) != "null" {
		t.Errorf("无报文的两列应为 null，得到 %s / %s",
			detail.UpstreamRequestShape, detail.ErrorResponseShape)
	}
}

// TestRequestDetailWithoutRewrite 断言未改写时 rewritten_parts 是空数组而不是 null。
func TestRequestDetailWithoutRewrite(t *testing.T) {
	e := newTestEnv()
	row := requestRow()
	row.RewrittenParts = nil
	e.store.detailRow = &row

	status, env := e.do(t, http.MethodGet, RequestsPath+"/req-1", "token", "")
	if status != http.StatusOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	var detail requestDetailView
	if err := json.Unmarshal(env["data"], &detail); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	if detail.RewrittenParts == nil {
		t.Errorf("rewritten_parts 应为空数组，得到 null：%s", env["data"])
	}
}

// TestRequestDetailNotFound 断言不属于当前账户或不存在都回 404：
// 以 403 区分会让 request_id 成为探测他人请求的入口。
func TestRequestDetailNotFound(t *testing.T) {
	e := newTestEnv()
	e.store.detailErr = sql.ErrNoRows
	status, env := e.do(t, http.MethodGet, RequestsPath+"/other", "token", "")
	if status != http.StatusNotFound || codeOf(t, env) != webapi.CodeNotFound {
		t.Fatalf("应 404: %d %s", status, env)
	}
}

// TestRequestDetailUnknownPath 断言 /requests/ 与多段路径都回 404 而不是当成标识。
func TestRequestDetailUnknownPath(t *testing.T) {
	for _, path := range []string{RequestsPath + "/", RequestsPath + "/a/b"} {
		e := newTestEnv()
		status, env := e.do(t, http.MethodGet, path, "token", "")
		if status != http.StatusNotFound || codeOf(t, env) != webapi.CodeNotFound {
			t.Errorf("%s 应 404: %d %s", path, status, env)
		}
		if e.store.detailID != "" {
			t.Errorf("%s 不应进入详情分支，得到标识 %q", path, e.store.detailID)
		}
	}
}

// TestRequestStats 断言聚合端点不被当成请求详情，且回分组计数。
func TestRequestStats(t *testing.T) {
	e := newTestEnv()
	e.store.stats = []store.RequestStatsItem{
		{Key: "2026-10-08", Total: 5, Success: 3, Failed: 1, Cancelled: 1},
	}
	status, env := e.do(t, http.MethodGet, RequestsPath+requestStatsSuffix, "token", "?group_by=day")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.detailID != "" {
		t.Errorf("聚合路径不应进入详情分支，得到标识 %q", e.store.detailID)
	}
	q := e.store.statsQuery
	if q.AccountID != 42 || q.GroupBy != "day" {
		t.Errorf("聚合条件 = %+v，期望账户 42 分组 day", q)
	}
	var data struct {
		Items []requestStatsItemView `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	want := requestStatsItemView{Key: "2026-10-08", Total: 5, Success: 3, Failed: 1, Cancelled: 1}
	if len(data.Items) != 1 || data.Items[0] != want {
		t.Errorf("items = %+v，期望 %+v", data.Items, want)
	}
}

// TestRequestStatsDefaultsAndErrors 断言分组维度缺省为 day，非法取值回 400。
func TestRequestStatsDefaultsAndErrors(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodGet, RequestsPath+requestStatsSuffix, "token", "")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.statsQuery.GroupBy != defaultGroupBy {
		t.Errorf("缺省分组 = %q，期望 %q", e.store.statsQuery.GroupBy, defaultGroupBy)
	}

	e = newTestEnv()
	status, env = e.do(t, http.MethodGet, RequestsPath+requestStatsSuffix, "token", "?group_by=week")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("非法分组应 400: %d %s", status, env)
	}
	if e.store.statsQuery.AccountID != 0 {
		t.Errorf("非法分组不应触达存储层，得到 %+v", e.store.statsQuery)
	}
}

// TestRequestsUnauthorized 断言三个端点未登录都回 401。
func TestRequestsUnauthorized(t *testing.T) {
	for _, path := range []string{RequestsPath, RequestsPath + "/req-1", RequestsPath + requestStatsSuffix} {
		e := newTestEnv()
		status, env := e.do(t, http.MethodGet, path, "", "")
		if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
			t.Errorf("%s 缺少令牌应 401: %d %s", path, status, env)
		}
	}
}

// TestRequestsStoreErrors 断言存储层失败回 500。
func TestRequestsStoreErrors(t *testing.T) {
	e := newTestEnv()
	e.store.requestErr = errors.New("数据库不可达")
	status, env := e.do(t, http.MethodGet, RequestsPath, "token", "")
	if status != http.StatusInternalServerError || codeOf(t, env) != webapi.CodeInternal {
		t.Fatalf("列表应 500: %d %s", status, env)
	}

	e = newTestEnv()
	e.store.detailErr = errors.New("数据库不可达")
	status, env = e.do(t, http.MethodGet, RequestsPath+"/req-1", "token", "")
	if status != http.StatusInternalServerError || codeOf(t, env) != webapi.CodeInternal {
		t.Fatalf("详情应 500: %d %s", status, env)
	}

	e = newTestEnv()
	e.store.statsErr = errors.New("数据库不可达")
	status, env = e.do(t, http.MethodGet, RequestsPath+requestStatsSuffix, "token", "")
	if status != http.StatusInternalServerError || codeOf(t, env) != webapi.CodeInternal {
		t.Fatalf("聚合应 500: %d %s", status, env)
	}
}

// TestRequestCorruptUsage 断言用量 JSON 损坏时回 500 而不是跳过该行。
func TestRequestCorruptUsage(t *testing.T) {
	e := newTestEnv()
	row := requestRow()
	row.Usage = json.RawMessage(`{"input_tokens":"ten"}`)
	e.store.requestRows = []store.RequestLogRow{row}
	e.store.requestTotal = 1
	status, env := e.do(t, http.MethodGet, RequestsPath, "token", "")
	if status != http.StatusInternalServerError || codeOf(t, env) != webapi.CodeInternal {
		t.Fatalf("应 500: %d %s", status, env)
	}
}
