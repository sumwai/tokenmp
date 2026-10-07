package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// 本文件覆盖第三方登录：提供方列表、授权地址、换取会话、
// 账号归并（同主体不重复建号）、state 校验与提供方错误路径。
// 提供方端点用本地假服务模拟，全链不经外网。

// fakeProvider 模拟一个 OAuth 提供方的 token / user / emails 端点。
type fakeProvider struct {
	// emailMode 控制 /user 的邮箱形态：has（直接给）或 null（回落 /emails）。
	emailMode string
	// verified 控制 /emails 里的条目是否已验证。
	verified bool
	// subject 是 /user 返回的稳定标识。
	subject int64
	// subMode 为真时按 Google 形态返回（sub/email 字符串）。
	subMode bool
}

// start 启动假服务并返回装配好的提供方定义。
func (f *fakeProvider) start(t *testing.T, id string) (OAuthProvider, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-" + id})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		if f.subMode {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"sub":   fmt.Sprintf("%d", f.subject),
				"email": "oauth-user@example.com",
			})
			return
		}
		email := any(nil)
		if f.emailMode == "has" {
			email = "octo@example.com"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": f.subject, "login": "octocat", "email": email,
		})
	})
	mux.HandleFunc("/emails", func(w http.ResponseWriter, _ *http.Request) {
		verified := f.verified
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"email": "octo@example.com", "primary": true, "verified": verified},
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	provider := OAuthProvider{
		ID: id, Name: "Fake " + id,
		ClientID: "cid", ClientSecret: "sec",
		RedirectURI:  "https://panel.example.com/auth/callback",
		AuthorizeURL: server.URL + "/authorize",
		TokenURL:     server.URL + "/token",
		UserInfoURL:  server.URL + "/user",
		Scope:        "openid email",
	}
	if id == "github" {
		provider.EmailsURL = server.URL + "/emails"
	}
	return provider, server
}

// authorizeState 走授权端点拿一次 state。
func authorizeState(t *testing.T, e *testEnv, providerID string) string {
	t.Helper()
	status, env := e.do(t, http.MethodGet, PathOAuthPrefix+providerID, nil, "")
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("获取授权地址失败: %d %s", status, env)
	}
	var data oauthAuthorizeData
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析授权地址: %v", err)
	}
	if data.State == "" {
		t.Fatal("授权地址缺 state")
	}
	// 授权地址本身必须携带 client_id、redirect_uri 与 state。
	parsed, err := url.Parse(data.AuthorizeURL)
	if err != nil {
		t.Fatalf("授权地址不可解析: %v", err)
	}
	q := parsed.Query()
	if q.Get("client_id") != "cid" || q.Get("redirect_uri") == "" || q.Get("state") != data.State {
		t.Fatalf("授权地址参数不全: %s", data.AuthorizeURL)
	}
	return data.State
}

// TestProvidersList 断言只列出已配置的登录方式。
func TestProvidersList(t *testing.T) {
	none := newTestEnv(t, Options{})
	status, env := none.do(t, http.MethodGet, PathProviders, nil, "")
	if status != http.StatusOK {
		t.Fatalf("providers 失败: %d", status)
	}
	var data struct {
		Items []providerData `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil || len(data.Items) != 0 {
		t.Fatalf("未配置时应返回空列表: %v %s", err, env)
	}

	fp := &fakeProvider{subMode: true, subject: 1}
	provider, _ := fp.start(t, "google")
	configured := newTestEnv(t, Options{OAuthProviders: []OAuthProvider{provider}})
	_, env = configured.do(t, http.MethodGet, PathProviders, nil, "")
	if err := json.Unmarshal(env["data"], &data); err != nil || len(data.Items) != 1 ||
		data.Items[0].ID != "google" {
		t.Fatalf("应列出 google: %v %s", err, env)
	}
}

// TestOAuthProviderMissing 断言未配置的提供方回 404。
func TestOAuthProviderMissing(t *testing.T) {
	e := newTestEnv(t, Options{})
	status, env := e.do(t, http.MethodGet, PathOAuthPrefix+"github", nil, "")
	if status != http.StatusNotFound || codeOf(t, env) != codeNotFound {
		t.Fatalf("未配置提供方应回 404: %d %s", status, env)
	}
}

// TestOAuthStateRejected 断言伪造或重复使用的 state 回 400。
func TestOAuthStateRejected(t *testing.T) {
	fp := &fakeProvider{emailMode: "has", subject: 42}
	provider, _ := fp.start(t, "github")
	e := newTestEnv(t, Options{OAuthProviders: []OAuthProvider{provider}})

	// 从未签发的 state。
	status, env := e.do(t, http.MethodPost, PathOAuthPrefix+"github/exchange",
		map[string]string{"code": "auth-code", "state": "forged-state"}, "")
	if status != http.StatusBadRequest || codeOf(t, env) != codeBadRequest {
		t.Fatalf("伪造 state 应回 400: %d %s", status, env)
	}

	// 同一 state 只能用一次。
	state := authorizeState(t, e, "github")
	for i := 0; i < 2; i++ {
		status, env = e.do(t, http.MethodPost, PathOAuthPrefix+"github/exchange",
			map[string]string{"code": "auth-code", "state": state}, "")
	}
	if status != http.StatusBadRequest || codeOf(t, env) != codeBadRequest {
		t.Fatalf("重复使用 state 应回 400: %d %s", status, env)
	}
}

// TestOAuthExchangeCreatesAccount 断言首次交换建号、再次交换归并到同一账号。
func TestOAuthExchangeCreatesAccount(t *testing.T) {
	fp := &fakeProvider{emailMode: "has", subject: 42}
	provider, _ := fp.start(t, "github")
	e := newTestEnv(t, Options{OAuthProviders: []OAuthProvider{provider}})

	state := authorizeState(t, e, "github")
	status, env := e.do(t, http.MethodPost, PathOAuthPrefix+"github/exchange",
		map[string]string{"code": "auth-code-1", "state": state}, "")
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("首次交换失败: %d %s", status, env)
	}
	var tokens sessionTokens
	if err := json.Unmarshal(env["data"], &tokens); err != nil {
		t.Fatalf("解析令牌: %v", err)
	}
	if tokens.User.Username != "octo" {
		t.Fatalf("用户名应取自邮箱前缀: %+v", tokens.User)
	}
	firstID := tokens.User.ID

	// 换一台设备再走一次授权：同一主体归并到同一账号，不重复建号。
	state2 := authorizeState(t, e, "github")
	status, env = e.do(t, http.MethodPost, PathOAuthPrefix+"github/exchange",
		map[string]string{"code": "auth-code-2", "state": state2}, "")
	if status != http.StatusOK {
		t.Fatalf("二次交换失败: %d %s", status, env)
	}
	if err := json.Unmarshal(env["data"], &tokens); err != nil {
		t.Fatalf("解析令牌: %v", err)
	}
	if tokens.User.ID != firstID {
		t.Fatalf("同一主体应归并到同一账号: %d != %d", tokens.User.ID, firstID)
	}

	// 账号表只应有一行。
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	if len(e.st.users) != 1 {
		t.Fatalf("账号数应为 1，实际 %d", len(e.st.users))
	}
}

// TestOAuthEmailMissing 断言提供方未给出已验证邮箱时回 401。
func TestOAuthEmailMissing(t *testing.T) {
	fp := &fakeProvider{emailMode: "null", verified: false, subject: 42}
	provider, _ := fp.start(t, "github")
	e := newTestEnv(t, Options{OAuthProviders: []OAuthProvider{provider}})

	state := authorizeState(t, e, "github")
	status, env := e.do(t, http.MethodPost, PathOAuthPrefix+"github/exchange",
		map[string]string{"code": "auth-code", "state": state}, "")
	if status != http.StatusUnauthorized || codeOf(t, env) != codeUnauthorized {
		t.Fatalf("缺已验证邮箱应回 401: %d %s", status, env)
	}
}

// TestOAuthGoogleShape 断言 Google 形态（sub/email 字符串）同样走通。
func TestOAuthGoogleShape(t *testing.T) {
	fp := &fakeProvider{subMode: true, subject: 1001}
	provider, _ := fp.start(t, "google")
	e := newTestEnv(t, Options{OAuthProviders: []OAuthProvider{provider}})

	state := authorizeState(t, e, "google")
	status, env := e.do(t, http.MethodPost, PathOAuthPrefix+"google/exchange",
		map[string]string{"code": "auth-code", "state": state}, "")
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("Google 形态交换失败: %d %s", status, env)
	}
	var tokens sessionTokens
	if err := json.Unmarshal(env["data"], &tokens); err != nil {
		t.Fatalf("解析令牌: %v", err)
	}
	if tokens.User.Username != "oauth-user" {
		t.Fatalf("用户名应取自邮箱前缀: %+v", tokens.User)
	}
}
