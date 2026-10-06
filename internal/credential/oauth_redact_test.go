package credential

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// 本文件断言订阅型凭据的令牌不会经打印或结构化日志泄露。

// TestOAuthTypesRedactTokens 断言 OAuth 相关类型经 %v 或 %+v 打印时只出现占位符。
func TestOAuthTypesRedactTokens(t *testing.T) {
	credential := OAuthCredential{
		Access:  "secret-access",
		Refresh: "secret-refresh",
		Expires: time.Unix(1_700_000_000, 0),
		Account: "acct-1",
	}
	input := OAuthRefreshInput{
		CredentialID: 1,
		Name:         "primary",
		Access:       "secret-access",
		Refresh:      "secret-refresh",
		Account:      "acct-1",
	}
	output := OAuthRefreshOutput{Access: "secret-access", Refresh: "secret-refresh"}
	secret := Secret{Kind: KindOAuth, Access: "secret-access", Refresh: "secret-refresh"}

	for name, value := range map[string]any{
		"OAuthCredential":    credential,
		"OAuthRefreshInput":  input,
		"OAuthRefreshOutput": output,
		"Secret":             secret,
	} {
		rendered := fmt.Sprintf("%v %+v", value, value)
		if strings.Contains(rendered, "secret-access") || strings.Contains(rendered, "secret-refresh") {
			t.Errorf("%s 打印泄露令牌：%s", name, rendered)
		}
	}
}

// TestOAuthTypesRedactInSlog 断言令牌作为结构化日志字段时只出现占位符。
func TestOAuthTypesRedactInSlog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("续期",
		"credential", OAuthCredential{Access: "secret-access", Refresh: "secret-refresh"},
		"input", OAuthRefreshInput{Access: "secret-access", Refresh: "secret-refresh"},
		"output", OAuthRefreshOutput{Access: "secret-access", Refresh: "secret-refresh"},
		"secret", Secret{Kind: KindOAuth, Access: "secret-access", Refresh: "secret-refresh"},
	)
	if strings.Contains(buf.String(), "secret-access") || strings.Contains(buf.String(), "secret-refresh") {
		t.Fatalf("结构化日志泄露令牌：%s", buf.String())
	}
}

// TestParseSecretOAuth 断言 oauth 形态解析出各字段，expires 按 epoch 毫秒还原。
func TestParseSecretOAuth(t *testing.T) {
	expires := time.UnixMilli(1_700_000_000_123)
	raw, err := BuildOAuthSecret("access-1", "refresh-1", expires, "acct-1")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	parsed, err := ParseSecret(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if parsed.Kind != KindOAuth || parsed.Access != "access-1" || parsed.Refresh != "refresh-1" || parsed.Account != "acct-1" {
		t.Fatalf("解析结果不符：%+v", parsed)
	}
	if !parsed.Expires.Equal(expires) {
		t.Fatalf("过期时刻 = %s，期望 %s", parsed.Expires, expires)
	}
}

// TestParseSecretAPIUnchanged 断言 api 形态与缺省形态解析一致，且带 type:api 也接受。
func TestParseSecretAPIUnchanged(t *testing.T) {
	for _, raw := range []string{`{"api_key":"sk-1"}`, `{"type":"api","api_key":"sk-1"}`} {
		parsed, err := ParseSecret([]byte(raw))
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", raw, err)
		}
		if parsed.Kind != KindAPI || parsed.APIKey != "sk-1" {
			t.Fatalf("解析 %s = %+v", raw, parsed)
		}
	}
}

// TestBuildOAuthSecretZeroExpiry 断言未知有效期写 0，解析回来是 epoch。
func TestBuildOAuthSecretZeroExpiry(t *testing.T) {
	raw, err := BuildOAuthSecret("access-1", "refresh-1", time.Time{}, "acct-1")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	parsed, err := ParseSecret(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !parsed.Expires.Equal(time.UnixMilli(0)) {
		t.Fatalf("过期时刻 = %s，期望 epoch", parsed.Expires)
	}
}

// TestMaskSecretOAuthUsesAccess 断言 oauth 形态的展示前缀取自访问令牌。
func TestMaskSecretOAuthUsesAccess(t *testing.T) {
	raw, err := BuildOAuthSecret("access-abcdefgh", "refresh-1", time.Time{}, "acct-1")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	got := MaskSecret(raw)
	if !strings.HasPrefix(got, "access-a") {
		t.Fatalf("前缀 = %q，期望取自访问令牌", got)
	}
	if strings.Contains(got, "refresh-1") {
		t.Fatalf("前缀泄露刷新令牌：%q", got)
	}
}
