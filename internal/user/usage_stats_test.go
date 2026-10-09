package user

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖用量聚合：作用域推导、分组维度、合计映射与错误分支。
// 数据访问用替身，全链不经数据库；合计与明细的自洽由 store 的集成用例证明。

// usageStatsData 是聚合响应的 data 形状，用于解码断言。
type usageStatsData struct {
	Items []struct {
		Key           string         `json:"key"`
		Calls         int64          `json:"calls"`
		Usage         map[string]int `json:"usage"`
		ChargedAmount string         `json:"charged_amount"`
	} `json:"items"`
}

// decodeUsageStats 解码聚合响应的 data。
func decodeUsageStats(t *testing.T, env map[string]json.RawMessage) usageStatsData {
	t.Helper()
	var data usageStatsData
	raw, err := json.Marshal(env["data"])
	if err != nil {
		t.Fatalf("编码 data: %v", err)
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	return data
}

// TestUsageStatsMapping 断言聚合行的对外形状：分组值、次数、分量合计与十进制金额。
func TestUsageStatsMapping(t *testing.T) {
	e := newTestEnv()
	e.store.usageStats = []store.UsageStatsItem{{
		Key:   "2026-10-08",
		Calls: 3,
		Tokens: map[string]int64{
			"input_token":  11,
			"output_token": 4,
			"request":      3,
			"unknown_key":  99,
		},
		ChargedAmount: "0.42",
	}}

	status, env := e.do(t, http.MethodGet, UsagePath+usageStatsSuffix, "token", "?group_by=day")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.usageStatsQuery.AccountID != 42 {
		t.Errorf("账户 = %d，期望会话推导出的 42", e.store.usageStatsQuery.AccountID)
	}
	if e.store.usageStatsQuery.GroupBy != "day" {
		t.Errorf("分组维度 = %q，期望 day", e.store.usageStatsQuery.GroupBy)
	}

	items := decodeUsageStats(t, env).Items
	if len(items) != 1 {
		t.Fatalf("分组数 = %d，期望 1", len(items))
	}
	item := items[0]
	if item.Key != "2026-10-08" || item.Calls != 3 || item.ChargedAmount != "0.42" {
		t.Errorf("分组行 = %+v，期望与替身一致", item)
	}
	// 分量字段恒出现：未取得的为 0。
	for field, want := range map[string]int{
		"input_tokens": 11, "output_tokens": 4, "cache_read_tokens": 0,
		"cache_write_tokens": 0, "cache_write_5m_tokens": 0, "cache_write_1h_tokens": 0,
		"reasoning_tokens": 0, "server_tool_uses": 0,
	} {
		if got := item.Usage[field]; got != want {
			t.Errorf("usage.%s = %d，期望 %d", field, got, want)
		}
	}
	// 未登记的键与 request 分量都不进 token 分量：request 与 calls 同义。
	for _, key := range []string{"unknown_key", "request"} {
		if _, ok := item.Usage[key]; ok {
			t.Errorf("usage 里不应出现 %q", key)
		}
	}
}

// TestUsageStatsGroupByValidation 断言分组维度的白名单与缺省值。
func TestUsageStatsGroupByValidation(t *testing.T) {
	e := newTestEnv()
	e.store.usageStats = []store.UsageStatsItem{}

	if status, _ := e.do(t, http.MethodGet, UsagePath+usageStatsSuffix, "token", ""); status != http.StatusOK {
		t.Fatalf("缺省分组应 200: %d", status)
	}
	if e.store.usageStatsQuery.GroupBy != "day" {
		t.Errorf("缺省分组 = %q，期望 day", e.store.usageStatsQuery.GroupBy)
	}

	if status, _ := e.do(t, http.MethodGet, UsagePath+usageStatsSuffix, "token", "?group_by=api_key"); status != http.StatusOK {
		t.Fatalf("api_key 分组应 200: %d", status)
	}

	// status 是请求记录的聚合维度，用量聚合不接受。
	status, env := e.do(t, http.MethodGet, UsagePath+usageStatsSuffix, "token", "?group_by=status")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("未知分组维度应 400: %d %s", status, env)
	}
}

// TestUsageStatsFiltersAndErrors 断言语义过滤参数透传与各错误分支。
func TestUsageStatsFiltersAndErrors(t *testing.T) {
	e := newTestEnv()
	e.store.usageStats = []store.UsageStatsItem{}

	status, env := e.do(t, http.MethodGet, UsagePath+usageStatsSuffix, "token",
		"?since=2026-10-01T00:00:00Z&until=2026-10-08T00:00:00Z&model=client-model&api_key_id=9")
	if status != http.StatusOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	got := e.store.usageStatsQuery
	if got.APIKeyID != 9 || got.RequestedModel != "client-model" {
		t.Errorf("过滤透传 = %+v，期望 api_key 9 / client-model", got)
	}
	if got.Since.IsZero() || got.Until.IsZero() {
		t.Errorf("时间区间未透传: %+v", got)
	}

	// 时间非法：400，不落到存储层（替身这一轮返回错误，若真调用会得到 500）。
	e.store.usageStatsErr = errors.New("不应被调用")
	status, env = e.do(t, http.MethodGet, UsagePath+usageStatsSuffix, "token", "?since=昨天")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("非法时间应 400: %d %s", status, env)
	}

	// 存储错误：500。
	e.store.usageStatsErr = errors.New("库故障")
	status, env = e.do(t, http.MethodGet, UsagePath+usageStatsSuffix, "token", "")
	if status != http.StatusInternalServerError || codeOf(t, env) != webapi.CodeInternal {
		t.Fatalf("存储错误应 500: %d %s", status, env)
	}

	// 非 GET：400。
	if status, _ := e.do(t, http.MethodPost, UsagePath+usageStatsSuffix, "token", ""); status != http.StatusBadRequest {
		t.Fatalf("POST 应 400: %d", status)
	}
}

// TestUsageStatsUnauthorized 断言未登录时不落存储层。
func TestUsageStatsUnauthorized(t *testing.T) {
	e := newTestEnv()
	e.session.err = errors.New("会话无效")

	status, env := e.do(t, http.MethodGet, UsagePath+usageStatsSuffix, "", "")
	if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
		t.Fatalf("未登录应 401: %d %s", status, env)
	}
}
