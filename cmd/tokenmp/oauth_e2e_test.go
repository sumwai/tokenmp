//go:build e2e

// 订阅型上游的端到端剧本：mock OAuth 服务（设备码 + 授权码 + 刷新端点）→ 登录写入凭据
// → 数据面调用 → 过期前自动续期 → 再次调用成功，并断言令牌不出现在日志里。
//
// 与 TestE2EOperatorJourney 同一运行口径：真实 MySQL、真实监听端口与进程内假上游，
// DSN 经 TOKENMP_TEST_MYSQL_DSN 传入，未设置时整体 SKIP。剧本自带数据清理。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/gateway"
	"github.com/sumwai/tokenmp/internal/observability"
	"github.com/sumwai/tokenmp/internal/store"
)

// 剧本使用的模型别名与上游模型名。
const (
	e2eOAuthDeviceModel         = "e2e-oauth-device"
	e2eOAuthDeviceUpstreamModel = "up-e2e-oauth-device"
	e2eOAuthCodeModel           = "e2e-oauth-code"
	e2eOAuthCodeUpstreamModel   = "up-e2e-oauth-code"
)

// mock OAuth 端点签发的令牌。刷新后的访问令牌与登录时不同，
// 「上游收到的是刷新后的令牌」因此可直接断言。
const (
	e2eOAuthLoginToken     = "oauth-access-login"
	e2eOAuthRefreshedToken = "oauth-access-refreshed"
	e2eOAuthCodeToken      = "oauth-access-code"
	e2eOAuthRefresh1       = "oauth-refresh-1"
	e2eOAuthRefresh2       = "oauth-refresh-2"
	e2eOAuthRefreshCode    = "oauth-refresh-code"
)

// e2eOAuthServer 是进程内 mock OAuth 服务：设备码、授权码与刷新三条路径。
type e2eOAuthServer struct {
	server *httptest.Server

	mu           sync.Mutex
	refreshCalls int
}

// newE2EOAuthServer 起一个 mock OAuth 服务并注册关闭。
func newE2EOAuthServer(t *testing.T) *e2eOAuthServer {
	t.Helper()
	o := &e2eOAuthServer{}
	o.server = httptest.NewServer(http.HandlerFunc(o.handle))
	t.Cleanup(o.server.Close)
	return o
}

// handle 按路径与 grant_type 分发。
func (o *e2eOAuthServer) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/device":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"device_code":"dc-1","user_code":"uc-1","verification_uri":"`+o.server.URL+`/verify","interval":1,"expires_in":600}`)
	case "/token":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "解析表单失败", http.StatusBadRequest)
			return
		}
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			e2eWriteOAuthToken(w, e2eOAuthLoginToken, e2eOAuthRefresh1, 30)
		case "refresh_token":
			o.mu.Lock()
			o.refreshCalls++
			o.mu.Unlock()
			e2eWriteOAuthToken(w, e2eOAuthRefreshedToken, e2eOAuthRefresh2, 3600)
		case "authorization_code":
			e2eWriteOAuthToken(w, e2eOAuthCodeToken, e2eOAuthRefreshCode, 3600)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"unsupported_grant_type"}`)
		}
	default:
		http.NotFound(w, r)
	}
}

// refreshCount 返回刷新端点被调用的次数。
func (o *e2eOAuthServer) refreshCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.refreshCalls
}

// e2eWriteOAuthToken 写一份令牌应答。
func e2eWriteOAuthToken(w http.ResponseWriter, access, refresh string, expiresIn int) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":%d,"account":"oauth-account"}`,
		access, refresh, expiresIn)
}

// e2eLogBuffer 是可并发写入的日志缓冲：网关与结算可能在多个 goroutine 里落日志。
type e2eLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *e2eLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *e2eLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestE2EOAuthJourney 是订阅型上游的端到端剧本。
func TestE2EOAuthJourney(t *testing.T) {
	dsn := os.Getenv(e2eDSNEnv)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过订阅型上游端到端剧本", e2eDSNEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	st, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	e2eDropAllTables(t, st)
	t.Cleanup(func() { e2eDropAllTables(t, st) })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	oauthSrv := newE2EOAuthServer(t)
	upstream := newE2EUpstream(t)
	svc := admin.New(st)

	// 商家与账户。
	merchantID, err := svc.CreateMerchant(ctx, "e2e-oauth", "E2E 订阅型商家", store.MerchantKindPartner)
	e2eMust(t, err)
	accountID, err := svc.CreateAccount(ctx, admin.AccountInput{
		Code: "e2e-oauth-account", Name: "E2E 订阅型账户", DefaultMerchantID: &merchantID,
	})
	e2eMust(t, err)
	key, err := svc.IssueKey(ctx, admin.IssueKeyInput{AccountID: accountID, MerchantID: &merchantID, Name: "e2e-oauth"})
	e2eMust(t, err)

	// 两条渠道：设备码分组与授权码分组，都指向同一个假上游。
	deviceGroup := "e2e-oauth-device-group"
	deviceChannel, err := svc.CreateChannel(ctx, admin.ChannelInput{
		MerchantID: merchantID, Name: "e2e-oauth-device", Vendor: "mock",
		Type: store.ChannelTypeOpenAIChat, CredGroup: deviceGroup, BaseURL: upstream.server.URL,
	})
	e2eMust(t, err)
	codeGroup := "e2e-oauth-code-group"
	codeChannel, err := svc.CreateChannel(ctx, admin.ChannelInput{
		MerchantID: merchantID, Name: "e2e-oauth-code", Vendor: "mock",
		Type: store.ChannelTypeOpenAIChat, CredGroup: codeGroup, BaseURL: upstream.server.URL,
	})
	e2eMust(t, err)

	// 渠道画像经 config 声明；当前没有写 config 的管理动作，剧本直接写列。
	deviceConfig := fmt.Sprintf(`{"oauth":{"token_url":%q,"device_url":%q,"client_id":"client-1"}}`,
		oauthSrv.server.URL+"/token", oauthSrv.server.URL+"/device")
	codeConfig := fmt.Sprintf(`{"oauth":{"authorize_url":%q,"token_url":%q,"client_id":"client-1"}}`,
		oauthSrv.server.URL+"/authorize", oauthSrv.server.URL+"/token")
	for _, pair := range []struct {
		id     uint64
		config string
	}{{deviceChannel, deviceConfig}, {codeChannel, codeConfig}} {
		if _, err := st.DB().ExecContext(ctx, "UPDATE upstream_channel SET config = ? WHERE id = ?", pair.config, pair.id); err != nil {
			t.Fatalf("写渠道 config 失败：%v", err)
		}
	}

	// 模型映射与定价。
	for _, pair := range []struct {
		channel uint64
		model   string
		upModel string
	}{
		{deviceChannel, e2eOAuthDeviceModel, e2eOAuthDeviceUpstreamModel},
		{codeChannel, e2eOAuthCodeModel, e2eOAuthCodeUpstreamModel},
	} {
		_, err := svc.SetModelMap(ctx, admin.ModelMapInput{
			ChannelID: pair.channel, Model: pair.model, UpstreamModel: pair.upModel, PriceMultiplier: "1",
		})
		e2eMust(t, err)
		_, err = svc.PublishPricing(ctx, admin.PublishPricingInput{
			MerchantID: merchantID, Model: pair.upModel, EffectiveAt: time.Now().Add(-time.Minute),
			Components: e2ePricingComponents(),
		})
		e2eMust(t, err)
	}

	// 账本：可透支货币包兜住 402 预检，token 包承接扣费。
	_, err = svc.CreditBucket(ctx, admin.CreditBucketInput{
		AccountID: accountID, MerchantID: merchantID, Unit: billing.UnitSettleCurrency,
		Amount: "100", Fallback: billing.FallbackChargeBalance, Source: billing.SourceRecharge,
	})
	e2eMust(t, err)
	productID, err := svc.CreateProduct(ctx, admin.ProductInput{
		MerchantID: merchantID, Name: "e2e-oauth-token", Unit: billing.UnitSettleToken,
		Qty: "1000000", Price: "10", ValidityDays: 30,
	})
	e2eMust(t, err)
	_, err = svc.Buy(ctx, admin.BuyInput{AccountID: accountID, ProductID: productID, Qty: "1", Fallback: billing.FallbackChargeBalance})
	e2eMust(t, err)

	// 登录：设备码流程写入 login 令牌（有效期 30 秒，落在续期窗口内）。
	deviceLogin, err := svc.OAuthLogin(ctx, admin.OAuthLoginInput{
		MerchantID: merchantID, Group: deviceGroup, Name: "primary",
	}, admin.OAuthLoginHooks{
		OnDeviceCode: func(_, _ string) {},
	})
	e2eMust(t, err)
	if deviceLogin.Flow != "device" || deviceLogin.Account != "oauth-account" {
		t.Fatalf("设备码登录结果不符：%+v", deviceLogin)
	}

	// 登录：授权码流程写入 code 令牌（有效期 1 小时，不在续期窗口内）。
	codeLogin, err := svc.OAuthLogin(ctx, admin.OAuthLoginInput{
		MerchantID: merchantID, Group: codeGroup, Name: "primary",
	}, admin.OAuthLoginHooks{
		OnAuthorizeURL: func(string) {},
		ReadCode:       func() (string, error) { return "code-1", nil },
	})
	e2eMust(t, err)
	if codeLogin.Flow != "code" || codeLogin.Account != "oauth-account" {
		t.Fatalf("授权码登录结果不符：%+v", codeLogin)
	}

	// 启动网关，日志集中到缓冲供「令牌不出现在日志」断言。
	logs := &e2eLogBuffer{}
	gw, err := gateway.New(st, gateway.Options{
		CompleteTimeout:  10 * time.Second,
		CredentialLogger: observability.NewJSONLogger(logs),
		Logger:           observability.NewAccessLogger(logs),
	})
	e2eMust(t, err)
	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		gw.Close()
		t.Fatalf("监听临时端口失败：%v", err)
	}
	server := &http.Server{Handler: gw.Handler(), ReadHeaderTimeout: gateway.ReadHeaderTimeout}
	serveCtx, stopServe := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gateway.Run(serveCtx, server, ln) }()
	gatewayURL := "http://" + ln.Addr().String()
	t.Cleanup(func() {
		stopServe()
		if err := <-done; err != nil {
			t.Errorf("关闭服务端失败：%v", err)
		}
		gw.Close()
	})
	e2eWaitForOK(t, gatewayURL+gateway.HealthzPath)

	endpoint := gatewayURL + domain.ProtocolOpenAIChat.EndpointPath()
	request := e2eRequest(domain.ProtocolOpenAIChat, e2eOAuthDeviceModel, false)

	// 第一次调用：登录令牌临近过期，数据面先续期再发请求。
	first := e2ePost(t, endpoint, key.Plaintext, request)
	if first.status != http.StatusOK {
		t.Fatalf("续期后首次调用状态码 = %d，期望 200，响应体 %s", first.status, first.body)
	}
	if hits := upstream.keyHit(e2eOAuthLoginToken); hits != 0 {
		t.Errorf("上游收到登录令牌 %d 次，期望 0（应先用刷新后的令牌）", hits)
	}
	if hits := upstream.keyHit(e2eOAuthRefreshedToken); hits != 1 {
		t.Errorf("上游收到刷新令牌 %d 次，期望 1", hits)
	}
	if got := oauthSrv.refreshCount(); got != 1 {
		t.Errorf("刷新端点被调用 %d 次，期望 1", got)
	}

	// 第二次调用：新令牌有效期充足，不应再续期。
	second := e2ePost(t, endpoint, key.Plaintext, request)
	if second.status != http.StatusOK {
		t.Fatalf("第二次调用状态码 = %d，期望 200，响应体 %s", second.status, second.body)
	}
	if got := oauthSrv.refreshCount(); got != 1 {
		t.Errorf("第二次调用不应再续期，刷新端点被调用 %d 次", got)
	}
	if hits := upstream.keyHit(e2eOAuthRefreshedToken); hits != 2 {
		t.Errorf("上游收到刷新令牌 %d 次，期望 2", hits)
	}

	// 授权码分组的凭据有效期充足，直接使用登录令牌。
	codeRequest := e2eRequest(domain.ProtocolOpenAIChat, e2eOAuthCodeModel, false)
	third := e2ePost(t, endpoint, key.Plaintext, codeRequest)
	if third.status != http.StatusOK {
		t.Fatalf("授权码分组调用状态码 = %d，期望 200，响应体 %s", third.status, third.body)
	}
	if hits := upstream.keyHit(e2eOAuthCodeToken); hits != 1 {
		t.Errorf("上游收到授权码令牌 %d 次，期望 1", hits)
	}
	if got := oauthSrv.refreshCount(); got != 1 {
		t.Errorf("授权码分组不应续期，刷新端点被调用 %d 次", got)
	}

	// 续期结果已写回凭据行。
	if got := e2eCredentialAccess(t, st, merchantID, deviceGroup, "primary"); got != e2eOAuthRefreshedToken {
		t.Errorf("写回的访问令牌 = %q，期望 %q", got, e2eOAuthRefreshedToken)
	}

	// 令牌不出现在日志：访问令牌、刷新令牌、认证头片段都不允许出现。
	rendered := logs.String()
	for _, token := range []string{
		e2eOAuthLoginToken, e2eOAuthRefreshedToken, e2eOAuthCodeToken,
		e2eOAuthRefresh1, e2eOAuthRefresh2, e2eOAuthRefreshCode,
	} {
		if strings.Contains(rendered, token) {
			t.Errorf("日志出现令牌 %q：\n%s", token, rendered)
		}
	}
	if strings.Contains(rendered, "Bearer ") {
		t.Errorf("日志出现 Authorization 形态片段：\n%s", rendered)
	}
}

// e2eCredentialAccess 读出某商家某分组某凭据当前的访问令牌。
func e2eCredentialAccess(t *testing.T, st *store.Store, merchantID uint64, group, name string) string {
	t.Helper()
	var raw []byte
	err := st.DB().QueryRowContext(context.Background(),
		"SELECT secret FROM upstream_credential WHERE merchant_id = ? AND cred_group = ? AND name = ? ORDER BY id DESC LIMIT 1",
		merchantID, group, name).Scan(&raw)
	if err != nil {
		t.Fatalf("读取凭据失败：%v", err)
	}
	var parsed struct {
		Access string `json:"access"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("解析凭据失败：%v", err)
	}
	return parsed.Access
}
