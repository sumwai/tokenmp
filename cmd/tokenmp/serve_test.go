package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/adapters/anthropic"
	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/adapters/openairesponses"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/quota"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
)

// testAPIKey 是测试客户端的明文密钥；库中存的是它的 SHA-256 hex。
const testAPIKey = "test-client-key"

// recordedUsage 是一条已落库流水在测试里的视图，占位与结算两条写入路径共用。
type recordedUsage struct {
	MerchantID      uint64
	AccountID       uint64
	ChannelID       uint64
	APIKeyID        uint64
	Model           string
	Usage           map[billing.Metric]int
	PricingID       uint64
	PricingSnapshot []byte
	GrossAmount     string
	Multiplier      string
	Settlement      []byte
}

// scopeKey 是规则池的键：scope 与实体 id。
type scopeKey struct {
	scope billing.Scope
	id    uint64
}

// fakeGatewayStore 是 gatewayStore 的内存替身。
//
// wantMerchantID 非零时只在该商家下返回候选与凭据：鉴权没把商家带进上下文时，
// 端到端用例会因无候选而失败，这比另建一处断言的覆盖更直接。
type fakeGatewayStore struct {
	auth *store.APIKeyAuth
	// authByHash 非 nil 时按密钥哈希返回各自的鉴权结果，用于「同账户多把 key」的用例；
	// 为 nil 时统一返回 auth。
	authByHash map[string]*store.APIKeyAuth
	// routes 是同协议候选，RouteCandidates 返回它。
	routes []store.RouteCandidate
	// crossRoutes 是仅由不限协议查询返回的候选，供跨协议降级用例注入。
	// 真实存储层的不限协议查询也包含 routes（它不限定 type），替身这里分开列出，
	// 由 resolver 的 routeChain 按渠道 id 去重，与生产路径同形。
	crossRoutes    []store.RouteCandidate
	credentials    []store.Credential
	wantMerchantID uint64

	// 结算数据：零值表示「无定价、无规则、无渠道加成」。
	pricing           *settlement.Pricing
	rules             map[scopeKey][]settlement.Rule
	dayKinds          map[string]billing.DayKind
	accountMultiplier decimal.Decimal
	channelMultiplier decimal.Decimal
	buckets           []settlement.Bucket

	// 窗口限额：quotas 是账户维度的限额定义，quotaUsed 按限额 id 给出已用量。
	quotas    []quota.Limit
	quotaUsed map[uint64]decimal.Decimal
	quotaErr  error

	// mu 保护用量记录与账本：流式请求下结算在服务端 goroutine 里被调用。
	mu sync.Mutex
	// usageRows 按写入顺序保存流水行，供端到端用例断言分量。
	usageRows []recordedUsage
	// insertErr 非 nil 时写入返回它，用于验证落库失败不影响转发。
	insertErr error
}

func (f *fakeGatewayStore) LookupAPIKey(_ context.Context, keyHash string, _ time.Time) (*store.APIKeyAuth, error) {
	if f.authByHash != nil {
		if auth, ok := f.authByHash[keyHash]; ok {
			return auth, nil
		}
		return nil, fmt.Errorf("store: 查询 account_api_key 失败: %w", sql.ErrNoRows)
	}
	if keyHash != hashAPIKey(testAPIKey) {
		return nil, fmt.Errorf("store: 查询 account_api_key 失败: %w", sql.ErrNoRows)
	}
	return f.auth, nil
}

func (f *fakeGatewayStore) RouteCandidates(_ context.Context, _ store.ChannelType, _ string, merchantID uint64) ([]store.RouteCandidate, error) {
	if f.wantMerchantID != 0 && merchantID != f.wantMerchantID {
		return nil, nil
	}
	return f.routes, nil
}

// RouteCandidatesAnyType 返回同协议候选与跨协议候选的并集，模拟不限协议查询。
func (f *fakeGatewayStore) RouteCandidatesAnyType(_ context.Context, _ string, merchantID uint64) ([]store.RouteCandidate, error) {
	if f.wantMerchantID != 0 && merchantID != f.wantMerchantID {
		return nil, nil
	}
	combined := make([]store.RouteCandidate, 0, len(f.routes)+len(f.crossRoutes))
	combined = append(combined, f.routes...)
	combined = append(combined, f.crossRoutes...)
	return combined, nil
}

func (f *fakeGatewayStore) CredentialsByGroup(_ context.Context, _ string, merchantID uint64) ([]store.Credential, error) {
	if f.wantMerchantID != 0 && merchantID != f.wantMerchantID {
		return nil, nil
	}
	return f.credentials, nil
}

// InsertUsage 落占位口径流水（结算失败时的回退路径）。
func (f *fakeGatewayStore) InsertUsage(_ context.Context, row store.UsageRow) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appendUsageLocked(recordedUsage{
		MerchantID: row.MerchantID, AccountID: row.AccountID, ChannelID: row.ChannelID,
		APIKeyID: row.APIKeyID, Model: row.Model, Usage: row.Usage, Multiplier: "1",
	})
}

// appendUsageLocked 在已持锁的前提下追加一行流水。
func (f *fakeGatewayStore) appendUsageLocked(row recordedUsage) (uint64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	f.usageRows = append(f.usageRows, row)
	return uint64(len(f.usageRows)), nil
}

// AccountBuckets 实现转发前的额度预检读取。
func (f *fakeGatewayStore) AccountBuckets(_ context.Context, _ uint64) ([]settlement.Bucket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]settlement.Bucket(nil), f.effectiveBucketsLocked()...), nil
}

// Quotas 实现窗口限额定义的读取；只返回请求 scope 与实体下的行。
func (f *fakeGatewayStore) Quotas(_ context.Context, scope billing.Scope, scopeID uint64) ([]quota.Limit, error) {
	if f.quotaErr != nil {
		return nil, f.quotaErr
	}
	var limits []quota.Limit
	for _, limit := range f.quotas {
		if limit.Scope == scope && limit.ScopeID == scopeID {
			limits = append(limits, limit)
		}
	}
	return limits, nil
}

// Usage 实现窗口用量聚合；按限额 id 给出固定的已用量。
func (f *fakeGatewayStore) Usage(_ context.Context, q quota.UsageQuery) (decimal.Decimal, error) {
	if f.quotaErr != nil {
		return decimal.Zero, f.quotaErr
	}
	return f.quotaUsed[q.QuotaID], nil
}

// InTx 模拟一段事务：持锁期间读写同一份账本副本，失败时丢弃改动。
func (f *fakeGatewayStore) InTx(ctx context.Context, fn func(context.Context, settlement.Tx) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	tx := &fakeGatewayTx{store: f, buckets: append([]settlement.Bucket(nil), f.effectiveBucketsLocked()...)}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	f.buckets = tx.buckets
	f.usageRows = append(f.usageRows, tx.inserted...)
	return nil
}

// effectiveBucketsLocked 返回当前账本；未显式配置时给一笔可透支的货币账本，
// 使既有无结算用例不受 402 预检影响。
func (f *fakeGatewayStore) effectiveBucketsLocked() []settlement.Bucket {
	if f.buckets == nil {
		return []settlement.Bucket{{
			ID: 1, Unit: billing.UnitSettleCurrency,
			Remaining: decimal.NewFromInt(1000), Fallback: billing.FallbackChargeBalance,
		}}
	}
	return f.buckets
}

// fakeGatewayTx 在事务内读写 fakeGatewayStore 的账本副本。
type fakeGatewayTx struct {
	store    *fakeGatewayStore
	buckets  []settlement.Bucket
	inserted []recordedUsage
}

func (t *fakeGatewayTx) LockAccount(context.Context, uint64) (decimal.Decimal, error) {
	if t.store.accountMultiplier.IsZero() {
		return decimal.NewFromInt(1), nil
	}
	return t.store.accountMultiplier, nil
}

func (t *fakeGatewayTx) ActivePricing(context.Context, uint64, string, time.Time) (*settlement.Pricing, error) {
	if t.store.pricing == nil {
		return nil, settlement.ErrNoPricing
	}
	return t.store.pricing, nil
}

func (t *fakeGatewayTx) PriceRulesByScope(_ context.Context, scope billing.Scope, scopeID uint64) ([]settlement.Rule, error) {
	return t.store.rules[scopeKey{scope: scope, id: scopeID}], nil
}

func (t *fakeGatewayTx) ModelMapMultiplier(context.Context, uint64, string) (decimal.Decimal, error) {
	if t.store.channelMultiplier.IsZero() {
		return decimal.NewFromInt(1), nil
	}
	return t.store.channelMultiplier, nil
}

func (t *fakeGatewayTx) CalendarDay(_ context.Context, calendar, _ string) (billing.DayKind, bool, error) {
	kind, ok := t.store.dayKinds[calendar]
	return kind, ok, nil
}

func (t *fakeGatewayTx) LockBuckets(context.Context, uint64) ([]settlement.Bucket, error) {
	return append([]settlement.Bucket(nil), t.buckets...), nil
}

func (t *fakeGatewayTx) InsertUsage(_ context.Context, row settlement.Usage) (uint64, error) {
	if t.store.insertErr != nil {
		return 0, t.store.insertErr
	}
	t.inserted = append(t.inserted, recordedUsage{
		MerchantID: row.MerchantID, AccountID: row.AccountID, ChannelID: row.ChannelID,
		APIKeyID: row.APIKeyID, Model: row.Model, Usage: row.Metrics, PricingID: row.PricingID,
		PricingSnapshot: row.PricingSnapshot, GrossAmount: row.GrossAmount.String(),
		Multiplier: row.Multiplier.String(), Settlement: row.Settlement,
	})
	return uint64(len(t.inserted)), nil
}

func (t *fakeGatewayTx) UpdateBucketRemaining(_ context.Context, bucketID uint64, remaining decimal.Decimal) error {
	for i := range t.buckets {
		if t.buckets[i].ID == bucketID {
			t.buckets[i].Remaining = remaining
			return nil
		}
	}
	return fmt.Errorf("账本 %d 不存在", bucketID)
}

// usageSnapshot 返回已写入流水行的一份拷贝。
func (f *fakeGatewayStore) usageSnapshot() []recordedUsage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedUsage(nil), f.usageRows...)
}

// activeAuth 返回一份鉴权通过的最小事实。
func activeAuth() *store.APIKeyAuth {
	return &store.APIKeyAuth{APIKeyID: 1, AccountID: 2, AccountStatus: accountStatusActive, MerchantID: 1}
}

// newTestGateway 装配网关；装配失败即让用例失败。
func newTestGateway(t *testing.T, st gatewayStore) *gateway {
	t.Helper()
	gw, err := newGateway(st, gatewayOptions{CompleteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("装配网关失败：%v", err)
	}
	t.Cleanup(gw.Close)
	return gw
}

// httpResult 是一次客户端调用的结果快照。
//
// 响应体在这里就被读完并关闭，不把 *http.Response 交给用例：
// 让用例拿着未关闭的响应体是 bodyclose 一类泄漏的常见形态。
type httpResult struct {
	status      int
	contentType string
	headers     http.Header
	body        []byte
}

// doPost 向网关发一次 POST，返回结果快照。
func doPost(t *testing.T, url, authorization, body string) httpResult {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set(authorizationHeader, authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("发送请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	return httpResult{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), headers: resp.Header.Clone(), body: respBody}
}

// assertJSONError 断言响应是期望状态码的 JSON，且带统一错误体的 error 字段。
func assertJSONError(t *testing.T, result httpResult, wantStatus int) {
	t.Helper()
	if result.status != wantStatus {
		t.Fatalf("状态码 = %d，期望 %d，响应体 %s", result.status, wantStatus, result.body)
	}
	if !strings.HasPrefix(result.contentType, jsonContentType) {
		t.Errorf("Content-Type = %q，期望 %q 前缀", result.contentType, jsonContentType)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result.body, &envelope); err != nil {
		t.Fatalf("响应体不是 JSON：%v，原文 %s", err, result.body)
	}
	if _, ok := envelope["error"]; !ok {
		t.Errorf("响应体缺少 error 字段：%s", result.body)
	}
}

// recordedUpstream 是模拟上游收到的一次请求的快照。
type recordedUpstream struct {
	path    string
	headers http.Header
	model   string
	// includeUsage 记录上游请求体是否带了用量索取开关。
	includeUsage bool
}

// TestGatewayForwardingThreeDialects 覆盖三方言的非流式端到端往返。
//
// 每条用例断言四件事：上游收到的是拼好的端点段、模型名已替换为 upstream_model、
// 凭据头按方言注入、上游响应逐字节回写给客户端。
func TestGatewayForwardingThreeDialects(t *testing.T) {
	tests := []struct {
		name         string
		protocol     domain.Protocol
		requestBody  string
		responseBody string
		wantHeader   string
		wantValue    string
	}{
		{
			name:         "openai_chat",
			protocol:     domain.ProtocolOpenAIChat,
			requestBody:  `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`,
			responseBody: `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantHeader:   "Authorization",
			wantValue:    "Bearer sk-upstream",
		},
		{
			name:         "openai_responses",
			protocol:     domain.ProtocolOpenAIResponses,
			requestBody:  `{"model":"alias","input":"hi"}`,
			responseBody: `{"id":"resp_1","model":"up-model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`,
			wantHeader:   "Authorization",
			wantValue:    "Bearer sk-upstream",
		},
		{
			name:         "anthropic_messages",
			protocol:     domain.ProtocolAnthropicMessages,
			requestBody:  `{"model":"alias","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			responseBody: `{"id":"msg_1","type":"message","role":"assistant","model":"up-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`,
			wantHeader:   "x-api-key",
			wantValue:    "sk-upstream",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := make(chan recordedUpstream, 1)
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var fields map[string]any
				_ = json.Unmarshal(raw, &fields)
				model, _ := fields["model"].(string)
				records <- recordedUpstream{path: r.URL.Path, headers: r.Header.Clone(), model: model}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tt.responseBody)
			}))
			defer upstreamServer.Close()

			st := &fakeGatewayStore{
				auth: activeAuth(),
				routes: []store.RouteCandidate{{
					ChannelID: 10, BaseURL: upstreamServer.URL, CredGroup: "group-a", UpstreamModel: "up-model",
				}},
				credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
				wantMerchantID: 1,
			}
			gatewayServer := httptest.NewServer(newTestGateway(t, st).handler)
			defer gatewayServer.Close()

			result := doPost(t, gatewayServer.URL+tt.protocol.EndpointPath(), authSchemePrefix+testAPIKey, tt.requestBody)
			if result.status != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
			}
			if string(result.body) != tt.responseBody {
				t.Errorf("响应体应逐字节透传上游：\n实际 %s\n期望 %s", result.body, tt.responseBody)
			}

			recorded := <-records
			if recorded.path != tt.protocol.EndpointSegment() {
				t.Errorf("上游路径 = %q，期望 %q", recorded.path, tt.protocol.EndpointSegment())
			}
			if recorded.model != "up-model" {
				t.Errorf("上游模型名 = %q，期望 up-model", recorded.model)
			}
			if got := recorded.headers.Get(tt.wantHeader); got != tt.wantValue {
				t.Errorf("%s = %q，期望 %q", tt.wantHeader, got, tt.wantValue)
			}
			if tt.protocol == domain.ProtocolAnthropicMessages {
				if got := recorded.headers.Get("anthropic-version"); got != "2023-06-01" {
					t.Errorf("anthropic-version = %q，期望 2023-06-01", got)
				}
				if got := recorded.headers.Get("Authorization"); got != "" {
					t.Errorf("anthropic 不应带 Authorization，实际 %q", got)
				}
			} else if got := recorded.headers.Get("x-api-key"); got != "" {
				t.Errorf("openai 不应带 x-api-key，实际 %q", got)
			}
		})
	}
}

// TestGatewayRotatesCredentialOnAuthFailure 覆盖「第一把 key 401、第二把成功」的凭据切换。
//
// 断言三件事：上游按序收到两把 key、客户端拿到正常响应、billing_usage 只产生一行成功流水。
func TestGatewayRotatesCredentialOnAuthFailure(t *testing.T) {
	const successBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	var (
		mu      sync.Mutex
		gotKeys []string
	)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		gotKeys = append(gotKeys, key)
		mu.Unlock()
		w.Header().Set("Content-Type", jsonContentType)
		if key != "sk-second" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"invalid api key"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, successBody)
	}))
	defer upstreamServer.Close()

	st := &fakeGatewayStore{
		auth: activeAuth(),
		routes: []store.RouteCandidate{{
			ChannelID: 10, BaseURL: upstreamServer.URL, CredGroup: "group-a", UpstreamModel: "up-model",
		}},
		credentials: []store.Credential{
			{Name: "old", Secret: []byte(`{"api_key":"sk-first"}`)},
			{Name: "new", Secret: []byte(`{"api_key":"sk-second"}`)},
		},
		wantMerchantID: 1,
	}
	server := httptest.NewServer(newTestGateway(t, st).handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if string(result.body) != successBody {
		t.Errorf("客户端应拿到成功的上游响应：\n实际 %s\n期望 %s", result.body, successBody)
	}

	mu.Lock()
	keys := append([]string(nil), gotKeys...)
	mu.Unlock()
	if want := []string{"sk-first", "sk-second"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("上游收到的 key = %v，期望 %v", keys, want)
	}
	if rows := st.usageSnapshot(); len(rows) != 1 {
		t.Fatalf("billing_usage 行数 = %d，期望 1", len(rows))
	}
}

// TestNewUpstreamTransportUsesConfiguredPool 断言连接池参数来自配置而非常量。
func TestNewUpstreamTransportUsesConfiguredPool(t *testing.T) {
	transport := newUpstreamTransport(gatewayOptions{
		UpstreamMaxIdleConns:        100,
		UpstreamMaxIdleConnsPerHost: 32,
		UpstreamIdleConnTimeout:     90 * time.Second,
	})
	if transport.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d，期望 100", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 32 {
		t.Errorf("MaxIdleConnsPerHost = %d，期望 32", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %s，期望 90s", transport.IdleConnTimeout)
	}
}

// TestGatewayHealthz 断言健康检查不鉴权且固定 200。
func TestGatewayHealthz(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{}).handler)
	defer server.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+healthzPath, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求健康检查失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", resp.StatusCode)
	}
}

// TestGatewayAuthFailureIsJSON 覆盖鉴权失败一律回 JSON 401。
func TestGatewayAuthFailureIsJSON(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	validBody := `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`
	tests := []struct {
		name          string
		authorization string
	}{
		{name: "缺少 Authorization 头"},
		{name: "非法方案", authorization: "Basic abc"},
		{name: "未知密钥", authorization: authSchemePrefix + "wrong-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(), tt.authorization, validBody)
			assertJSONError(t, result, http.StatusUnauthorized)
		})
	}
}

// TestGatewayMethodNotAllowedIsJSON 覆盖已注册路径上的非 POST 方法回 JSON 405。
func TestGatewayMethodNotAllowedIsJSON(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+domain.ProtocolOpenAIChat.EndpointPath(), nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set(authorizationHeader, authSchemePrefix+testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("发送请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	assertJSONError(t, httpResult{
		status:      resp.StatusCode,
		contentType: resp.Header.Get("Content-Type"),
		body:        body,
	}, http.StatusMethodNotAllowed)
}

// TestGatewayNoRouteIsJSON 覆盖无候选渠道时回 JSON 404。
func TestGatewayNoRouteIsJSON(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	assertJSONError(t, result, http.StatusNotFound)
}

// TestGatewayUpstreamUnreachableIsJSON 覆盖上游不可达时回 JSON 502。
func TestGatewayUpstreamUnreachableIsJSON(t *testing.T) {
	// 先起一个上游再关掉：地址保持有效，连接被拒。
	deadUpstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadUpstream.Close()

	st := &fakeGatewayStore{
		auth: activeAuth(),
		routes: []store.RouteCandidate{{
			ChannelID: 10, BaseURL: deadUpstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
		}},
		credentials: []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
	}
	server := httptest.NewServer(newTestGateway(t, st).handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	assertJSONError(t, result, http.StatusBadGateway)
}

// TestGatewayNonStreamTimeoutIsJSON504 覆盖非流式整体超时回 JSON 504。
//
// 上游一直不返回，handler 侧 deadline 到时取消上游调用，错误按上游超时编码。
func TestGatewayNonStreamTimeoutIsJSON504(t *testing.T) {
	slowUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer slowUpstream.Close()

	st := &fakeGatewayStore{
		auth: activeAuth(),
		routes: []store.RouteCandidate{{
			ChannelID: 10, BaseURL: slowUpstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
		}},
		credentials: []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
	}
	gw, err := newGateway(st, gatewayOptions{CompleteTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("装配网关失败：%v", err)
	}
	defer gw.Close()
	server := httptest.NewServer(gw.handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	assertJSONError(t, result, http.StatusGatewayTimeout)
}

// TestGatewayUnregisteredPathIsJSON 覆盖未注册路径回 JSON 404 而不是标准库的纯文本。
func TestGatewayUnregisteredPathIsJSON(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	result := doPost(t, server.URL+"/v1/unknown", authSchemePrefix+testAPIKey, "{}")
	assertJSONError(t, result, http.StatusNotFound)
}

// attemptLogLines 解析尝试日志缓冲区为若干条 JSON 字段映射。
//
// 只断言尝试日志时装配层不注入访问日志与凭据日志，因此缓冲区里应当只有尝试行。
func attemptLogLines(t *testing.T, buf *bytes.Buffer) []map[string]json.RawMessage {
	t.Helper()
	var lines []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("尝试日志不是 JSON：%v，原文 %s", err, line)
		}
		lines = append(lines, fields)
	}
	return lines
}

// TestGatewayLogsAttemptsAcrossChannelFallback 覆盖换渠道重试的尝试级观测。
//
// 第一条候选返回 503（可重试），流水线退避后换第二条候选成功。断言尝试日志恰好两行、
// 共享同一 request id、渠道 id 分别为两条候选，且第二行标记为换渠道重试。
func TestGatewayLogsAttemptsAcrossChannelFallback(t *testing.T) {
	failUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"boom"}}`)
	}))
	defer failUpstream.Close()

	const successBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	okUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, successBody)
	}))
	defer okUpstream.Close()

	st := &fakeGatewayStore{
		auth: activeAuth(),
		// 优先级不同即无需随机：高优先级的失败渠道先试，退避后换低优先级渠道。
		routes: []store.RouteCandidate{
			{ChannelID: 10, BaseURL: failUpstream.URL, CredGroup: "group-a", UpstreamModel: "up-model", Priority: 10},
			{ChannelID: 5, BaseURL: okUpstream.URL, CredGroup: "group-a", UpstreamModel: "up-model", Priority: 5},
		},
		credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		wantMerchantID: 1,
	}
	var buf bytes.Buffer
	gw, err := newGateway(st, gatewayOptions{
		CompleteTimeout: 5 * time.Second,
		Observer:        newAttemptObserver(newJSONLogger(&buf)),
	})
	if err != nil {
		t.Fatalf("装配网关失败：%v", err)
	}
	defer gw.Close()
	server := httptest.NewServer(gw.handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}

	lines := attemptLogLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("尝试日志行数 = %d，期望 2：%s", len(lines), buf.String())
	}
	requestIDs := make([]string, len(lines))
	for i, line := range lines {
		if err := json.Unmarshal(line["request_id"], &requestIDs[i]); err != nil {
			t.Fatalf("解析 request_id 失败：%v，原文 %s", err, line["request_id"])
		}
	}
	if requestIDs[0] == "" || requestIDs[0] != requestIDs[1] {
		t.Errorf("两次尝试应共享同一 request id，实际 %v", requestIDs)
	}
	wantPerLine := []map[string]string{
		{
			"channel_id":       `10`,
			"retry":            `false`,
			"channel_switched": `false`,
			"upstream_status":  `503`,
		},
		{
			"channel_id":       `5`,
			"retry":            `true`,
			"channel_switched": `true`,
			"upstream_status":  `200`,
		},
	}
	for i, want := range wantPerLine {
		for key, value := range want {
			if got := string(lines[i][key]); got != value {
				t.Errorf("第 %d 行 %s = %s，期望 %s", i+1, key, got, value)
			}
		}
	}
}

// TestGatewayAttemptObserverFailureDoesNotBreakForwarding 守护「观测失败不影响转发」：
// 观测器返回错误时，客户端仍拿到正常响应。
func TestGatewayAttemptObserverFailureDoesNotBreakForwarding(t *testing.T) {
	const successBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, successBody)
	}))
	defer upstream.Close()

	st := &fakeGatewayStore{
		auth: activeAuth(),
		routes: []store.RouteCandidate{{
			ChannelID: 10, BaseURL: upstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
		}},
		credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		wantMerchantID: 1,
	}
	gw, err := newGateway(st, gatewayOptions{
		CompleteTimeout: 5 * time.Second,
		Observer:        failingObserver{},
	})
	if err != nil {
		t.Fatalf("装配网关失败：%v", err)
	}
	defer gw.Close()
	server := httptest.NewServer(gw.handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if string(result.body) != successBody {
		t.Errorf("观测失败不得改变对客户端的响应：\n实际 %s\n期望 %s", result.body, successBody)
	}
}

// TestRunServerGracefulShutdown 覆盖 ctx 取消后 runServer 优雅关闭并返回。
//
// 用临时端口而不是固定端口：并发跑测试或本机已有服务占用端口时不会互相干扰。
func TestRunServerGracefulShutdown(t *testing.T) {
	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听临时端口失败：%v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(healthz), ReadHeaderTimeout: readHeaderTimeout}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, server, ln) }()

	waitForOK(t, "http://"+ln.Addr().String()+healthzPath)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("优雅关闭返回错误：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServer 在取消后未退出")
	}
}

// waitForOK 轮询 url 直到返回 200 或超时。
func waitForOK(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("构造请求失败：%v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 %s 就绪超时", url)
}

// TestGatewayPaymentRequiredPrecheck 覆盖 402 预检的各分支。
//
// 预检只防「彻底没钱」：从未充值或只剩耗尽的 reject 包时回 402；
// 存在可透支的 currency 账本时放行，请求照常转发。
func TestGatewayPaymentRequiredPrecheck(t *testing.T) {
	const responseBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	tests := []struct {
		name       string
		buckets    []settlement.Bucket
		wantStatus int
	}{
		{
			name:       "从未充值",
			buckets:    []settlement.Bucket{},
			wantStatus: http.StatusPaymentRequired,
		},
		{
			name: "只剩耗尽的 reject 包",
			buckets: []settlement.Bucket{{
				ID: 1, Unit: billing.UnitSettleToken, Remaining: decimal.Zero, Fallback: billing.FallbackReject,
			}},
			wantStatus: http.StatusPaymentRequired,
		},
		{
			name: "currency 可透支",
			buckets: []settlement.Bucket{{
				ID: 1, Unit: billing.UnitSettleCurrency, Remaining: decimal.Zero, Fallback: billing.FallbackChargeBalance,
			}},
			wantStatus: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", jsonContentType)
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, responseBody)
			}))
			t.Cleanup(upstream.Close)

			st := &fakeGatewayStore{
				auth:        activeAuth(),
				routes:      []store.RouteCandidate{{ChannelID: 10, BaseURL: upstream.URL, CredGroup: "group-a", UpstreamModel: "up-model"}},
				credentials: []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
				buckets:     tt.buckets,
			}
			server := httptest.NewServer(newTestGateway(t, st).handler)
			t.Cleanup(server.Close)

			result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
				authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
			if result.status != tt.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d，响应体 %s", result.status, tt.wantStatus, result.body)
			}
			if tt.wantStatus == http.StatusPaymentRequired {
				assertJSONError(t, result, http.StatusPaymentRequired)
			}
		})
	}
}

// TestGatewayQuotaEnforcement 覆盖窗口限额的端到端响应：
//
//   - reject 超限回 429 且错误码为 quota_exceeded；
//   - throttle 超限回 429 rate_limited 并附 Retry-After；
//   - 未超限、未配置限额、聚合失败都放行；
//   - 已用量回落（等价于 quota reset 后）重新放行。
func TestGatewayQuotaEnforcement(t *testing.T) {
	const responseBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(upstream.Close)

	dailyQuota := func(action billing.Action) quota.Limit {
		return quota.Limit{
			ID: 1, Scope: billing.ScopeAccount, ScopeID: 2, Metric: billing.MetricRequest,
			WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay,
			LimitAmount: decimal.NewFromInt(100), Action: action,
		}
	}
	tests := []struct {
		name       string
		quotas     []quota.Limit
		used       map[uint64]decimal.Decimal
		quotaErr   error
		wantStatus int
		wantCode   string
		wantRetry  bool
	}{
		{
			name:   "未超限放行",
			quotas: []quota.Limit{dailyQuota(billing.ActionReject)},
			used:   map[uint64]decimal.Decimal{1: decimal.NewFromInt(99)}, wantStatus: http.StatusOK,
		},
		{name: "未配置限额放行", wantStatus: http.StatusOK},
		{
			name:       "reject 超限",
			quotas:     []quota.Limit{dailyQuota(billing.ActionReject)},
			used:       map[uint64]decimal.Decimal{1: decimal.NewFromInt(100)},
			wantStatus: http.StatusTooManyRequests, wantCode: string(domain.CodeQuotaExceeded),
		},
		{
			name:       "throttle 超限",
			quotas:     []quota.Limit{dailyQuota(billing.ActionThrottle)},
			used:       map[uint64]decimal.Decimal{1: decimal.NewFromInt(150)},
			wantStatus: http.StatusTooManyRequests, wantCode: string(domain.CodeRateLimited), wantRetry: true,
		},
		{
			name:     "聚合失败放行",
			quotas:   []quota.Limit{dailyQuota(billing.ActionReject)},
			quotaErr: fmt.Errorf("聚合超时"), wantStatus: http.StatusOK,
		},
		{
			name:   "重置后已用量回落放行",
			quotas: []quota.Limit{dailyQuota(billing.ActionReject)},
			used:   map[uint64]decimal.Decimal{1: decimal.Zero}, wantStatus: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeGatewayStore{
				auth:        activeAuth(),
				routes:      []store.RouteCandidate{{ChannelID: 10, BaseURL: upstream.URL, CredGroup: "group-a", UpstreamModel: "up-model"}},
				credentials: []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
				quotas:      tt.quotas,
				quotaUsed:   tt.used,
				quotaErr:    tt.quotaErr,
			}
			server := httptest.NewServer(newTestGateway(t, st).handler)
			t.Cleanup(server.Close)

			result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
				authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
			if result.status != tt.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d，响应体 %s", result.status, tt.wantStatus, result.body)
			}
			if tt.wantStatus == http.StatusOK {
				return
			}
			assertJSONError(t, result, tt.wantStatus)
			var envelope errorEnvelope
			if err := json.Unmarshal(result.body, &envelope); err != nil {
				t.Fatalf("解析错误体失败：%v", err)
			}
			if envelope.Error.Code != tt.wantCode {
				t.Errorf("错误码 = %q，期望 %q", envelope.Error.Code, tt.wantCode)
			}
			retryAfter := result.headers.Get(retryAfterHeader)
			if tt.wantRetry && retryAfter == "" {
				t.Error("throttle 响应应带 Retry-After")
			}
			if !tt.wantRetry && retryAfter != "" {
				t.Errorf("reject 响应不应带 Retry-After，得到 %q", retryAfter)
			}
		})
	}
}

// TestGatewayQuotaEnforcementByAPIKey 覆盖 key 维度的端到端：同一账户下的两把 key，
// key A 设了日限额且已超限，key B 未设限。A 收到 429，B 正常转发。
//
// 同时断言落库流水带上各自 key 的主键：限额判定依赖这一列。
func TestGatewayQuotaEnforcementByAPIKey(t *testing.T) {
	const responseBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(upstream.Close)

	const (
		keyA   = "test-api-key-a"
		keyB   = "test-api-key-b"
		keyAID = 11
		keyBID = 22
	)
	// 两把 key 归属同一账户与商家，区分只在 api_key_id。
	st := &fakeGatewayStore{
		authByHash: map[string]*store.APIKeyAuth{
			hashAPIKey(keyA): {APIKeyID: keyAID, AccountID: 2, AccountStatus: accountStatusActive, MerchantID: 1},
			hashAPIKey(keyB): {APIKeyID: keyBID, AccountID: 2, AccountStatus: accountStatusActive, MerchantID: 1},
		},
		routes:      []store.RouteCandidate{{ChannelID: 10, BaseURL: upstream.URL, CredGroup: "group-a", UpstreamModel: "up-model"}},
		credentials: []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		// 只给 key A 设了达到上限的日限额。
		quotas: []quota.Limit{{
			ID: 1, Scope: billing.ScopeAPIKey, ScopeID: keyAID, Metric: billing.MetricRequest,
			WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay,
			LimitAmount: decimal.NewFromInt(100), Action: billing.ActionReject,
		}},
		quotaUsed: map[uint64]decimal.Decimal{1: decimal.NewFromInt(100)},
	}
	server := httptest.NewServer(newTestGateway(t, st).handler)
	t.Cleanup(server.Close)

	body := `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`
	endpoint := server.URL + domain.ProtocolOpenAIChat.EndpointPath()

	blocked := doPost(t, endpoint, authSchemePrefix+keyA, body)
	assertJSONError(t, blocked, http.StatusTooManyRequests)
	var envelope errorEnvelope
	if err := json.Unmarshal(blocked.body, &envelope); err != nil {
		t.Fatalf("解析错误体失败：%v", err)
	}
	if envelope.Error.Code != string(domain.CodeQuotaExceeded) {
		t.Errorf("错误码 = %q，期望 %q", envelope.Error.Code, domain.CodeQuotaExceeded)
	}

	allowed := doPost(t, endpoint, authSchemePrefix+keyB, body)
	if allowed.status != http.StatusOK {
		t.Fatalf("未被限额的 key 状态码 = %d，期望 200，响应体 %s", allowed.status, allowed.body)
	}

	rows := st.usageSnapshot()
	if len(rows) != 1 {
		t.Fatalf("流水行数 = %d，期望 1（被拒的请求不落库）", len(rows))
	}
	if rows[0].APIKeyID != keyBID {
		t.Errorf("api_key_id = %d，期望 %d", rows[0].APIKeyID, keyBID)
	}
}

// TestGatewaySettlementEndToEnd 覆盖结算端到端：定价解析、倍率链、扣减与流水结算字段。
//
// 数值可复算：gross = 1000×0.27/1e6 + 500×1.10/1e6 = 0.00082；
// 倍率 = 账户 2 × 渠道 1.5 × 规则 0.5 = 1.5；扣减 = 0.00082 × 1.5 = 0.00123。
func TestGatewaySettlementEndToEnd(t *testing.T) {
	const responseBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":1000,"completion_tokens":500}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(upstream.Close)

	st := &fakeGatewayStore{
		auth:        activeAuth(),
		routes:      []store.RouteCandidate{{ChannelID: 10, BaseURL: upstream.URL, CredGroup: "group-a", UpstreamModel: "up-model"}},
		credentials: []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		pricing: &settlement.Pricing{ID: 7, Version: 3, Components: []settlement.Component{
			{ID: 11, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
				UnitPrice: decimal.RequireFromString("0.27"), BasisQty: decimal.RequireFromString("1000000")},
			{ID: 12, Metric: billing.MetricOutputToken, UnitSettle: billing.UnitSettleCurrency,
				UnitPrice: decimal.RequireFromString("1.10"), BasisQty: decimal.RequireFromString("1000000")},
		}},
		accountMultiplier: decimal.NewFromInt(2),
		channelMultiplier: decimal.RequireFromString("1.5"),
		rules: map[scopeKey][]settlement.Rule{
			{scope: billing.ScopePricing, id: 7}: {{
				ID: 21, Scope: billing.ScopePricing, ScopeID: 7,
				Multiplier: decimal.RequireFromString("0.5"),
			}},
		},
		buckets: []settlement.Bucket{{
			ID: 5, Unit: billing.UnitSettleCurrency,
			Remaining: decimal.RequireFromString("1"), Fallback: billing.FallbackChargeBalance,
		}},
	}
	server := httptest.NewServer(newTestGateway(t, st).handler)
	t.Cleanup(server.Close)

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}

	rows := st.usageSnapshot()
	if len(rows) != 1 {
		t.Fatalf("流水行数 = %d，期望 1", len(rows))
	}
	row := rows[0]
	if row.Model != "up-model" {
		t.Errorf("model = %q，期望履约模型 up-model", row.Model)
	}
	if row.PricingID != 7 {
		t.Errorf("pricing_id = %d，期望 7", row.PricingID)
	}
	if row.GrossAmount != "0.00082" {
		t.Errorf("gross_amount = %s，期望 0.00082", row.GrossAmount)
	}
	if row.Multiplier != "1.5" {
		t.Errorf("multiplier = %s，期望 1.5", row.Multiplier)
	}
	if len(row.PricingSnapshot) == 0 {
		t.Fatal("pricing_snapshot 为空")
	}
	if len(row.Settlement) == 0 {
		t.Fatal("settlement 为空")
	}

	var payload struct {
		Lines []struct {
			BucketID uint64 `json:"bucket_id"`
			Unit     string `json:"unit"`
			Qty      string `json:"qty"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(row.Settlement, &payload); err != nil {
		t.Fatalf("解析 settlement 失败：%v", err)
	}
	if len(payload.Lines) != 1 {
		t.Fatalf("扣减明细 = %#v，期望一行", payload.Lines)
	}
	line := payload.Lines[0]
	if line.BucketID != 5 || line.Unit != string(billing.UnitSettleCurrency) || line.Qty != "0.00123" {
		t.Errorf("扣减明细 = %#v，期望 bucket 5 / currency / 0.00123", line)
	}

	st.mu.Lock()
	remaining := st.buckets[0].Remaining
	st.mu.Unlock()
	if remaining.String() != "0.99877" {
		t.Errorf("账本余量 = %s，期望 0.99877（1 - 0.00123）", remaining)
	}
}

// postForConcurrency 在 goroutine 中发一次转发请求，只回状态码与错误。
//
// 并发用例不能在子 goroutine 里调 t.Fatalf，故不复用 doPost。
func postForConcurrency(url string) (int, error) {
	body := `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authorizationHeader, authSchemePrefix+testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0, err
	}
	return resp.StatusCode, nil
}

// TestGatewayRateLimitTimeoutFallsBackToNextChannel 覆盖限流等待超时走渠道回退。
//
// 等待上限取 1ns：令牌不足时在第一次判定就超时，用例不依赖真实等待。
func TestGatewayRateLimitTimeoutFallsBackToNextChannel(t *testing.T) {
	const successBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`

	var primaryCalls, fallbackCalls int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&primaryCalls, 1)
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, successBody)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&fallbackCalls, 1)
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, successBody)
	}))
	defer fallback.Close()

	st := &fakeGatewayStore{
		auth: activeAuth(),
		// 优先级不同即无需随机：受限渠道先试，超时后换低优先级渠道。
		routes: []store.RouteCandidate{
			{ChannelID: 10, BaseURL: primary.URL, CredGroup: "group-a", UpstreamModel: "up-model", Priority: 10, RateLimitQPS: 1},
			{ChannelID: 5, BaseURL: fallback.URL, CredGroup: "group-a", UpstreamModel: "up-model", Priority: 5},
		},
		credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		wantMerchantID: 1,
	}
	var buf bytes.Buffer
	gw, err := newGateway(st, gatewayOptions{
		CompleteTimeout: 5 * time.Second,
		RateLimitWait:   time.Nanosecond,
		Observer:        newAttemptObserver(newJSONLogger(&buf)),
	})
	if err != nil {
		t.Fatalf("装配网关失败：%v", err)
	}
	defer gw.Close()
	server := httptest.NewServer(gw.handler)
	defer server.Close()

	request := `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`
	endpoint := server.URL + domain.ProtocolOpenAIChat.EndpointPath()
	// 首个请求消耗受限渠道的令牌，走受限渠道成功。
	if first := doPost(t, endpoint, authSchemePrefix+testAPIKey, request); first.status != http.StatusOK {
		t.Fatalf("首个请求状态码 = %d，期望 200，响应体 %s", first.status, first.body)
	}
	// 第二个请求令牌不足，等待超时后换下一条候选。
	second := doPost(t, endpoint, authSchemePrefix+testAPIKey, request)
	if second.status != http.StatusOK {
		t.Fatalf("回退后状态码 = %d，期望 200，响应体 %s", second.status, second.body)
	}
	if got := atomic.LoadInt64(&primaryCalls); got != 1 {
		t.Errorf("受限渠道上游调用 = %d，期望 1（第二次应被限流拦下）", got)
	}
	if got := atomic.LoadInt64(&fallbackCalls); got != 1 {
		t.Errorf("回退渠道上游调用 = %d，期望 1", got)
	}

	lines := attemptLogLines(t, &buf)
	if len(lines) != 3 {
		t.Fatalf("尝试日志行数 = %d，期望 3：%s", len(lines), buf.String())
	}
	var code string
	if err := json.Unmarshal(lines[1]["error_code"], &code); err != nil {
		t.Fatalf("解析 error_code 失败：%v", err)
	}
	if code != string(domain.CodeUpstreamRateLimited) {
		t.Errorf("第二条日志错误码 = %q，期望 %q", code, domain.CodeUpstreamRateLimited)
	}
	if got := string(lines[2]["channel_switched"]); got != "true" {
		t.Errorf("第三条日志 channel_switched = %s，期望 true", got)
	}
}

// TestGatewayBreakerSkipsFailingChannel 覆盖端到端熔断：故障渠道连续 5 次上游 500 后
// 进入打开态，第 6 个请求不再打到它，由健康渠道服务，且被跳过的候选在尝试日志里留下
// skipped 结果。
func TestGatewayBreakerSkipsFailingChannel(t *testing.T) {
	const (
		successBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
		threshold   = 5
	)
	var failingCalls, healthyCalls int64
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&failingCalls, 1)
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer failing.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&healthyCalls, 1)
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, successBody)
	}))
	defer healthy.Close()

	st := &fakeGatewayStore{
		auth: activeAuth(),
		// 优先级不同即无需随机：故障渠道先试，回退到健康渠道。
		routes: []store.RouteCandidate{
			{ChannelID: 10, BaseURL: failing.URL, CredGroup: "group-a", UpstreamModel: "up-model", Priority: 10},
			{ChannelID: 20, BaseURL: healthy.URL, CredGroup: "group-a", UpstreamModel: "up-model", Priority: 5},
		},
		credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		wantMerchantID: 1,
	}
	var buf bytes.Buffer
	gw, err := newGateway(st, gatewayOptions{
		CompleteTimeout:  5 * time.Second,
		BreakerThreshold: threshold,
		BreakerCooldown:  time.Minute,
		Observer:         newAttemptObserver(newJSONLogger(&buf)),
	})
	if err != nil {
		t.Fatalf("装配网关失败：%v", err)
	}
	defer gw.Close()
	server := httptest.NewServer(gw.handler)
	defer server.Close()

	request := `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`
	endpoint := server.URL + domain.ProtocolOpenAIChat.EndpointPath()

	// 前 5 个请求各自在故障渠道上失败一次后回退到健康渠道，客户端无感。
	for i := 0; i < threshold; i++ {
		if result := doPost(t, endpoint, authSchemePrefix+testAPIKey, request); result.status != http.StatusOK {
			t.Fatalf("第 %d 个请求状态码 = %d，期望 200，响应体 %s", i+1, result.status, result.body)
		}
	}
	// 第 6 个请求：故障渠道已打开，直接由健康渠道服务。
	if result := doPost(t, endpoint, authSchemePrefix+testAPIKey, request); result.status != http.StatusOK {
		t.Fatalf("熔断后请求状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if got := atomic.LoadInt64(&failingCalls); got != threshold {
		t.Errorf("故障渠道被调用 = %d 次，期望停在 %d", got, threshold)
	}
	if got := atomic.LoadInt64(&healthyCalls); got != threshold+1 {
		t.Errorf("健康渠道被调用 = %d 次，期望 %d", got, threshold+1)
	}

	skipped := 0
	for _, line := range attemptLogLines(t, &buf) {
		var outcome string
		if err := json.Unmarshal(line["outcome"], &outcome); err != nil {
			t.Fatalf("解析 outcome 失败：%v", err)
		}
		if outcome == string(domain.AttemptSkipped) {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("熔断跳过的尝试日志 = %d 条，期望 1 条：%s", skipped, buf.String())
	}
}

// TestGatewayBreakerProbesWhenAllChannelsOpen 覆盖「全部候选被熔断时放行一个探测」：
// 唯一候选打开后，下一个请求仍会绕过剩余冷却期探测一次，而不是直接返回失败。
func TestGatewayBreakerProbesWhenAllChannelsOpen(t *testing.T) {
	var calls int64
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer failing.Close()

	st := &fakeGatewayStore{
		auth: activeAuth(),
		routes: []store.RouteCandidate{
			{ChannelID: 10, BaseURL: failing.URL, CredGroup: "group-a", UpstreamModel: "up-model"},
		},
		credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		wantMerchantID: 1,
	}
	var buf bytes.Buffer
	gw, err := newGateway(st, gatewayOptions{
		CompleteTimeout:  5 * time.Second,
		BreakerThreshold: 1,
		BreakerCooldown:  time.Minute,
		Observer:         newAttemptObserver(newJSONLogger(&buf)),
	})
	if err != nil {
		t.Fatalf("装配网关失败：%v", err)
	}
	defer gw.Close()
	server := httptest.NewServer(gw.handler)
	defer server.Close()

	request := `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`
	endpoint := server.URL + domain.ProtocolOpenAIChat.EndpointPath()

	// 首个请求把唯一候选打到打开态。
	if result := doPost(t, endpoint, authSchemePrefix+testAPIKey, request); result.status != http.StatusBadGateway {
		t.Fatalf("首个请求状态码 = %d，期望 502，响应体 %s", result.status, result.body)
	}
	// 第二个请求在冷却期未满时仍探测一次，而不是直接回 502「所有候选熔断」。
	if result := doPost(t, endpoint, authSchemePrefix+testAPIKey, request); result.status != http.StatusBadGateway {
		t.Fatalf("探测请求状态码 = %d，期望 502，响应体 %s", result.status, result.body)
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Errorf("故障渠道被调用 = %d 次，期望 2（第 1 次失败打开、第 2 次探测）", got)
	}

	// 第二个请求的尝试日志先记熔断跳过，再记一次真实探测失败。
	lines := attemptLogLines(t, &buf)
	if len(lines) != 3 {
		t.Fatalf("尝试日志行数 = %d，期望 3：%s", len(lines), buf.String())
	}
	last := lines[len(lines)-1]
	var outcome string
	if err := json.Unmarshal(last["outcome"], &outcome); err != nil {
		t.Fatalf("解析 outcome 失败：%v", err)
	}
	if outcome != string(domain.AttemptFailed) {
		t.Errorf("末条日志 outcome = %q，期望 %q", outcome, domain.AttemptFailed)
	}
}

// TestGatewayConcurrencyLimitSerializesRequests 覆盖并发上限 1 的渠道上两个并发请求：
//
// 上游任一时刻只应看到 1 个在途请求，两个请求都完成，且并发位不泄漏——
// 第三个请求仍能正常完成。
func TestGatewayConcurrencyLimitSerializesRequests(t *testing.T) {
	const successBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`

	var active, maxActive int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := atomic.AddInt64(&active, 1)
		for {
			observed := atomic.LoadInt64(&maxActive)
			if current <= observed || atomic.CompareAndSwapInt64(&maxActive, observed, current) {
				break
			}
		}
		// 留出重叠窗口：没有并发位约束时第二个请求会同时进入本处理器。
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt64(&active, -1)
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, successBody)
	}))
	defer upstream.Close()

	st := &fakeGatewayStore{
		auth: activeAuth(),
		routes: []store.RouteCandidate{{
			ChannelID: 10, BaseURL: upstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
			RateLimitConcurrency: 1,
		}},
		credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		wantMerchantID: 1,
	}
	gw := newTestGateway(t, st)
	server := httptest.NewServer(gw.handler)
	defer server.Close()
	endpoint := server.URL + domain.ProtocolOpenAIChat.EndpointPath()

	statuses := make([]int, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i], errs[i] = postForConcurrency(endpoint)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个请求失败：%v", i+1, err)
		}
		if statuses[i] != http.StatusOK {
			t.Errorf("第 %d 个请求状态码 = %d，期望 200", i+1, statuses[i])
		}
	}
	if got := atomic.LoadInt64(&maxActive); got > 1 {
		t.Errorf("上游并发峰值 = %d，期望不超过 1", got)
	}

	// 并发位已全部归还：第三个请求不得因残留占用而超时。
	if third := doPost(t, endpoint, authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`); third.status != http.StatusOK {
		t.Errorf("第三个请求状态码 = %d，期望 200，响应体 %s", third.status, third.body)
	}
}

// e2eDialect 是一种线协议在路由矩阵里的固定样本：
// 客户端请求体、上游响应体与调用上游时的凭据形态。
//
// 矩阵只换「客户端方言」与「上游渠道方言」两个维度，请求与响应样本固定在一处。
type e2eDialect struct {
	protocol domain.Protocol
	adapter  domain.Adapter
	// requestBody 与 streamRequestBody 分别是本方言的非流式与流式客户端请求体。
	requestBody       string
	streamRequestBody string
	// upstreamResponse 与 upstreamStream 分别是本方言上游返回的非流式与流式响应体。
	// 流式样本复用 usage_test.go 里的三方言 SSE 常量。
	upstreamResponse string
	upstreamStream   string
	// upstreamHeader 与 upstreamHeaderValue 是本方言调用上游时凭据请求头的注入形态。
	upstreamHeader      string
	upstreamHeaderValue string
	// streamTextMarker 与 streamTerminator 用于断言重建后的流属于客户端方言。
	streamTextMarker string
	streamTerminator string
}

// e2eDialects 返回矩阵里的三种方言，顺序即子用例顺序。
func e2eDialects() []e2eDialect {
	return []e2eDialect{
		{
			protocol:            domain.ProtocolOpenAIChat,
			adapter:             openaichat.New(),
			requestBody:         `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`,
			streamRequestBody:   `{"model":"alias","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			upstreamResponse:    `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
			upstreamStream:      chatSSE,
			upstreamHeader:      "Authorization",
			upstreamHeaderValue: "Bearer sk-upstream",
			streamTextMarker:    `"content":"hello"`,
			streamTerminator:    "data: [DONE]",
		},
		{
			protocol:            domain.ProtocolOpenAIResponses,
			adapter:             openairesponses.New(),
			requestBody:         `{"model":"alias","input":"hi"}`,
			streamRequestBody:   `{"model":"alias","input":"hi","stream":true}`,
			upstreamResponse:    `{"id":"resp_1","model":"up-model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":3}}`,
			upstreamStream:      responsesSSE,
			upstreamHeader:      "Authorization",
			upstreamHeaderValue: "Bearer sk-upstream",
			streamTextMarker:    `"delta":"hello"`,
			streamTerminator:    "event: response.completed",
		},
		{
			protocol:            domain.ProtocolAnthropicMessages,
			adapter:             anthropic.New(),
			requestBody:         `{"model":"alias","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			streamRequestBody:   `{"model":"alias","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":true}`,
			upstreamResponse:    `{"id":"msg_1","type":"message","role":"assistant","model":"up-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`,
			upstreamStream:      anthropicSSE,
			upstreamHeader:      "x-api-key",
			upstreamHeaderValue: "sk-upstream",
			streamTextMarker:    `"text":"hello"`,
			streamTerminator:    "event: message_stop",
		},
	}
}

// dialectFor 按协议取出矩阵样本；未登记即让用例失败。
func dialectFor(t *testing.T, protocol domain.Protocol) e2eDialect {
	t.Helper()
	for _, dialect := range e2eDialects() {
		if dialect.protocol == protocol {
			return dialect
		}
	}
	t.Fatalf("没有协议 %q 的样本", string(protocol))
	return e2eDialect{}
}

// dialectUpstream 记录最后一次上游调用的事实，供矩阵断言读取。
type dialectUpstream struct {
	mu         sync.Mutex
	lastPath   string
	lastHeader http.Header
	lastModel  string
	lastStream bool
	calls      int
}

// snapshot 返回最后一次调用的事实与累计调用次数。
func (u *dialectUpstream) snapshot() (string, http.Header, string, bool, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastPath, u.lastHeader.Clone(), u.lastModel, u.lastStream, u.calls
}

// newDialectUpstream 起一个按端点段回响应的上游：网关按候选渠道的协议拼接地址，
// 命中的路径因此直接反映本次尝试使用的是哪种上游方言。
func newDialectUpstream(t *testing.T) (*httptest.Server, *dialectUpstream) {
	t.Helper()
	dialects := e2eDialects()
	recorder := &dialectUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		model, _ := fields["model"].(string)
		stream, _ := fields["stream"].(bool)
		recorder.mu.Lock()
		recorder.lastPath = r.URL.Path
		recorder.lastHeader = r.Header.Clone()
		recorder.lastModel = model
		recorder.lastStream = stream
		recorder.calls++
		recorder.mu.Unlock()

		for _, dialect := range dialects {
			if r.URL.Path != dialect.protocol.EndpointSegment() {
				continue
			}
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, dialect.upstreamStream)
				return
			}
			w.Header().Set("Content-Type", jsonContentType)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, dialect.upstreamResponse)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

// testGatewayWithLogs 装配一个带尝试日志与访问日志缓冲区的网关。
func testGatewayWithLogs(t *testing.T, st gatewayStore) (http.Handler, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	attempts := &bytes.Buffer{}
	access := &bytes.Buffer{}
	gw, err := newGateway(st, gatewayOptions{
		CompleteTimeout: 5 * time.Second,
		Observer:        newAttemptObserver(newJSONLogger(attempts)),
		Logger:          newAccessLogger(access),
	})
	if err != nil {
		t.Fatalf("装配网关失败：%v", err)
	}
	t.Cleanup(gw.Close)
	return gw.handler, attempts, access
}

// assertAttemptLogCrossProtocol 断言尝试日志恰有一行，且跨协议标记与响应重建标注符合期望。
func assertAttemptLogCrossProtocol(t *testing.T, buf *bytes.Buffer, cross bool) {
	t.Helper()
	lines := attemptLogLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("尝试日志行数 = %d，期望 1：%s", len(lines), buf.String())
	}
	if got, want := string(lines[0]["cross_protocol"]), strconv.FormatBool(cross); got != want {
		t.Errorf("尝试日志 cross_protocol = %s，期望 %s", got, want)
	}
	parts := string(lines[0]["rewritten_parts"])
	reencoded := strings.Contains(parts, string(domain.RewritePartResponseReencoded))
	if cross && !reencoded {
		t.Errorf("跨协议尝试应标注 response_reencoded，得到 %s", parts)
	}
	if !cross && reencoded {
		t.Errorf("同协议尝试不应标注 response_reencoded，得到 %s", parts)
	}
}

// assertAccessLogCrossProtocol 断言请求级访问日志里的跨协议标记。
func assertAccessLogCrossProtocol(t *testing.T, buf *bytes.Buffer, cross bool) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &fields); err != nil {
		t.Fatalf("访问日志不是 JSON：%v，原文 %s", err, buf.String())
	}
	if got, want := string(fields["cross_protocol"]), strconv.FormatBool(cross); got != want {
		t.Errorf("访问日志 cross_protocol = %s，期望 %s", got, want)
	}
}

// TestGatewayCrossProtocolRoutingMatrix 覆盖三方言 ×（同协议命中 / 跨协议命中）的非流式矩阵。
//
// 断言：上游收到的端点、模型名与凭据按上游方言；同协议命中原样透传；跨协议命中时客户端
// 拿到的是本方言的合法响应体；尝试与访问日志都按是否跨协议标记 cross_protocol。
func TestGatewayCrossProtocolRoutingMatrix(t *testing.T) {
	dialects := e2eDialects()
	for _, client := range dialects {
		for _, upstream := range dialects {
			sameProtocol := client.protocol == upstream.protocol
			name := string(client.protocol) + "/"
			if sameProtocol {
				name += "same_protocol"
			} else {
				name += "cross_protocol_from_" + string(upstream.protocol)
			}
			t.Run(name, func(t *testing.T) {
				upstreamServer, recorder := newDialectUpstream(t)
				candidate := store.RouteCandidate{
					ChannelID: 10, ChannelType: store.ChannelType(upstream.protocol),
					BaseURL: upstreamServer.URL, CredGroup: "group-a", UpstreamModel: "up-model",
				}
				st := &fakeGatewayStore{
					auth:           activeAuth(),
					credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
					wantMerchantID: 1,
				}
				if sameProtocol {
					st.routes = []store.RouteCandidate{candidate}
				} else {
					st.crossRoutes = []store.RouteCandidate{candidate}
				}
				handler, attempts, access := testGatewayWithLogs(t, st)
				server := httptest.NewServer(handler)
				defer server.Close()

				result := doPost(t, server.URL+client.protocol.EndpointPath(), authSchemePrefix+testAPIKey, client.requestBody)
				if result.status != http.StatusOK {
					t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
				}
				if result.contentType != client.adapter.ContentType() {
					t.Errorf("Content-Type = %q，期望 %q", result.contentType, client.adapter.ContentType())
				}

				path, header, model, _, calls := recorder.snapshot()
				if calls != 1 {
					t.Fatalf("上游调用次数 = %d，期望 1", calls)
				}
				if path != upstream.protocol.EndpointSegment() {
					t.Errorf("上游路径 = %q，期望 %q", path, upstream.protocol.EndpointSegment())
				}
				if model != "up-model" {
					t.Errorf("上游模型名 = %q，期望 up-model", model)
				}
				if got := header.Get(upstream.upstreamHeader); got != upstream.upstreamHeaderValue {
					t.Errorf("%s = %q，期望 %q", upstream.upstreamHeader, got, upstream.upstreamHeaderValue)
				}

				if sameProtocol {
					if string(result.body) != upstream.upstreamResponse {
						t.Errorf("同协议命中应逐字节透传上游响应：\n实际 %s\n期望 %s", result.body, upstream.upstreamResponse)
					}
				} else {
					assertClientDialectText(t, client, result.body)
					if string(result.body) == upstream.upstreamResponse {
						t.Error("跨协议响应不应与上游原始响应逐字节相同")
					}
				}
				assertAttemptLogCrossProtocol(t, attempts, !sameProtocol)
				assertAccessLogCrossProtocol(t, access, !sameProtocol)
			})
		}
	}
}

// assertClientDialectText 断言响应体是客户端方言的合法结构且承载期望文本。
func assertClientDialectText(t *testing.T, client e2eDialect, body []byte) {
	t.Helper()
	resp, err := client.adapter.DecodeResponse(body)
	if err != nil {
		t.Fatalf("响应不是客户端方言 %q 的合法结构：%v，原文 %s", string(client.protocol), err, body)
	}
	for _, part := range resp.Message.Parts {
		if part.Kind == domain.PartText && part.Text == "hello" {
			return
		}
	}
	t.Errorf("响应缺少期望文本 hello，得到 %+v", resp.Message.Parts)
}

// TestGatewayCrossProtocolMissingIsJSON404 覆盖三级候选全缺位：保持既有 404 错误体不变。
func TestGatewayCrossProtocolMissingIsJSON404(t *testing.T) {
	for _, client := range e2eDialects() {
		t.Run(string(client.protocol), func(t *testing.T) {
			st := &fakeGatewayStore{auth: activeAuth(), wantMerchantID: 1}
			server := httptest.NewServer(newTestGateway(t, st).handler)
			defer server.Close()

			result := doPost(t, server.URL+client.protocol.EndpointPath(), authSchemePrefix+testAPIKey, client.requestBody)
			assertJSONError(t, result, http.StatusNotFound)
		})
	}
}

// TestGatewayFallsBackToCrossProtocolAfterSameProtocolFailure 覆盖同协议候选失败后继续降到跨协议候选，
// 并守住去重：不限协议查询含同协议行，失败的同协议渠道不得在跨协议阶段再试一次。
func TestGatewayFallsBackToCrossProtocolAfterSameProtocolFailure(t *testing.T) {
	failUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"boom"}}`)
	}))
	defer failUpstream.Close()
	okUpstream, recorder := newDialectUpstream(t)

	chatDialect := dialectFor(t, domain.ProtocolOpenAIChat)
	degradedChannel := store.RouteCandidate{
		ChannelID: 10, ChannelType: store.ChannelTypeOpenAIChat,
		BaseURL: failUpstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
	}
	st := &fakeGatewayStore{
		auth:        activeAuth(),
		credentials: []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		// 同协议段只有失败渠道；不限协议查询包含它自己与一条 anthropic 渠道。
		routes: []store.RouteCandidate{degradedChannel},
		crossRoutes: []store.RouteCandidate{
			degradedChannel,
			{
				ChannelID: 20, ChannelType: store.ChannelTypeAnthropicMessages,
				BaseURL: okUpstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
			},
		},
		wantMerchantID: 1,
	}
	handler, attempts, _ := testGatewayWithLogs(t, st)
	server := httptest.NewServer(handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(), authSchemePrefix+testAPIKey, chatDialect.requestBody)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	assertClientDialectText(t, chatDialect, result.body)

	_, _, model, _, calls := recorder.snapshot()
	if calls != 1 {
		t.Errorf("跨协议上游调用次数 = %d，期望 1（失败的同协议渠道不得在跨协议阶段重试）", calls)
	}
	if model != "up-model" {
		t.Errorf("上游模型名 = %q，期望 up-model", model)
	}
	// 两次尝试：同协议失败一次，跨协议成功一次；第二行标记换渠道且跨协议。
	lines := attemptLogLines(t, attempts)
	if len(lines) != 2 {
		t.Fatalf("尝试日志行数 = %d，期望 2：%s", len(lines), attempts.String())
	}
	if got := string(lines[1]["cross_protocol"]); got != "true" {
		t.Errorf("第二条尝试 cross_protocol = %s，期望 true", got)
	}
	if got := string(lines[1]["channel_switched"]); got != "true" {
		t.Errorf("第二条尝试 channel_switched = %s，期望 true", got)
	}
}

// TestGatewayFallsBackToCrossProtocolAfterSameProtocolSegmentExhausted 覆盖同协议段两条候选全失败后
// 仍降到跨协议候选：尝试预算按段计量后，同协议段的失败不会挤掉跨协议降级的机会。
func TestGatewayFallsBackToCrossProtocolAfterSameProtocolSegmentExhausted(t *testing.T) {
	failUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"boom"}}`)
	}))
	defer failUpstream.Close()
	okUpstream, recorder := newDialectUpstream(t)

	chatDialect := dialectFor(t, domain.ProtocolOpenAIChat)
	// 同协议段两条渠道都失败；跨协议段提供一条健康的 anthropic 渠道。
	failingChannels := []store.RouteCandidate{
		{
			ChannelID: 10, ChannelType: store.ChannelTypeOpenAIChat,
			BaseURL: failUpstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
		},
		{
			ChannelID: 11, ChannelType: store.ChannelTypeOpenAIChat,
			BaseURL: failUpstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
		},
	}
	st := &fakeGatewayStore{
		auth:        activeAuth(),
		credentials: []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		routes:      failingChannels,
		crossRoutes: []store.RouteCandidate{{
			ChannelID: 20, ChannelType: store.ChannelTypeAnthropicMessages,
			BaseURL: okUpstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
		}},
		wantMerchantID: 1,
	}
	handler, attempts, _ := testGatewayWithLogs(t, st)
	server := httptest.NewServer(handler)
	defer server.Close()

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(), authSchemePrefix+testAPIKey, chatDialect.requestBody)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	assertClientDialectText(t, chatDialect, result.body)

	_, _, model, _, calls := recorder.snapshot()
	if calls != 1 {
		t.Errorf("跨协议上游调用次数 = %d，期望 1", calls)
	}
	if model != "up-model" {
		t.Errorf("上游模型名 = %q，期望 up-model", model)
	}
	// 三次尝试：同协议两条各失败一次，第三条跨协议成功；末条同时标记换渠道与跨协议。
	lines := attemptLogLines(t, attempts)
	if len(lines) != 3 {
		t.Fatalf("尝试日志行数 = %d，期望 3：%s", len(lines), attempts.String())
	}
	if got := string(lines[2]["cross_protocol"]); got != "true" {
		t.Errorf("第三条尝试 cross_protocol = %s，期望 true", got)
	}
	if got := string(lines[2]["channel_switched"]); got != "true" {
		t.Errorf("第三条尝试 channel_switched = %s，期望 true", got)
	}
	var outcome string
	if err := json.Unmarshal(lines[2]["outcome"], &outcome); err != nil {
		t.Fatalf("解析 outcome 失败：%v", err)
	}
	if outcome != string(domain.AttemptOK) {
		t.Errorf("第三条尝试 outcome = %s，期望 %s", outcome, domain.AttemptOK)
	}
}

// TestGatewayCrossProtocolStreamingRebuild 覆盖流式跨协议重建：上游方言的 SSE 帧被重建为
// 客户端方言的帧，且转换事实在尝试与访问日志里标记。
func TestGatewayCrossProtocolStreamingRebuild(t *testing.T) {
	dialects := e2eDialects()
	for _, client := range dialects {
		for _, upstream := range dialects {
			if client.protocol == upstream.protocol {
				continue
			}
			t.Run(string(client.protocol)+"/from_"+string(upstream.protocol), func(t *testing.T) {
				upstreamServer, recorder := newDialectUpstream(t)
				st := &fakeGatewayStore{
					auth:        activeAuth(),
					credentials: []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
					crossRoutes: []store.RouteCandidate{{
						ChannelID: 10, ChannelType: store.ChannelType(upstream.protocol),
						BaseURL: upstreamServer.URL, CredGroup: "group-a", UpstreamModel: "up-model",
					}},
					wantMerchantID: 1,
				}
				handler, attempts, access := testGatewayWithLogs(t, st)
				server := httptest.NewServer(handler)
				defer server.Close()

				result := doPost(t, server.URL+client.protocol.EndpointPath(), authSchemePrefix+testAPIKey, client.streamRequestBody)
				if result.status != http.StatusOK {
					t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
				}
				if result.contentType != client.adapter.StreamContentType() {
					t.Errorf("Content-Type = %q，期望 %q", result.contentType, client.adapter.StreamContentType())
				}
				body := string(result.body)
				if !strings.Contains(body, client.streamTextMarker) {
					t.Errorf("重建后的流缺少客户端方言的文本增量 %q：%s", client.streamTextMarker, body)
				}
				if !strings.Contains(body, client.streamTerminator) {
					t.Errorf("重建后的流缺少客户端方言的结束标记 %q：%s", client.streamTerminator, body)
				}
				if body == upstream.upstreamStream {
					t.Error("跨协议流不应与上游原始帧逐字节相同")
				}

				_, _, model, stream, calls := recorder.snapshot()
				if calls != 1 || !stream {
					t.Fatalf("上游调用次数 = %d、stream = %v，期望 1 次流式调用", calls, stream)
				}
				if model != "up-model" {
					t.Errorf("上游模型名 = %q，期望 up-model", model)
				}
				assertAttemptLogCrossProtocol(t, attempts, true)
				assertAccessLogCrossProtocol(t, access, true)
			})
		}
	}
}
