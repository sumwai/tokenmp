package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/identity"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖管理面只读清单：会话与身份两道判定、分页与会话信封、过滤参数透传、
// 以及各视图的字段白名单。判定与派生都不经数据库。

// fakeSessions 是会话解析替身：令牌非空即有效，身份集合由用例给出。
type fakeSessions struct {
	userID uint64
	roles  []string
	err    error
}

func (f *fakeSessions) SessionSubject(_ context.Context, token string) (uint64, []string, error) {
	if f.err != nil {
		return 0, nil, f.err
	}
	if strings.TrimSpace(token) == "" {
		return 0, nil, errors.New("adminapi: 空令牌")
	}
	return f.userID, f.roles, nil
}

// fakeLister 记录最后一次调用的过滤参数，并返回固定行。
type fakeLister struct {
	channels      []store.Channel
	credentials   []admin.CredentialView
	modelMaps     []store.ModelMap
	accounts      []store.Account
	pricing       []store.Pricing
	quotas        []admin.QuotaView
	adjustments   []store.Adjustment
	usage         []store.UsageListRow
	err           error
	lastMerchant  uint64
	lastModel     string
	lastScope     billing.Scope
	lastScopeID   uint64
	lastAccountID uint64
	lastSince     time.Time
}

func (f *fakeLister) ListChannels(context.Context) ([]store.Channel, error) {
	return f.channels, f.err
}

func (f *fakeLister) ListCredentials(context.Context) ([]admin.CredentialView, error) {
	return f.credentials, f.err
}

func (f *fakeLister) ListModelMaps(context.Context) ([]store.ModelMap, error) {
	return f.modelMaps, f.err
}

func (f *fakeLister) ListAccounts(context.Context) ([]store.Account, error) {
	return f.accounts, f.err
}

func (f *fakeLister) ListPricing(_ context.Context, merchantID uint64, model string) ([]store.Pricing, error) {
	f.lastMerchant, f.lastModel = merchantID, model
	return f.pricing, f.err
}

func (f *fakeLister) ListQuotas(_ context.Context, scope billing.Scope, scopeID uint64) ([]admin.QuotaView, error) {
	f.lastScope, f.lastScopeID = scope, scopeID
	return f.quotas, f.err
}

func (f *fakeLister) ListAdjustments(_ context.Context, accountID uint64) ([]store.Adjustment, error) {
	f.lastAccountID = accountID
	return f.adjustments, f.err
}

func (f *fakeLister) ListUsage(_ context.Context, accountID uint64, since time.Time) ([]store.UsageListRow, error) {
	f.lastAccountID, f.lastSince = accountID, since
	return f.usage, f.err
}

// newEnv 构造「管理员 + 固定行」的测试环境。
func newEnv() (*Handler, *fakeSessions, *fakeLister) {
	session := &fakeSessions{userID: 7, roles: identity.Roles(store.RoleAdmin)}
	lister := &fakeLister{}
	return NewHandler(Options{Sessions: session, Lister: lister}), session, lister
}

// do 发一次请求，返回状态码与解出的信封字段。
func do(t *testing.T, h *Handler, method, path, token string) (int, map[string]json.RawMessage) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析信封 %q: %v", rec.Body.String(), err)
	}
	return rec.Code, env
}

// codeOf 读信封里的业务码。
func codeOf(t *testing.T, env map[string]json.RawMessage) int {
	t.Helper()
	var code int
	if err := json.Unmarshal(env["code"], &code); err != nil {
		t.Fatalf("解析 code: %v", err)
	}
	return code
}

// itemsOf 读信封里的 data.items 原始 JSON。
func itemsOf(t *testing.T, env map[string]json.RawMessage) json.RawMessage {
	t.Helper()
	var data map[string]json.RawMessage
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	items, ok := data[itemsKey]
	if !ok {
		t.Fatalf("data 里没有 %s: %s", itemsKey, env["data"])
	}
	return items
}

// metaOf 读信封的分页三字段。
func metaOf(t *testing.T, env map[string]json.RawMessage) (page, size, total int) {
	t.Helper()
	for name, target := range map[string]*int{"page": &page, "size": &size, "total": &total} {
		if err := json.Unmarshal(env[name], target); err != nil {
			t.Fatalf("解析 %s: %v", name, err)
		}
	}
	return page, size, total
}

// listPaths 是全部只读清单路径，按契约顺序排列。
var listPaths = []string{
	ChannelsPath, CredentialsPath, ModelMapsPath, AccountsPath,
	PricingPath, QuotasPath, AdjustmentsPath, UsagePath,
}

// TestUnauthorized 断言缺少令牌与令牌无效都回 401。
func TestUnauthorized(t *testing.T) {
	h, session, _ := newEnv()

	status, env := do(t, h, http.MethodGet, ChannelsPath, "")
	if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
		t.Fatalf("缺少令牌应 401: %d %s", status, env)
	}

	session.err = context.DeadlineExceeded
	status, env = do(t, h, http.MethodGet, ChannelsPath, "stale")
	if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
		t.Fatalf("令牌无效应 401: %d %s", status, env)
	}
}

// TestForbiddenForNonAdmin 断言已登录但没有管理面身份时回 403，且不透露子树里
// 有哪些端点：非管理员访问不存在的管理面路径同样得到 403。
func TestForbiddenForNonAdmin(t *testing.T) {
	for _, role := range []string{store.RoleMember, store.RolePartner} {
		h, session, _ := newEnv()
		session.roles = identity.Roles(role)
		for _, path := range append(append([]string{}, listPaths...), "/api/v1/admin/nope") {
			status, env := do(t, h, http.MethodGet, path, "token")
			if status != http.StatusForbidden || codeOf(t, env) != webapi.CodeForbidden {
				t.Fatalf("角色 %s 访问 %s 应 403: %d %s", role, path, status, env)
			}
		}
	}
}

// TestAdminListsReturnItems 断言管理员对八组清单都得到 200 与 items 数组。
func TestAdminListsReturnItems(t *testing.T) {
	h, _, lister := newEnv()
	lister.channels = []store.Channel{{ID: 1, MerchantID: 2, Name: "c", Type: store.ChannelType("openai-chat")}}
	lister.credentials = []admin.CredentialView{{ID: 3, MerchantID: 2, CredGroup: "g", Kind: "api", Prefix: "sk-…"}}
	lister.modelMaps = []store.ModelMap{{ID: 4, ChannelID: 1, Model: "m", UpstreamModel: "u", PriceMultiplier: "1"}}
	lister.accounts = []store.Account{{ID: 5, Code: "a", Name: "n", PriceMultiplier: "1", Status: store.StatusActive}}
	lister.pricing = []store.Pricing{{ID: 6, MerchantID: 2, Model: "m", Version: 1, EffectiveAt: time.Unix(0, 0).UTC()}}
	lister.quotas = []admin.QuotaView{{ID: 7, Scope: billing.ScopeAccount, ScopeID: 5, LimitAmount: "10"}}
	lister.adjustments = []store.Adjustment{{ID: 8, AccountID: 5, DeltaAmount: "-1", Reason: "退费"}}
	lister.usage = []store.UsageListRow{{ID: 9, AccountID: 5, Model: "m", GrossAmount: "0.5", Multiplier: "1"}}

	for _, path := range listPaths {
		status, env := do(t, h, http.MethodGet, path, "token")
		if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
			t.Fatalf("%s 应 200: %d %s", path, status, env)
		}
		items := itemsOf(t, env)
		if string(items) == "[]" || string(items) == "null" {
			t.Errorf("%s 应返回 items: %s", path, items)
		}
		itemsCount := strings.Count(string(items), `"id"`)
		if itemsCount != 1 {
			t.Errorf("%s items = %s，期望 1 条", path, items)
		}
	}
}

// TestChannelViewDropsConfig 断言渠道视图不回显 config：探针请求头里可能有鉴权 token。
func TestChannelViewDropsConfig(t *testing.T) {
	h, _, lister := newEnv()
	lister.channels = []store.Channel{{
		ID: 1, Name: "c", Config: json.RawMessage(`{"headers":{"Authorization":"Bearer secret"}}`),
	}}
	_, env := do(t, h, http.MethodGet, ChannelsPath, "token")
	items := string(itemsOf(t, env))
	if strings.Contains(items, "config") || strings.Contains(items, "secret") {
		t.Fatalf("渠道清单不应回显 config: %s", items)
	}
}

// TestCredentialViewKeepsMaskedPrefix 断言凭据清单只给脱敏前缀与形态。
func TestCredentialViewKeepsMaskedPrefix(t *testing.T) {
	h, _, lister := newEnv()
	lister.credentials = []admin.CredentialView{{ID: 3, Kind: "oauth", Prefix: "sk-…", Expired: true, Enabled: true}}
	_, env := do(t, h, http.MethodGet, CredentialsPath, "token")
	items := string(itemsOf(t, env))
	for _, want := range []string{`"kind":"oauth"`, `"prefix":"sk-…"`, `"expired":true`} {
		if !strings.Contains(items, want) {
			t.Errorf("凭据清单缺 %s: %s", want, items)
		}
	}
}

// TestPaging 断言分页按偏移切分，并把三字段写进信封。
func TestPaging(t *testing.T) {
	h, _, lister := newEnv()
	lister.channels = []store.Channel{{ID: 1}, {ID: 2}, {ID: 3}}

	status, env := do(t, h, http.MethodGet, ChannelsPath+"?page=2&size=2", "token")
	if status != http.StatusOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	page, size, total := metaOf(t, env)
	if page != 2 || size != 2 || total != 3 {
		t.Fatalf("分页 = %d/%d/%d，期望 2/2/3", page, size, total)
	}
	if items := string(itemsOf(t, env)); strings.Count(items, `"id"`) != 1 || !strings.Contains(items, `"id":3`) {
		t.Fatalf("第 2 页应为 1 条（id=3）: %s", items)
	}

	// 越界页返回空数组而不是 null：契约把 items 声明为数组。
	_, env = do(t, h, http.MethodGet, ChannelsPath+"?page=9&size=2", "token")
	if items := string(itemsOf(t, env)); items != "[]" {
		t.Fatalf("越界页应为空数组: %s", items)
	}
}

// TestBadPaging 断言非法分页参数回 400。
func TestBadPaging(t *testing.T) {
	h, _, _ := newEnv()
	for _, query := range []string{"?page=0", "?page=x", "?size=0", "?size=101"} {
		status, env := do(t, h, http.MethodGet, ChannelsPath+query, "token")
		if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
			t.Errorf("%s 应 400: %d %s", query, status, env)
		}
	}
}

// TestRejectsNonGet 断言写方法回 400：只读清单没有写入口。
func TestRejectsNonGet(t *testing.T) {
	h, _, _ := newEnv()
	for _, path := range listPaths {
		status, env := do(t, h, http.MethodPost, path, "token")
		if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
			t.Errorf("POST %s 应 400: %d %s", path, status, env)
		}
	}
}

// TestUnknownPath 断言子树内未声明的路径回 404。
func TestUnknownPath(t *testing.T) {
	h, _, _ := newEnv()
	status, env := do(t, h, http.MethodGet, "/api/v1/admin/merchants", "token")
	if status != http.StatusNotFound || codeOf(t, env) != webapi.CodeNotFound {
		t.Fatalf("未声明路径应 404: %d %s", status, env)
	}
}

// TestPricingFilters 断言商家与模型过滤透传到业务读取面。
func TestPricingFilters(t *testing.T) {
	h, _, lister := newEnv()
	status, _ := do(t, h, http.MethodGet, PricingPath+"?merchant_id=9&model=gpt-4o", "token")
	if status != http.StatusOK {
		t.Fatalf("应 200: %d", status)
	}
	if lister.lastMerchant != 9 || lister.lastModel != "gpt-4o" {
		t.Fatalf("过滤参数 = %d/%q，期望 9/gpt-4o", lister.lastMerchant, lister.lastModel)
	}

	status, env := do(t, h, http.MethodGet, PricingPath+"?merchant_id=0", "token")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("merchant_id=0 应 400: %d %s", status, env)
	}
}

// TestQuotaFilters 断言限额过滤与 CLI 同口径：scope + scope_id 成对，account_id 是简写。
func TestQuotaFilters(t *testing.T) {
	h, _, lister := newEnv()

	status, _ := do(t, h, http.MethodGet, QuotasPath+"?scope=api_key&scope_id=12", "token")
	if status != http.StatusOK {
		t.Fatalf("应 200: %d", status)
	}
	if lister.lastScope != billing.ScopeAPIKey || lister.lastScopeID != 12 {
		t.Fatalf("范围过滤 = %q/%d，期望 api_key/12", lister.lastScope, lister.lastScopeID)
	}

	status, _ = do(t, h, http.MethodGet, QuotasPath+"?account_id=5", "token")
	if status != http.StatusOK {
		t.Fatalf("应 200: %d", status)
	}
	if lister.lastScope != billing.ScopeAccount || lister.lastScopeID != 5 {
		t.Fatalf("账户简写 = %q/%d，期望 account/5", lister.lastScope, lister.lastScopeID)
	}

	// scope 与 scope_id 必须成对：只给 scope 时报的是缺另一半，而不是把空 id 当成过滤值。
	status, env := do(t, h, http.MethodGet, QuotasPath+"?scope=account", "token")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("只给 scope 应 400: %d %s", status, env)
	}

	status, env = do(t, h, http.MethodGet, QuotasPath+"?scope=nope&scope_id=1", "token")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("非法 scope 应 400: %d %s", status, env)
	}
}

// TestUsageFilters 断言账户与起始时刻过滤透传，非法时刻回 400。
func TestUsageFilters(t *testing.T) {
	h, _, lister := newEnv()
	const stamp = "2026-10-09T00:00:00Z"
	status, _ := do(t, h, http.MethodGet, UsagePath+"?account_id=5&since="+stamp, "token")
	if status != http.StatusOK {
		t.Fatalf("应 200: %d", status)
	}
	want, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("解析夹具时刻: %v", err)
	}
	if lister.lastAccountID != 5 || !lister.lastSince.Equal(want) {
		t.Fatalf("过滤 = %d/%s，期望 5/%s", lister.lastAccountID, lister.lastSince, want)
	}

	status, env := do(t, h, http.MethodGet, UsagePath+"?since=昨天", "token")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("非法 since 应 400: %d %s", status, env)
	}
}

// TestAdjustmentFilters 断言调账按账户过滤透传。
func TestAdjustmentFilters(t *testing.T) {
	h, _, lister := newEnv()
	status, _ := do(t, h, http.MethodGet, AdjustmentsPath+"?account_id=5", "token")
	if status != http.StatusOK {
		t.Fatalf("应 200: %d", status)
	}
	if lister.lastAccountID != 5 {
		t.Fatalf("账户过滤 = %d，期望 5", lister.lastAccountID)
	}
}

// TestListErrorIsInternal 断言业务读取失败回 500，不把底层错误文案带出去。
func TestListErrorIsInternal(t *testing.T) {
	h, _, lister := newEnv()
	lister.err = errors.New("store: 查询 billing_usage 失败: table doesn't exist")
	status, env := do(t, h, http.MethodGet, UsagePath, "token")
	if status != http.StatusInternalServerError || codeOf(t, env) != webapi.CodeInternal {
		t.Fatalf("应 500: %d %s", status, env)
	}
	var message string
	if err := json.Unmarshal(env["message"], &message); err != nil {
		t.Fatalf("解析 message: %v", err)
	}
	if strings.Contains(message, "table doesn't exist") {
		t.Fatalf("错误文案泄漏了底层细节: %q", message)
	}
}
