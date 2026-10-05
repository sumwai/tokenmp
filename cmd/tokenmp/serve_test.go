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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
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
	auth           *store.APIKeyAuth
	routes         []store.RouteCandidate
	credentials    []store.Credential
	wantMerchantID uint64

	// 结算数据：零值表示「无定价、无规则、无渠道加成」。
	pricing           *settlement.Pricing
	rules             map[scopeKey][]settlement.Rule
	dayKinds          map[string]billing.DayKind
	accountMultiplier decimal.Decimal
	channelMultiplier decimal.Decimal
	buckets           []settlement.Bucket

	// mu 保护用量记录与账本：流式请求下结算在服务端 goroutine 里被调用。
	mu sync.Mutex
	// usageRows 按写入顺序保存流水行，供端到端用例断言分量。
	usageRows []recordedUsage
	// insertErr 非 nil 时写入返回它，用于验证落库失败不影响转发。
	insertErr error
}

func (f *fakeGatewayStore) LookupAPIKey(_ context.Context, keyHash string, _ time.Time) (*store.APIKeyAuth, error) {
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
		Model: row.Model, Usage: row.Usage, Multiplier: "1",
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
		Model: row.Model, Usage: row.Metrics, PricingID: row.PricingID,
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
	return httpResult{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), body: respBody}
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
