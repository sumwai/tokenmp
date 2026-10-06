package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/credential"
	"github.com/sumwai/tokenmp/internal/oauth"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖 `admin credential oauth-login` 业务层：设备码与授权码两条流程、
// 画像缺失的拒绝，以及「结果只回显 group 与 account」。

// oauthLoginService 构造指向假端点的服务，并把渠道画像写进假存储。
func oauthLoginService(t *testing.T, server *httptest.Server, config string) (*Service, *fakeStore, *store.CredentialRow) {
	t.Helper()
	f := &fakeStore{}
	var captured store.CredentialRow
	f.channelConfigByGroup = func(context.Context, uint64, string) ([]byte, error) {
		return []byte(config), nil
	}
	f.insertCredential = func(_ context.Context, c store.CredentialRow) (uint64, error) {
		captured = c
		return 42, nil
	}
	s := New(f,
		WithClock(func() time.Time { return fixedNow }),
		WithOAuthClient(oauth.New(oauth.Options{HTTPClient: server.Client()})),
	)
	return s, f, &captured
}

// TestOAuthLoginDeviceFlow 断言设备码流程展示用户码、轮询换令牌并写入 oauth 凭据。
func TestOAuthLoginDeviceFlow(t *testing.T) {
	var tokenCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device":
			if err := r.ParseForm(); err != nil {
				t.Errorf("解析表单失败：%v", err)
			}
			if r.Form.Get("client_id") != "client-1" {
				t.Errorf("设备码请求缺少 client_id：%v", r.Form)
			}
			_, _ = w.Write([]byte(`{"device_code":"dc-1","user_code":"uc-1","verification_uri":"https://verify","interval":1,"expires_in":600}`))
		case "/token":
			tokenCalls++
			_, _ = w.Write([]byte(`{"access_token":"access-device","refresh_token":"refresh-device","expires_in":3600,"account":"acct-device"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config := `{"oauth":{"token_url":"` + server.URL + `/token","device_url":"` + server.URL + `/device","client_id":"client-1"}}`
	s, f, captured := oauthLoginService(t, server, config)

	var gotURI, gotUserCode string
	result, err := s.OAuthLogin(context.Background(), OAuthLoginInput{MerchantID: 1, Group: "group-a"}, OAuthLoginHooks{
		OnDeviceCode: func(uri, userCode string) { gotURI, gotUserCode = uri, userCode },
	})
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if gotURI != "https://verify" || gotUserCode != "uc-1" {
		t.Fatalf("设备码展示不符：uri=%q code=%q", gotURI, gotUserCode)
	}
	if result.Flow != oauthFlowDevice || result.Account != "acct-device" || result.Group != "group-a" || result.ID != 42 {
		t.Fatalf("结果不符：%+v", result)
	}
	if tokenCalls != 1 {
		t.Errorf("令牌端点调用 %d 次，期望 1", tokenCalls)
	}
	if !f.called("InsertCredential") {
		t.Fatal("应写入凭据")
	}
	assertOAuthSecret(t, captured.Secret, "access-device", "refresh-device", "acct-device")
}

// TestOAuthLoginAuthorizationCodeFlow 断言授权码流程展示授权地址、交换回调码并写入凭据。
func TestOAuthLoginAuthorizationCodeFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			http.NotFound(w, r)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("解析表单失败：%v", err)
			}
			if r.Form.Get("code") != "code-1" {
				t.Errorf("交换请求缺少授权码：%v", r.Form)
			}
			_, _ = w.Write([]byte(`{"access_token":"access-code","refresh_token":"refresh-code","expires_in":60}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config := `{"oauth":{"authorize_url":"` + server.URL + `/authorize","token_url":"` + server.URL + `/token","client_id":"client-1"}}`
	s, _, captured := oauthLoginService(t, server, config)

	var authorizeURL string
	result, err := s.OAuthLogin(context.Background(), OAuthLoginInput{MerchantID: 1, Group: "group-a", Account: "acct-code"}, OAuthLoginHooks{
		OnAuthorizeURL: func(url string) { authorizeURL = url },
		ReadCode:       func() (string, error) { return "code-1", nil },
	})
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if !strings.Contains(authorizeURL, "client_id=client-1") {
		t.Fatalf("授权地址不符：%q", authorizeURL)
	}
	if result.Flow != oauthFlowCode || result.Account != "acct-code" {
		t.Fatalf("结果不符：%+v", result)
	}
	assertOAuthSecret(t, captured.Secret, "access-code", "refresh-code", "acct-code")
}

// TestOAuthLoginReadCodeError 断言读取授权码失败时中止，不写入凭据。
func TestOAuthLoginReadCodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	config := `{"oauth":{"authorize_url":"` + server.URL + `/authorize","token_url":"` + server.URL + `/token","client_id":"client-1"}}`
	s, f, _ := oauthLoginService(t, server, config)

	_, err := s.OAuthLogin(context.Background(), OAuthLoginInput{MerchantID: 1, Group: "group-a"}, OAuthLoginHooks{
		ReadCode: func() (string, error) { return "", errors.New("EOF") },
	})
	if err == nil {
		t.Fatal("读取授权码失败应中止")
	}
	if f.called("InsertCredential") {
		t.Fatal("失败时不应写入凭据")
	}
}

// TestOAuthLoginRejectsUnusableProfile 断言画像缺失时在触达令牌端点前报错。
func TestOAuthLoginRejectsUnusableProfile(t *testing.T) {
	f := &fakeStore{}
	f.channelConfigByGroup = func(context.Context, uint64, string) ([]byte, error) {
		return []byte(`{"signin_header":{"name":"x"}}`), nil
	}
	s := New(f, WithClock(func() time.Time { return fixedNow }))

	if _, err := s.OAuthLogin(context.Background(), OAuthLoginInput{MerchantID: 1, Group: "group-a"}, OAuthLoginHooks{}); err == nil {
		t.Fatal("缺少画像应被拒绝")
	}
	if f.called("InsertCredential") {
		t.Fatal("缺少画像时不应写入凭据")
	}
}

// TestOAuthLoginValidatesInput 断言缺少商家或分组时在触达存储层前报错。
func TestOAuthLoginValidatesInput(t *testing.T) {
	f := &fakeStore{}
	s := New(f)
	if _, err := s.OAuthLogin(context.Background(), OAuthLoginInput{Group: "g"}, OAuthLoginHooks{}); err == nil {
		t.Fatal("缺少商家应被拒绝")
	}
	if _, err := s.OAuthLogin(context.Background(), OAuthLoginInput{MerchantID: 1}, OAuthLoginHooks{}); err == nil {
		t.Fatal("缺少分组应被拒绝")
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层：%v", f.calls)
	}
}

// TestOAuthLoginResultHasNoToken 断言登录结果序列化后不含任何令牌。
func TestOAuthLoginResultHasNoToken(t *testing.T) {
	encoded, err := json.Marshal(OAuthLoginResult{ID: 1, Group: "g", Account: "acct", Flow: oauthFlowDevice})
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	if strings.Contains(string(encoded), "access") || strings.Contains(string(encoded), "refresh") {
		t.Fatalf("结果泄露令牌：%s", encoded)
	}
}

// TestListCredentialsShowsOAuthState 断言凭据列表展示 oauth 形态、过期时刻与过期标记。
func TestListCredentialsShowsOAuthState(t *testing.T) {
	expired, err := credential.BuildOAuthSecret("", "refresh-1", time.Time{}, "acct")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	valid, err := credential.BuildOAuthSecret("access-abcdefgh", "refresh-2", fixedNow.Add(time.Hour), "acct")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	f := &fakeStore{}
	f.listCredentials = func(context.Context) ([]store.CredentialRow, error) {
		return []store.CredentialRow{
			{ID: 1, MerchantID: 2, CredGroup: "g", Name: "expired", Secret: expired, Enabled: true},
			{ID: 2, MerchantID: 2, CredGroup: "g", Name: "valid", Secret: valid, Enabled: true},
			{ID: 3, MerchantID: 2, CredGroup: "g", Name: "static", Secret: []byte(`{"api_key":"sk-abcdefgh"}`), Enabled: true},
		}, nil
	}
	s := New(f, WithClock(func() time.Time { return fixedNow }))
	views, err := s.ListCredentials(context.Background())
	if err != nil {
		t.Fatalf("列出失败：%v", err)
	}
	if len(views) != 3 {
		t.Fatalf("行数 = %d，期望 3", len(views))
	}
	if views[0].Kind != string(credential.KindOAuth) || !views[0].Expired {
		t.Errorf("过期凭据状态不符：%+v", views[0])
	}
	if views[1].Kind != string(credential.KindOAuth) || views[1].Expired {
		t.Errorf("有效凭据状态不符：%+v", views[1])
	}
	if views[2].Kind != string(credential.KindAPI) || views[2].Expired {
		t.Errorf("静态凭据状态不符：%+v", views[2])
	}
}

// assertOAuthSecret 断言写入的 secret 是 oauth 形态且字段正确。
func assertOAuthSecret(t *testing.T, raw []byte, access, refresh, account string) {
	t.Helper()
	parsed, err := credential.ParseSecret(raw)
	if err != nil {
		t.Fatalf("解析写入的 secret 失败：%v", err)
	}
	if parsed.Kind != credential.KindOAuth || parsed.Access != access || parsed.Refresh != refresh || parsed.Account != account {
		t.Fatalf("写入的 secret 不符：%+v", parsed)
	}
}
