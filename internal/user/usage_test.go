package user

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖用量流水列表：作用域推导、过滤参数、行映射与错误分支。
// 数据访问用替身，全链不经数据库。

// usageRow 构造一条替身流水行。
func usageRow() store.AccountUsageRow {
	return store.AccountUsageRow{
		ID:             5,
		Model:          "up-model",
		RequestedModel: "client-model",
		Protocol:       "openai_chat",
		CrossProtocol:  true,
		Usage: json.RawMessage(`{"input_token":10,"output_token":4,"cache_read_token":3,` +
			`"cache_write_5m":2,"reasoning_token":1,"request":1}`),
		GrossAmount: "0.1",
		Multiplier:  "2",
		CreatedAt:   time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
	}
}

// TestUsageItemMapping 断言对外形状：模型名两侧互换、分量为十进制文本、扣减量已折算。
func TestUsageItemMapping(t *testing.T) {
	e := newTestEnv()
	e.store.usageRows = []store.AccountUsageRow{usageRow()}
	e.store.usageTotal = 1

	status, env := e.do(t, http.MethodGet, UsagePath, "token", "?page=2&size=5")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.usageFilter.AccountID != 42 {
		t.Errorf("账户 = %d，期望会话推导出的 42", e.store.usageFilter.AccountID)
	}
	if e.store.usageFilter.Limit != 5 || e.store.usageFilter.Offset != 5 {
		t.Errorf("分页 = limit %d offset %d，期望 5 / 5", e.store.usageFilter.Limit, e.store.usageFilter.Offset)
	}
	for _, key := range []string{"page", "size", "total"} {
		var got *int
		if err := json.Unmarshal(env[key], &got); err != nil {
			t.Fatalf("解析 %s: %v", key, err)
		}
		if got == nil {
			t.Fatalf("%s 应为分页填充值，得到 null", key)
		}
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
		"model":          `"client-model"`,
		"upstream_model": `"up-model"`,
		"protocol":       `"openai_chat"`,
		"cross_protocol": `true`,
		"charged_amount": `"0.2"`,
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
		CacheWrite5mTokens: 2, ReasoningTokens: 1,
	}
	if usage != wantUsage {
		t.Errorf("usage = %+v，期望 %+v", usage, wantUsage)
	}
}

// TestUsageFilters 断言四个过滤参数按契约映射到存储条件。
func TestUsageFilters(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodGet, UsagePath, "token",
		"?since=2026-10-01T00:00:00Z&until=2026-10-08T00:00:00Z&model=client-model&api_key_id=9")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	f := e.store.usageFilter
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); !f.Since.Equal(want) {
		t.Errorf("since = %s，期望 %s", f.Since, want)
	}
	if want := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC); !f.Until.Equal(want) {
		t.Errorf("until = %s，期望 %s", f.Until, want)
	}
	if f.RequestedModel != "client-model" {
		t.Errorf("model = %q，期望 client-model", f.RequestedModel)
	}
	if f.APIKeyID != 9 {
		t.Errorf("api_key_id = %d，期望 9", f.APIKeyID)
	}
}

// TestUsageRejectsBadParams 断言时刻与密钥 id 非法都回 400，且不触达存储层。
func TestUsageRejectsBadParams(t *testing.T) {
	tests := []struct{ name, query string }{
		{name: "since 非 RFC3339", query: "?since=2026-10-01"},
		{name: "until 非 RFC3339", query: "?until=yesterday"},
		{name: "api_key_id 为零", query: "?api_key_id=0"},
		{name: "api_key_id 非整数", query: "?api_key_id=abc"},
		{name: "size 为零", query: "?size=0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv()
			status, env := e.do(t, http.MethodGet, UsagePath, "token", tt.query)
			if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
				t.Fatalf("应 400: %d %s", status, env)
			}
			if e.store.usageFilter.AccountID != 0 {
				t.Errorf("非法参数不应触达存储层，得到 filter %+v", e.store.usageFilter)
			}
		})
	}
}

// TestUsageStoreError 断言存储层失败回 500，且不把内部错误写进信封。
func TestUsageStoreError(t *testing.T) {
	e := newTestEnv()
	e.store.usageErr = errors.New("数据库不可达")
	status, env := e.do(t, http.MethodGet, UsagePath, "token", "")
	if status != http.StatusInternalServerError || codeOf(t, env) != webapi.CodeInternal {
		t.Fatalf("应 500: %d %s", status, env)
	}
}

// TestUsageCorruptAmount 断言库中金额无法解析时回 500 而不是跳过该行：
// 静默少一行会让用户以为那次调用没有发生。
func TestUsageCorruptAmount(t *testing.T) {
	e := newTestEnv()
	row := usageRow()
	row.GrossAmount = "not-a-decimal"
	e.store.usageRows = []store.AccountUsageRow{row}
	e.store.usageTotal = 1
	status, env := e.do(t, http.MethodGet, UsagePath, "token", "")
	if status != http.StatusInternalServerError || codeOf(t, env) != webapi.CodeInternal {
		t.Fatalf("应 500: %d %s", status, env)
	}
}

// TestUsageUnauthorizedAndMethod 断言未登录回 401、非 GET 回 400。
func TestUsageUnauthorizedAndMethod(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodGet, UsagePath, "", "")
	if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
		t.Fatalf("缺少令牌应 401: %d %s", status, env)
	}
	status, env = e.do(t, http.MethodPost, UsagePath, "token", "")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("非 GET 应 400: %d %s", status, env)
	}
}

// TestUsageEmptyHistory 断言没有流水时回空数组而不是 null：
// 前端按数组渲染，null 会迫使每个列表页多写一条分支。
func TestUsageEmptyHistory(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodGet, UsagePath, "token", "")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	var data struct {
		Items []usageItemView `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	if data.Items == nil {
		t.Errorf("items 应为空数组，得到 null：%s", env["data"])
	}
}
