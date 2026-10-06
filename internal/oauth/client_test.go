package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

// 本文件用进程内假端点覆盖三种 OAuth 交互：刷新令牌、设备码与授权码交换，
// 以及 invalid_grant 的分流与「错误文案不含令牌」的约束。

// testProfile 指向假端点。
func testProfile(server *httptest.Server) domain.OAuthProfile {
	return domain.OAuthProfile{
		AuthorizeURL: server.URL + "/authorize",
		TokenURL:     server.URL + "/token",
		DeviceURL:    server.URL + "/device",
		ClientID:     "client-1",
		Scope:        "read",
	}
}

// TestRefreshRotatesRefreshToken 断言续期返回新访问令牌与端点新发的刷新令牌。
func TestRefreshRotatesRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析表单失败：%v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-old" {
			t.Errorf("刷新请求参数不符：%v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-new","refresh_token":"refresh-new","expires_in":3600,"account":"acct"}`))
	}))
	defer server.Close()

	token, err := New(Options{}).Refresh(context.Background(), testProfile(server), "refresh-old")
	if err != nil {
		t.Fatalf("续期失败：%v", err)
	}
	if token.Access != "access-new" || token.Refresh != "refresh-new" {
		t.Fatalf("令牌不符：%+v", token)
	}
	if token.Account != "acct" {
		t.Errorf("账户标识 = %q，期望 acct", token.Account)
	}
	if token.Expires.IsZero() || time.Until(token.Expires) < time.Hour-time.Minute {
		t.Errorf("过期时刻不符：%s", token.Expires)
	}
}

// TestRefreshKeepsRefreshWhenEndpointOmitsIt 断言端点未回新刷新令牌时沿用旧值。
func TestRefreshKeepsRefreshWhenEndpointOmitsIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"access-new","expires_in":60}`))
	}))
	defer server.Close()

	token, err := New(Options{}).Refresh(context.Background(), testProfile(server), "refresh-old")
	if err != nil {
		t.Fatalf("续期失败：%v", err)
	}
	if token.Refresh != "refresh-old" {
		t.Fatalf("刷新令牌 = %q，期望沿用 refresh-old", token.Refresh)
	}
}

// TestRefreshInvalidGrant 断言 invalid_grant 映射为哨兵错误。
func TestRefreshInvalidGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
	}))
	defer server.Close()

	_, err := New(Options{}).Refresh(context.Background(), testProfile(server), "refresh-old")
	if !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("应返回 ErrInvalidGrant，得到 %v", err)
	}
}

// TestRefreshServerErrorKeepsOldTokensOutOfMessage 断言非授权类失败不被判为凭据失效，
// 且错误文案里不含任何令牌。
func TestRefreshServerErrorKeepsOldTokensOutOfMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`service unavailable`))
	}))
	defer server.Close()

	_, err := New(Options{}).Refresh(context.Background(), testProfile(server), "refresh-secret-value")
	if err == nil {
		t.Fatal("端点 503 应返回错误")
	}
	if errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("503 不应被判为授权失效：%v", err)
	}
	if strings.Contains(err.Error(), "refresh-secret-value") {
		t.Fatalf("错误文案泄露刷新令牌：%v", err)
	}
}

// TestExchangeCode 断言授权码交换把 code 与 client_id 送达端点。
func TestExchangeCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析表单失败：%v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "code-1" {
			t.Errorf("交换请求参数不符：%v", r.Form)
		}
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":120}`))
	}))
	defer server.Close()

	token, err := New(Options{}).ExchangeCode(context.Background(), testProfile(server), "code-1")
	if err != nil {
		t.Fatalf("交换失败：%v", err)
	}
	if token.Access != "access-1" || token.Refresh != "refresh-1" {
		t.Fatalf("令牌不符：%+v", token)
	}
}

// TestAuthorizeURL 断言授权地址带上响应类型、客户端与范围。
func TestAuthorizeURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	got, err := New(Options{}).AuthorizeURL(testProfile(server), "state-1")
	if err != nil {
		t.Fatalf("拼装失败：%v", err)
	}
	for _, want := range []string{"response_type=code", "client_id=client-1", "scope=read", "state=state-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("授权地址缺少 %q：%s", want, got)
		}
	}
}

// TestStartDeviceCode 断言设备码响应的字段被正确解析。
func TestStartDeviceCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device" {
			t.Errorf("设备码请求路径 = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"device_code":"dc-1","user_code":"uc-1","verification_uri":"https://verify","verification_uri_complete":"https://verify?code=uc-1","interval":2,"expires_in":600}`))
	}))
	defer server.Close()

	device, err := New(Options{}).StartDeviceCode(context.Background(), testProfile(server))
	if err != nil {
		t.Fatalf("发起设备码失败：%v", err)
	}
	if device.DeviceCode != "dc-1" || device.UserCode != "uc-1" || device.VerificationURI != "https://verify" {
		t.Fatalf("设备码响应不符：%+v", device)
	}
	if device.Interval != 2*time.Second || device.ExpiresIn != 10*time.Minute {
		t.Errorf("间隔/有效期不符：%+v", device)
	}
}

// TestPollDeviceCode 断言轮询跳过 authorization_pending、对 slow_down 加长间隔，最终拿到令牌。
func TestPollDeviceCode(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		case 2:
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
		default:
			_, _ = w.Write([]byte(`{"access_token":"access-device","refresh_token":"refresh-device","expires_in":60}`))
		}
	}))
	defer server.Close()

	token, err := New(Options{}).PollDeviceCode(context.Background(), testProfile(server), DeviceCode{
		DeviceCode: "dc-1",
		Interval:   time.Millisecond,
		ExpiresIn:  time.Minute,
	})
	if err != nil {
		t.Fatalf("轮询失败：%v", err)
	}
	if token.Access != "access-device" || token.Refresh != "refresh-device" {
		t.Fatalf("令牌不符：%+v", token)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("轮询次数 = %d，期望 3", got)
	}
}

// TestPollDeviceCodeDenied 断言授权被拒时立即返回，不再轮询。
func TestPollDeviceCodeDenied(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"error":"access_denied","error_description":"user denied"}`))
	}))
	defer server.Close()

	_, err := New(Options{}).PollDeviceCode(context.Background(), testProfile(server), DeviceCode{
		DeviceCode: "dc-1",
		Interval:   time.Millisecond,
		ExpiresIn:  time.Minute,
	})
	if err == nil {
		t.Fatal("被拒应返回错误")
	}
	if calls.Load() != 1 {
		t.Fatalf("被拒后不应继续轮询，实际 %d 次", calls.Load())
	}
}
