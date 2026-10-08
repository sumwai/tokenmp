package user

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/apikey"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖密钥自助管理的可观察行为：分页与过滤透传、明文只回一次、
// 归属校验、视图裁剪与各错误分支。

// doJSON 发起一次带 JSON 体的请求并解码信封。
func (e *testEnv) doJSON(t *testing.T, method, path, token string, body any) (int, map[string]json.RawMessage) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体：%v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("响应不是 JSON（%d）: %s", rec.Code, rec.Body.String())
	}
	for _, key := range []string{"code", "data", "message", "page", "size", "total"} {
		if _, ok := envelope[key]; !ok {
			t.Errorf("信封缺少字段 %s: %s", key, rec.Body.String())
		}
	}
	return rec.Code, envelope
}

// TestListKeysTransfersPagingAndTrimsView 断言分页参数透传，且视图不含内部字段。
func TestListKeysTransfersPagingAndTrimsView(t *testing.T) {
	e := newTestEnv()
	merchant := uint64(3)
	expires := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	e.store.keys = []store.APIKey{{
		ID: 5, AccountID: 42, MerchantID: &merchant, Name: "默认",
		KeyHash: "deadbeef", KeyPrefix: "sk-0123", Enabled: true, ExpiresAt: &expires,
	}}
	e.store.total = 7

	status, env := e.doJSON(t, http.MethodGet, KeysPath+"?page=2&size=5", "token", nil)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.listLimit != 5 || e.store.listOffset != 5 {
		t.Errorf("分页透传 = (limit %d, offset %d)，期望 (5, 5)", e.store.listLimit, e.store.listOffset)
	}
	if e.store.listOn != nil {
		t.Error("未指定 enabled 时不应过滤")
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
	for _, forbidden := range []string{"account_id", "merchant_id", "key_hash"} {
		if _, ok := data.Items[0][forbidden]; ok {
			t.Errorf("视图不应包含 %s：%s", forbidden, env["data"])
		}
	}
	var page, size, total int
	if err := json.Unmarshal(env["page"], &page); err != nil {
		t.Fatalf("解析 page: %v", err)
	}
	if err := json.Unmarshal(env["size"], &size); err != nil {
		t.Fatalf("解析 size: %v", err)
	}
	if err := json.Unmarshal(env["total"], &total); err != nil {
		t.Fatalf("解析 total: %v", err)
	}
	if page != 2 || size != 5 || total != 7 {
		t.Errorf("分页字段 = (%d, %d, %d)，期望 (2, 5, 7)", page, size, total)
	}
}

// TestListKeysPagingAndFilterBranches 断言 size 封顶、enabled 过滤与非法参数分支。
func TestListKeysPagingAndFilterBranches(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    int
		limit   int
		filter  *bool
		wantErr bool
	}{
		{name: "缺省", query: "", want: webapi.CodeOK, limit: defaultPageSize},
		{name: "size 封顶", query: "?size=500", want: webapi.CodeOK, limit: maxPageSize},
		{name: "enabled 过滤", query: "?enabled=false", want: webapi.CodeOK, limit: defaultPageSize, filter: boolPtr(false)},
		{name: "page 为零", query: "?page=0", want: webapi.CodeBadRequest, wantErr: true},
		{name: "size 非整数", query: "?size=abc", want: webapi.CodeBadRequest, wantErr: true},
		{name: "enabled 非布尔", query: "?enabled=maybe", want: webapi.CodeBadRequest, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv()
			status, env := e.doJSON(t, http.MethodGet, KeysPath+tt.query, "token", nil)
			if got := codeOf(t, env); got != tt.want {
				t.Fatalf("code = %d，期望 %d（status %d）", got, tt.want, status)
			}
			if tt.wantErr {
				return
			}
			if e.store.listLimit != tt.limit {
				t.Errorf("limit = %d，期望 %d", e.store.listLimit, tt.limit)
			}
			if tt.filter == nil {
				if e.store.listOn != nil {
					t.Error("不应过滤 enabled")
				}
				return
			}
			if e.store.listOn == nil || *e.store.listOn != *tt.filter {
				t.Errorf("enabled 过滤 = %v，期望 %v", e.store.listOn, *tt.filter)
			}
		})
	}
}

// TestCreateKeyReturnsSecretOnce 断言创建响应带明文，落库的只有哈希与前缀。
func TestCreateKeyReturnsSecretOnce(t *testing.T) {
	e := newTestEnv()
	status, env := e.doJSON(t, http.MethodPost, KeysPath, "token", map[string]any{"name": "  生产  "})
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}

	var data struct {
		ID        uint64 `json:"id"`
		Name      string `json:"name"`
		KeyPrefix string `json:"key_prefix"`
		Enabled   bool   `json:"enabled"`
		Secret    string `json:"secret"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	if data.Secret == "" {
		t.Fatal("创建响应必须带明文")
	}
	if data.Name != "生产" {
		t.Errorf("名称应去除首尾空白，得到 %q", data.Name)
	}
	if data.KeyPrefix != apikey.Prefix(data.Secret) {
		t.Errorf("前缀 = %q，与明文的 apikey.Prefix 不一致", data.KeyPrefix)
	}
	if !data.Enabled {
		t.Error("新建密钥应处于启用状态")
	}

	// 落库的形状：账户归属来自会话，哈希与前缀由 apikey 规则给出。
	if e.store.inserted.AccountID != 42 {
		t.Errorf("落库归属 = %d，期望 42（会话推导）", e.store.inserted.AccountID)
	}
	if e.store.inserted.KeyHash != apikey.Hash(data.Secret) {
		t.Errorf("落库哈希与明文不对应")
	}
	if e.store.inserted.KeyPrefix != apikey.Prefix(data.Secret) {
		t.Errorf("落库前缀与明文不对应")
	}
}

// TestCreateKeyRejectsInvalidBody 断言名字缺失、超长与未知字段都回 400。
func TestCreateKeyRejectsInvalidBody(t *testing.T) {
	tests := []struct {
		name string
		body any
	}{
		{name: "名字缺失", body: map[string]any{}},
		{name: "名字为空", body: map[string]any{"name": "   "}},
		{name: "名字超长", body: map[string]any{"name": string(make([]rune, maxKeyNameRunes+1))}},
		{name: "未知字段", body: map[string]any{"name": "ok", "account_id": 7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv()
			status, env := e.doJSON(t, http.MethodPost, KeysPath, "token", tt.body)
			if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
				t.Fatalf("应 400: %d %s", status, env)
			}
		})
	}
}

// TestRevokeKeyChecksOwnership 断言只有归属当前账户的密钥才能被吊销。
func TestRevokeKeyChecksOwnership(t *testing.T) {
	e := newTestEnv()
	path := KeysPath + "/5/revoke"

	// 他人账户的密钥按不存在处理，不以 403 区分存在性。
	other := uint64(9)
	e.store.byIDKey = &store.APIKey{ID: 5, AccountID: other}
	status, env := e.doJSON(t, http.MethodPost, path, "token", nil)
	if status != http.StatusNotFound || codeOf(t, env) != webapi.CodeNotFound {
		t.Fatalf("他人密钥应 404: %d %s", status, env)
	}
	if e.store.lastEnable.id != 0 {
		t.Error("他人密钥不应被改状态")
	}

	// 不存在。
	e.store.byIDErr = sql.ErrNoRows
	status, env = e.doJSON(t, http.MethodPost, path, "token", nil)
	if status != http.StatusNotFound || codeOf(t, env) != webapi.CodeNotFound {
		t.Fatalf("不存在应 404: %d %s", status, env)
	}

	// 属于当前账户：置 false。
	e.store.byIDErr = nil
	e.store.byIDKey = &store.APIKey{ID: 5, AccountID: 42}
	status, env = e.doJSON(t, http.MethodPost, path, "token", nil)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.lastEnable.id != 5 || e.store.lastEnable.enabled {
		t.Errorf("吊销结果 = %+v，期望 id=5 enabled=false", e.store.lastEnable)
	}
}

// TestRevokeKeyRejectsBadPathOrMethod 断言路径非法与非 POST 方法都回 400 或 404。
func TestRevokeKeyRejectsBadPathOrMethod(t *testing.T) {
	e := newTestEnv()
	for _, path := range []string{KeysPath + "/abc/revoke", KeysPath + "/0/revoke", KeysPath + "/5/unknown"} {
		status, env := e.doJSON(t, http.MethodPost, path, "token", nil)
		if status != http.StatusNotFound || codeOf(t, env) != webapi.CodeNotFound {
			t.Errorf("%s 应 404: %d %s", path, status, env)
		}
	}
	status, env := e.doJSON(t, http.MethodGet, KeysPath+"/5/revoke", "token", nil)
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("非 POST 应 400: %d %s", status, env)
	}
}

// TestKeysRequireSession 断言密钥端点同样由会话推导作用域。
func TestKeysRequireSession(t *testing.T) {
	e := newTestEnv()
	status, env := e.doJSON(t, http.MethodGet, KeysPath, "", nil)
	if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
		t.Fatalf("缺少令牌应 401: %d %s", status, env)
	}
}

// boolPtr 取布尔字面量的地址，便于表驱动用例给出期望值。
func boolPtr(v bool) *bool { return &v }
