package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sumwai/tokenmp/internal/me"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖用户级端点的可观察行为：会话鉴权、作用域推导、摘要口径透传与错误分支。
// 数据访问与摘要都用替身，全链不经数据库。

type fakeSessions struct {
	userID uint64
	role   string
	err    error
}

func (f *fakeSessions) SessionSubject(_ context.Context, _ string) (uint64, string, error) {
	if f.err != nil {
		return 0, "", f.err
	}
	return f.userID, f.role, nil
}

type fakeStore struct {
	account *store.Account
	err     error

	keys       []store.APIKey
	total      int
	listErr    error
	listLimit  int
	listOffset int
	listOn     *bool
	inserted   store.APIKey
	insertErr  error
	byIDKey    *store.APIKey
	byIDErr    error
	lastEnable struct {
		id      uint64
		enabled bool
	}
	enableErr error

	// 用量流水与模型目录：字段承载替身返回值与最后一次调用的入参。
	usageRows       []store.AccountUsageRow
	usageTotal      int
	usageErr        error
	usageFilter     store.AccountUsageFilter
	models          []store.AccountModel
	modelsErr       error
	modelsAccountID uint64

	// 请求记录：字段承载替身返回值与最后一次调用的入参。
	requestRows   []store.RequestLogRow
	requestTotal  int
	requestErr    error
	requestFilter store.RequestLogFilter
	detailRow     *store.RequestLogRow
	detailErr     error
	detailAccount uint64
	detailID      string
	attempts      []store.RequestAttempt
	attemptsErr   error
	stats         []store.RequestStatsItem
	statsErr      error
	statsQuery    store.RequestStatsQuery
}

func (f *fakeStore) AccountByOwner(_ context.Context, _ uint64) (*store.Account, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.account, nil
}

func (f *fakeStore) InsertAPIKey(_ context.Context, k store.APIKey) (uint64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	f.inserted = k
	return 99, nil
}

func (f *fakeStore) ListAPIKeysByAccount(_ context.Context, _ uint64, enabled *bool, limit, offset int) ([]store.APIKey, int, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	f.listOn, f.listLimit, f.listOffset = enabled, limit, offset
	return f.keys, f.total, nil
}

func (f *fakeStore) APIKeyByID(_ context.Context, _ uint64) (*store.APIKey, error) {
	if f.byIDErr != nil {
		return nil, f.byIDErr
	}
	return f.byIDKey, nil
}

func (f *fakeStore) SetAPIKeyEnabled(_ context.Context, id uint64, enabled bool) error {
	if f.enableErr != nil {
		return f.enableErr
	}
	f.lastEnable.id, f.lastEnable.enabled = id, enabled
	return nil
}

func (f *fakeStore) ListAccountUsage(_ context.Context, filter store.AccountUsageFilter) ([]store.AccountUsageRow, int, error) {
	f.usageFilter = filter
	if f.usageErr != nil {
		return nil, 0, f.usageErr
	}
	return f.usageRows, f.usageTotal, nil
}

func (f *fakeStore) ListAccountModels(_ context.Context, accountID uint64) ([]store.AccountModel, error) {
	f.modelsAccountID = accountID
	if f.modelsErr != nil {
		return nil, f.modelsErr
	}
	return f.models, nil
}

func (f *fakeStore) ListRequestLogs(_ context.Context, filter store.RequestLogFilter) ([]store.RequestLogRow, int, error) {
	f.requestFilter = filter
	if f.requestErr != nil {
		return nil, 0, f.requestErr
	}
	return f.requestRows, f.requestTotal, nil
}

func (f *fakeStore) RequestLogByRequestID(_ context.Context, accountID uint64, requestID string) (*store.RequestLogRow, error) {
	f.detailAccount, f.detailID = accountID, requestID
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	return f.detailRow, nil
}

func (f *fakeStore) RequestAttempts(_ context.Context, _ string) ([]store.RequestAttempt, error) {
	if f.attemptsErr != nil {
		return nil, f.attemptsErr
	}
	return f.attempts, nil
}

func (f *fakeStore) RequestStats(_ context.Context, q store.RequestStatsQuery) ([]store.RequestStatsItem, error) {
	f.statsQuery = q
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	return f.stats, nil
}

type fakeSummary struct {
	accountID uint64
	apiKeyID  uint64
	recent    int
	summary   *me.Summary
	err       error
}

func (f *fakeSummary) Summary(_ context.Context, accountID, apiKeyID uint64, recentLimit int) (*me.Summary, error) {
	f.accountID, f.apiKeyID, f.recent = accountID, apiKeyID, recentLimit
	if f.err != nil {
		return nil, f.err
	}
	return f.summary, nil
}

type testEnv struct {
	handler http.Handler
	session *fakeSessions
	store   *fakeStore
	summary *fakeSummary
}

func newTestEnv() *testEnv {
	session := &fakeSessions{userID: 7, role: store.RoleMember}
	st := &fakeStore{account: &store.Account{ID: 42, Code: "acc_42"}}
	summary := &fakeSummary{summary: &me.Summary{Account: me.AccountView{ID: 42, Code: "acc_42"}}}
	return &testEnv{
		handler: NewHandler(Options{Sessions: session, Store: st, Summary: summary}),
		session: session,
		store:   st,
		summary: summary,
	}
}

// do 发起一次请求并解码信封，同时校验六个字段固定出现。
func (e *testEnv) do(t *testing.T, method, path, token, query string) (int, map[string]json.RawMessage) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path+query, nil)
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

func codeOf(t *testing.T, envelope map[string]json.RawMessage) int {
	t.Helper()
	var code int
	if err := json.Unmarshal(envelope["code"], &code); err != nil {
		t.Fatalf("解析 code: %v", err)
	}
	return code
}

// ---- 用例 ----

// TestAccountUnauthorized 断言缺少令牌与令牌无效都回 401 且文案一致。
func TestAccountUnauthorized(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodGet, AccountPath, "", "")
	if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
		t.Fatalf("缺少令牌应 401: %d %s", status, env)
	}

	e.session.err = errors.New("会话已失效")
	status, env = e.do(t, http.MethodGet, AccountPath, "stale", "")
	if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
		t.Fatalf("令牌无效应 401: %d %s", status, env)
	}
}

// TestAccountWithoutOwnedAccount 断言会话有效但没有归属账户时回 403 而不是 401：
// 两者的下一步动作不同，不能折叠成同一个码。
func TestAccountWithoutOwnedAccount(t *testing.T) {
	e := newTestEnv()
	e.store.err = sql.ErrNoRows
	status, env := e.do(t, http.MethodGet, AccountPath, "token", "")
	if status != http.StatusForbidden || codeOf(t, env) != webapi.CodeForbidden {
		t.Fatalf("无归属账户应 403: %d %s", status, env)
	}
}

// TestAccountReturnsSummary 断言摘要取自会话推导出的账户，且不带密钥维度。
func TestAccountReturnsSummary(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodGet, AccountPath, "token", "")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.summary.accountID != 42 {
		t.Errorf("摘要账户 = %d，期望 42", e.summary.accountID)
	}
	if e.summary.apiKeyID != 0 {
		t.Errorf("账户概览不应带密钥维度，得到 %d", e.summary.apiKeyID)
	}
	if e.summary.recent != defaultRecent {
		t.Errorf("缺省流水条数 = %d，期望 %d", e.summary.recent, defaultRecent)
	}
	var data me.Summary
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	if data.Account.Code != "acc_42" {
		t.Errorf("账户编码 = %q，期望 acc_42", data.Account.Code)
	}
}

// TestAccountRecentParam 断言 recent 的缺省、封顶与非法分支。
func TestAccountRecentParam(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
		code  int
	}{
		{name: "缺省", query: "", want: defaultRecent, code: webapi.CodeOK},
		{name: "零值合法", query: "?recent=0", want: 0, code: webapi.CodeOK},
		{name: "超上限截断", query: "?recent=500", want: maxRecent, code: webapi.CodeOK},
		{name: "负数非法", query: "?recent=-1", want: 0, code: webapi.CodeBadRequest},
		{name: "非整数非法", query: "?recent=abc", want: 0, code: webapi.CodeBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv()
			status, env := e.do(t, http.MethodGet, AccountPath, "token", tt.query)
			if got := codeOf(t, env); got != tt.code {
				t.Fatalf("code = %d，期望 %d（status %d，body %s）", got, tt.code, status, env)
			}
			if tt.code == webapi.CodeOK && e.summary.recent != tt.want {
				t.Errorf("recent = %d，期望 %d", e.summary.recent, tt.want)
			}
		})
	}
}

// TestAccountRejectsNonGet 断言非 GET 方法回 400。
func TestAccountRejectsNonGet(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodPost, AccountPath, "token", "")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("非 GET 应 400: %d %s", status, env)
	}
}

// TestUnknownPathReturnsEnvelope 断言子树内未声明的路径回页面信封 404。
func TestUnknownPathReturnsEnvelope(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodGet, PathPrefix+"nope", "token", "")
	if status != http.StatusNotFound || codeOf(t, env) != webapi.CodeNotFound {
		t.Fatalf("未知路径应 404: %d %s", status, env)
	}
}
