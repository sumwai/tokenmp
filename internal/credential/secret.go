package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// 本文件是 upstream_credential.secret 的解析与构造口径：管理面写入与数据面读取
// 共用同一份结构定义，避免两处各自演化出不同的字段名。
//
// 凭据有两种形态：
//
//   - api：静态密钥，取 api_key 字段，或整个 JSON 缺 type 时的默认形态；
//   - oauth：订阅型凭据，取 access / refresh / expires / account，由数据面惰性续期。
//
// 存储层不解释结构，只保证是合法 JSON；解释发生在本包。

// Kind 是凭据形态。
type Kind string

const (
	// KindAPI 是静态密钥形态，也是缺省形态。
	KindAPI Kind = "api"
	// KindOAuth 是订阅型 OAuth 形态。
	KindOAuth Kind = "oauth"
)

// Secret 是一行 upstream_credential.secret 解析后的内容。
//
// OAuth 形态的 token 字段只在本包与注入路径里出现，绝不进日志：
// String 与 LogValue 会把整个值替换成占位符。
type Secret struct {
	// Kind 是凭据形态。
	Kind Kind
	// APIKey 是 api 形态的明文密钥。
	APIKey string
	// Access 是 oauth 形态的访问令牌。
	Access string
	// Refresh 是 oauth 形态的刷新令牌。
	Refresh string
	// Expires 是 access 的过期时刻；零值表示未知或不适用。
	Expires time.Time
	// Account 是 oauth 形态的账户标识，仅供人工辨识。
	Account string
}

// secretJSON 是 secret 列的线格式。
type secretJSON struct {
	Type    string `json:"type,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	Access  string `json:"access,omitempty"`
	Refresh string `json:"refresh,omitempty"`
	// Expires 是 epoch 毫秒。用指针区分「未给出」与「0（已过期）」。
	Expires *int64 `json:"expires,omitempty"`
	Account string `json:"account,omitempty"`
}

// ParseSecret 解析一行凭据 secret。
//
// type 缺省或为 "api" 时按静态密钥形态解析；为 "oauth" 时按订阅形态解析；
// 其它取值视为写坏的行并报错，由调用方决定跳过还是失败。
func ParseSecret(raw []byte) (Secret, error) {
	var parsed secretJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Secret{}, fmt.Errorf("凭据 JSON 无法解析: %w", err)
	}
	switch Kind(strings.TrimSpace(parsed.Type)) {
	case KindOAuth:
		if strings.TrimSpace(parsed.Access) == "" && strings.TrimSpace(parsed.Refresh) == "" {
			return Secret{}, errors.New("oauth 凭据缺少 access 与 refresh")
		}
		secret := Secret{
			Kind:    KindOAuth,
			Access:  parsed.Access,
			Refresh: parsed.Refresh,
			Account: strings.TrimSpace(parsed.Account),
		}
		if parsed.Expires != nil {
			secret.Expires = time.UnixMilli(*parsed.Expires)
		}
		return secret, nil
	case KindAPI, "":
		if strings.TrimSpace(parsed.APIKey) == "" {
			return Secret{}, errors.New("凭据缺少 api_key")
		}
		return Secret{Kind: KindAPI, APIKey: parsed.APIKey}, nil
	default:
		return Secret{}, fmt.Errorf("未知的凭据形态 %q", parsed.Type)
	}
}

// BuildAPISecret 构造静态密钥形态的 secret；保持与既有写入完全同形。
func BuildAPISecret(apiKey string) ([]byte, error) {
	//nolint:gosec // G117：这是写入库表的凭据 JSON，字段名由协议约定，不是硬编码凭据。
	return json.Marshal(secretJSON{APIKey: apiKey})
}

// BuildOAuthSecret 构造订阅形态的 secret。
//
// expires 为零值时写 0（epoch），表示该访问令牌已过期；refresh 与 account 原样保留，
// 使「刷新令牌失效被标记」这一事实在管理面可见，且不吞掉后续可能的排查线索。
func BuildOAuthSecret(access, refresh string, expires time.Time, account string) ([]byte, error) {
	millis := expires.UnixMilli()
	if expires.IsZero() {
		millis = 0
	}
	//nolint:gosec // G117：这是写入库表的凭据 JSON，字段名由协议约定，不是硬编码凭据。
	return json.Marshal(secretJSON{
		Type:    string(KindOAuth),
		Access:  access,
		Refresh: refresh,
		Expires: &millis,
		Account: account,
	})
}

// MaskSecret 把一行 secret 脱敏成可展示的前缀。
//
// 解析失败时返回固定掩码而不是原文：宁可看不出这条凭据是什么，
// 也不能把结构不确定的 JSON 片段当明文漏出去。
func MaskSecret(raw []byte) string {
	parsed, err := ParseSecret(raw)
	if err != nil {
		return maskedFull
	}
	if parsed.Kind == KindOAuth {
		return maskPrefix(parsed.Access)
	}
	return maskPrefix(parsed.APIKey)
}

// 脱敏展示口径：前缀足以区分凭据行，又不足以还原明文。
const (
	// visiblePrefixLen 是脱敏后保留的明文字符数。
	visiblePrefixLen = 8
	// maskedFull 是无法展示前缀时的整段掩码。
	maskedFull = "********"
)

// maskPrefix 保留取值的前 visiblePrefixLen 个字符，其余以省略号代替。
func maskPrefix(value string) string {
	if strings.TrimSpace(value) == "" {
		return maskedFull
	}
	if len(value) <= visiblePrefixLen {
		return maskedFull
	}
	return value[:visiblePrefixLen] + "…"
}

// String 实现 fmt.Stringer：凭据经 %v 或 %+v 打印时只出现占位符。
func (s Secret) String() string {
	return redactedSecret
}

// LogValue 实现 slog.LogValuer：凭据作为日志字段时只出现占位符。
func (s Secret) LogValue() slog.Value {
	return slog.StringValue(redactedSecret)
}
