//go:build e2e

// 管理面配置动作的端到端验证：真实 MySQL、真实 HTTP 服务、真实页面会话。
//
// 与运营剧本分文件：剧本验证转发与计费，本文件只验证页面侧的管理面写路径 ——
// 注册出来的普通主体越权被拒、提权后能配出「商家 → 渠道 → 凭据 → 模型映射」，
// 以及会话、错误码与落库结果。与剧本一样由 make e2e 触发，需要真实 MySQL。
package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/adminapi"
	"github.com/sumwai/tokenmp/internal/gateway"
	"github.com/sumwai/tokenmp/internal/store"
)

// e2eWebAccount 是注册用的账号参数。
const (
	e2eWebEmail    = "admin-e2e@example.com"
	e2eWebUsername = "admin-e2e"
	e2eWebPassword = "e2e-web-password"
	// e2eWebFingerprint 是客户端指纹，challenge 与 signup 必须一致。
	e2eWebFingerprint = "e2e-web-admin-fingerprint"
	// e2eWebCredential 是写入凭据的明文，用来断言它不出现在列表里。
	e2eWebCredential = "sk-e2e-do-not-echo"
)

// TestE2EWebAdminConfigActions 覆盖管理面配置动作的完整链路。
func TestE2EWebAdminConfigActions(t *testing.T) {
	dsn := os.Getenv(e2eDSNEnv)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过管理面配置动作验证", e2eDSNEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	st, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("关闭连接失败：%v", err)
		}
	})
	e2eDropAllTables(t, st)
	t.Cleanup(func() { e2eDropAllTables(t, st) })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	base := e2eWebStartServe(t, st)
	svc := admin.New(st)

	// 注册入口开着：注册出来的主体一律是 member。
	token := e2eWebSignup(t, base)

	// 未登录与越权先于一切动作：401 与 403 分开，前端才能决定回登录还是回无权页。
	t.Run("未登录回401", func(t *testing.T) {
		status, env := e2eWebRequest(t, http.MethodPost, base+adminapi.MerchantsPath, "",
			`{"code":"e2e","name":"端到端商家","kind":"partner"}`)
		if status != http.StatusUnauthorized || e2eWebCode(t, env) != 401 {
			t.Fatalf("应 401: %d %s", status, env)
		}
	})
	t.Run("普通主体回403", func(t *testing.T) {
		status, env := e2eWebRequest(t, http.MethodPost, base+adminapi.MerchantsPath, token,
			`{"code":"e2e","name":"端到端商家","kind":"partner"}`)
		if status != http.StatusForbidden || e2eWebCode(t, env) != 403 {
			t.Fatalf("应 403: %d %s", status, env)
		}
		// 只读清单同样越权：管理面没有「能读不能写」的中间态。
		if status, _ := e2eWebRequest(t, http.MethodGet, base+adminapi.ChannelsPath, token, ""); status != http.StatusForbidden {
			t.Fatalf("普通主体读清单应 403，得到 %d", status)
		}
	})

	// 提权：会话每次请求都从库里读身份，改库即刻生效，不需要重新登录。
	if _, err := st.DB().ExecContext(ctx, "UPDATE web_user SET role = ? WHERE email = ?",
		store.RoleAdmin, e2eWebEmail); err != nil {
		t.Fatalf("提权失败：%v", err)
	}

	// 商家 → 渠道 → 凭据 → 模型映射：页面上配一条上游账号的动作序列。
	merchantID := e2eWebCreatedID(t, base, token, http.MethodPost, adminapi.MerchantsPath,
		`{"code":"e2e-partner","name":"端到端商家","kind":"partner"}`)
	channelID := e2eWebCreatedID(t, base, token, http.MethodPost, adminapi.ChannelsPath,
		fmt.Sprintf(`{"merchant_id":%d,"name":"e2e-channel","vendor":"openai","type":"openai_chat",`+
			`"cred_group":"e2e-group","base_url":"https://up.example.com","priority":7}`, merchantID))
	credentialID := e2eWebCreatedID(t, base, token, http.MethodPost, adminapi.CredentialsPath,
		fmt.Sprintf(`{"merchant_id":%d,"cred_group":"e2e-group","name":"primary","api_key":%q}`,
			merchantID, e2eWebCredential))
	e2eWebOK(t, base, token, http.MethodPut, adminapi.ModelMapsPath,
		fmt.Sprintf(`{"channel_id":%d,"model":"e2e-alias","upstream_model":"e2e-up"}`, channelID))

	// 读回：落库的行与请求一致，凭据明文不在列表里。
	channels, err := svc.ListChannels(ctx)
	e2eMust(t, err)
	if len(channels) != 1 || channels[0].ID != channelID || channels[0].MerchantID != merchantID {
		t.Fatalf("渠道行 = %+v，期望刚建的 %d", channels, channelID)
	}
	if channels[0].Priority != 7 || channels[0].BaseURL != "https://up.example.com" {
		t.Errorf("渠道字段未按请求落库：%+v", channels[0])
	}
	credentials, err := svc.ListCredentials(ctx)
	e2eMust(t, err)
	if len(credentials) != 1 || credentials[0].ID != credentialID {
		t.Fatalf("凭据行 = %+v，期望刚建的 %d", credentials, credentialID)
	}
	if strings.Contains(credentials[0].Prefix, e2eWebCredential) {
		t.Errorf("凭据列表回显了明文：%q", credentials[0].Prefix)
	}
	maps, err := svc.ListModelMaps(ctx)
	e2eMust(t, err)
	if len(maps) != 1 || maps[0].ChannelID != channelID || maps[0].UpstreamModel != "e2e-up" {
		t.Fatalf("模型映射行 = %+v", maps)
	}

	// 启停两向都能走通：停用是运营动作，误停之后必须能原地恢复。
	e2eWebOK(t, base, token, http.MethodPost, fmt.Sprintf("%s/%d/disable", adminapi.ChannelsPath, channelID), "")
	e2eWebAssertChannelEnabled(t, svc, ctx, channelID, false)
	e2eWebOK(t, base, token, http.MethodPost, fmt.Sprintf("%s/%d/enable", adminapi.ChannelsPath, channelID), "")
	e2eWebAssertChannelEnabled(t, svc, ctx, channelID, true)

	e2eWebOK(t, base, token, http.MethodPost, fmt.Sprintf("%s/%d/disable", adminapi.CredentialsPath, credentialID), "")
	credentials, err = svc.ListCredentials(ctx)
	e2eMust(t, err)
	if credentials[0].Enabled {
		t.Error("凭据停用未生效")
	}
	e2eWebOK(t, base, token, http.MethodPost, fmt.Sprintf("%s/%d/enable", adminapi.CredentialsPath, credentialID), "")
	credentials, err = svc.ListCredentials(ctx)
	e2eMust(t, err)
	if !credentials[0].Enabled {
		t.Error("凭据启用未生效")
	}

	// 模型映射停用后再 PUT 覆盖：upsert 把启用位置回 1，页面不需要单独的启用动作。
	e2eWebOK(t, base, token, http.MethodPost, fmt.Sprintf("%s/%d/disable", adminapi.ModelMapsPath, maps[0].ID), "")
	e2eWebAssertModelMapEnabled(t, svc, ctx, maps[0].ID, false)
	e2eWebOK(t, base, token, http.MethodPut, adminapi.ModelMapsPath,
		fmt.Sprintf(`{"channel_id":%d,"model":"e2e-alias","upstream_model":"e2e-up-2"}`, channelID))
	e2eWebAssertModelMapEnabled(t, svc, ctx, maps[0].ID, true)

	// 归属绑定：商家域（/api/v1/partner/*）的作用域来源。
	userID := e2eWebUserID(t, st, ctx)
	e2eWebOK(t, base, token, http.MethodPost, fmt.Sprintf("%s/%d/owner", adminapi.MerchantsPath, merchantID),
		fmt.Sprintf(`{"user_id":%d}`, userID))
	merchants, err := svc.ListMerchants(ctx)
	e2eMust(t, err)
	owner := e2eWebMerchantOwner(t, merchants, merchantID)
	if owner == nil || *owner != userID {
		t.Fatalf("商家归属 = %v，期望 %d", owner, userID)
	}

	// 形状与枚举错误在落库之前被拦下，且文案是字段级提示。
	for name, tc := range map[string]struct {
		method string
		path   string
		body   string
	}{
		"协议非法": {http.MethodPost, adminapi.ChannelsPath,
			fmt.Sprintf(`{"merchant_id":%d,"name":"c","type":"openai","cred_group":"g","base_url":"https://u"}`, merchantID)},
		"未知字段": {http.MethodPost, adminapi.ChannelsPath,
			fmt.Sprintf(`{"merchant_id":%d,"name":"c","type":"openai_chat","cred_group":"g","base_url":"https://u","enabled":true}`, merchantID)},
		"商家类型非法": {http.MethodPost, adminapi.MerchantsPath, `{"code":"x","name":"x","kind":"reseller"}`},
		"模型名为空":  {http.MethodPut, adminapi.ModelMapsPath, fmt.Sprintf(`{"channel_id":%d,"model":" ","upstream_model":"u"}`, channelID)},
	} {
		t.Run(name, func(t *testing.T) {
			status, env := e2eWebRequest(t, tc.method, base+tc.path, token, tc.body)
			if status != http.StatusBadRequest || e2eWebCode(t, env) != 400 {
				t.Fatalf("应 400: %d %s", status, env)
			}
		})
	}

	// 唯一键冲突回 409，与 400 分开：文案不同，下一步也不同。
	t.Run("商家编码重复回409", func(t *testing.T) {
		status, env := e2eWebRequest(t, http.MethodPost, base+adminapi.MerchantsPath, token,
			`{"code":"e2e-partner","name":"重复编码","kind":"partner"}`)
		if status != http.StatusConflict || e2eWebCode(t, env) != 409 {
			t.Fatalf("应 409: %d %s", status, env)
		}
	})
}

// e2eWebStartServe 起一个开启注册入口的真实服务，返回基地址。
func e2eWebStartServe(t *testing.T, st *store.Store) string {
	t.Helper()
	gw, err := gateway.New(st, gateway.Options{
		CompleteTimeout:  10 * time.Second,
		WebSignupEnabled: true,
	})
	e2eMust(t, err)

	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		gw.Close()
		t.Fatalf("监听临时端口失败：%v", err)
	}
	server := &http.Server{Handler: gw.Handler(), ReadHeaderTimeout: gateway.ReadHeaderTimeout}
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gateway.Run(serveCtx, server, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("关闭服务端失败：%v", err)
		}
		gw.Close()
	})

	base := "http://" + ln.Addr().String()
	e2eWaitForOK(t, base+gateway.HealthzPath)
	return base
}

// e2eWebSignup 走一次页面注册（challenge → 加密 → signup），返回访问令牌。
func e2eWebSignup(t *testing.T, base string) string {
	t.Helper()
	status, env := e2eWebRequest(t, http.MethodGet,
		base+"/api/v1/auth/challenge?fingerprint="+url.QueryEscape(e2eWebFingerprint), "", "")
	if status != http.StatusOK {
		t.Fatalf("取加密公钥应 200，得到 %d %s", status, env)
	}
	var challenge struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(env["data"], &challenge); err != nil {
		t.Fatalf("解析 public_key 失败：%v", err)
	}

	body, err := json.Marshal(map[string]string{
		"email":       e2eWebEmail,
		"username":    e2eWebUsername,
		"password":    e2eWebEncrypt(t, challenge.PublicKey, e2eWebPassword),
		"fingerprint": e2eWebFingerprint,
	})
	e2eMust(t, err)

	status, env = e2eWebRequest(t, http.MethodPost, base+"/api/v1/auth/signup", "", string(body))
	if status != http.StatusOK {
		t.Fatalf("注册应 200，得到 %d %s", status, env)
	}
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(env["data"], &session); err != nil {
		t.Fatalf("解析会话失败：%v", err)
	}
	if session.AccessToken == "" {
		t.Fatal("注册响应没有访问令牌")
	}
	return session.AccessToken
}

// e2eWebEncrypt 用 challenge 的 SPKI 公钥加密密码（RSA-OAEP / SHA-256，base64 密文）。
func e2eWebEncrypt(t *testing.T, spkiBase64, plaintext string) string {
	t.Helper()
	der, err := base64.StdEncoding.DecodeString(spkiBase64)
	if err != nil {
		t.Fatalf("解码公钥失败：%v", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("解析公钥失败：%v", err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("公钥不是 RSA：%T", parsed)
	}
	cipher, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, []byte(plaintext), nil)
	if err != nil {
		t.Fatalf("加密密码失败：%v", err)
	}
	return base64.StdEncoding.EncodeToString(cipher)
}

// e2eWebRequest 发一次页面请求，返回状态码与解出的信封。
func e2eWebRequest(t *testing.T, method, endpoint, token, body string) (int, map[string]json.RawMessage) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, endpoint, reader)
	if err != nil {
		t.Fatalf("构造 %s %s 失败：%v", method, endpoint, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败：%v", method, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败：%v", err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解析信封 %q 失败：%v", raw, err)
	}
	return resp.StatusCode, env
}

// e2eWebCode 读信封里的业务码。
func e2eWebCode(t *testing.T, env map[string]json.RawMessage) int {
	t.Helper()
	var code int
	if err := json.Unmarshal(env["code"], &code); err != nil {
		t.Fatalf("解析 code 失败：%v", err)
	}
	return code
}

// e2eWebOK 发一次动作并要求 200。
func e2eWebOK(t *testing.T, base, token, method, path, body string) {
	t.Helper()
	status, env := e2eWebRequest(t, method, base+path, token, body)
	if status != http.StatusOK {
		t.Fatalf("%s %s 应 200，得到 %d %s", method, path, status, env)
	}
}

// e2eWebCreatedID 发一次写入动作并要求 data.id。
func e2eWebCreatedID(t *testing.T, base, token, method, path, body string) uint64 {
	t.Helper()
	status, env := e2eWebRequest(t, method, base+path, token, body)
	if status != http.StatusOK {
		t.Fatalf("%s %s 应 200，得到 %d %s", method, path, status, env)
	}
	var data struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data.id 失败：%v", err)
	}
	if data.ID == 0 {
		t.Fatalf("%s %s 未返回 id：%s", method, path, env["data"])
	}
	return data.ID
}

// e2eWebUserID 读注册出来的登录主体 id。
func e2eWebUserID(t *testing.T, st *store.Store, ctx context.Context) uint64 {
	t.Helper()
	var id uint64
	if err := st.DB().QueryRowContext(ctx, "SELECT id FROM web_user WHERE email = ?", e2eWebEmail).Scan(&id); err != nil {
		t.Fatalf("查询登录主体失败：%v", err)
	}
	return id
}

// e2eWebAssertChannelEnabled 断言渠道启用位。
func e2eWebAssertChannelEnabled(t *testing.T, svc *admin.Service, ctx context.Context, id uint64, want bool) {
	t.Helper()
	channels, err := svc.ListChannels(ctx)
	e2eMust(t, err)
	for _, channel := range channels {
		if channel.ID == id {
			if channel.Enabled != want {
				t.Fatalf("渠道 %d 启用位 = %v，期望 %v", id, channel.Enabled, want)
			}
			return
		}
	}
	t.Fatalf("渠道列表里没有 %d", id)
}

// e2eWebAssertModelMapEnabled 断言模型映射启用位。
func e2eWebAssertModelMapEnabled(t *testing.T, svc *admin.Service, ctx context.Context, id uint64, want bool) {
	t.Helper()
	maps, err := svc.ListModelMaps(ctx)
	e2eMust(t, err)
	for _, m := range maps {
		if m.ID == id {
			if m.Enabled != want {
				t.Fatalf("模型映射 %d 启用位 = %v，期望 %v", id, m.Enabled, want)
			}
			return
		}
	}
	t.Fatalf("模型映射列表里没有 %d", id)
}

// e2eWebMerchantOwner 取商家的归属主体。
func e2eWebMerchantOwner(t *testing.T, merchants []store.Merchant, id uint64) *uint64 {
	t.Helper()
	for _, m := range merchants {
		if m.ID == id {
			return m.OwnerUserID
		}
	}
	t.Fatalf("商家列表里没有 %d", id)
	return nil
}
