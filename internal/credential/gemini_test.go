package credential

import (
	"context"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
)

// TestUpstreamHeadersInjectsGeminiAPIKey 守护 Gemini 的默认注入形态是 x-goog-api-key。
//
// 该形态既是 Gemini 原生端点的要求，也是渠道未配置注入形态时的现状口径。
func TestUpstreamHeadersInjectsGeminiAPIKey(t *testing.T) {
	const apiKey = "sk-gemini"
	provider := singleRef("ref", Credential{APIKey: apiKey})
	headers, err := provider.UpstreamHeaders(context.Background(),
		domain.Route{CredentialRef: "ref", Protocol: domain.ProtocolGeminiGenerate})
	if err != nil {
		t.Fatalf("UpstreamHeaders 返回错误：%v", err)
	}
	if got := headers.Get("x-goog-api-key"); got != apiKey {
		t.Fatalf("x-goog-api-key = %q，期望 %q", got, apiKey)
	}
	if got := headers.Get("Authorization"); got != "" {
		t.Fatalf("Gemini 形态不应同时注入 Authorization，实际 %q", got)
	}
}

// TestUpstreamQueryOnlyForQueryStyle 守护查询参数形态的凭据注入：
// 只有渠道显式配置 query 时才经 UpstreamQuery 追加 key=，其余形态不改上游地址。
func TestUpstreamQueryOnlyForQueryStyle(t *testing.T) {
	provider := singleRef("ref", Credential{APIKey: "sk-query"})

	values, err := provider.UpstreamQuery(context.Background(),
		domain.Route{CredentialRef: "ref", Protocol: domain.ProtocolGeminiGenerate})
	if err != nil {
		t.Fatalf("默认形态不应报错：%v", err)
	}
	if values != nil {
		t.Fatalf("默认形态不应追加查询参数，实际 %v", values)
	}

	route := domain.Route{
		CredentialRef:         "ref",
		Protocol:              domain.ProtocolGeminiGenerate,
		CredentialHeaderStyle: domain.CredentialHeaderQuery,
	}
	values, err = provider.UpstreamQuery(context.Background(), route)
	if err != nil {
		t.Fatalf("查询参数形态报错：%v", err)
	}
	if got := values.Get("key"); got != "sk-query" {
		t.Fatalf("key = %q，期望 %q", got, "sk-query")
	}

	headers, err := provider.UpstreamHeaders(context.Background(), route)
	if err != nil {
		t.Fatalf("查询参数形态的 UpstreamHeaders 报错：%v", err)
	}
	if got := headers.Get("x-goog-api-key"); got != "" {
		t.Fatalf("查询参数形态不应注入凭据头，实际 %q", got)
	}
}

// TestUpstreamHeadersChannelConfigOverridesProtocol 守护渠道配置优先于协议默认形态。
//
// 同一个 Gemini 协议既可以走原生 x-goog-api-key，也可以走兼容端点的 ?key=，
// 差异来自渠道配置而不是协议本身。
func TestUpstreamHeadersChannelConfigOverridesProtocol(t *testing.T) {
	provider := singleRef("ref", Credential{APIKey: "sk-override"})
	route := domain.Route{
		CredentialRef:         "ref",
		Protocol:              domain.ProtocolGeminiGenerate,
		CredentialHeaderStyle: domain.CredentialHeaderAuthorization,
	}
	headers, err := provider.UpstreamHeaders(context.Background(), route)
	if err != nil {
		t.Fatalf("UpstreamHeaders 返回错误：%v", err)
	}
	if got := headers.Get("Authorization"); got != "Bearer sk-override" {
		t.Fatalf("Authorization = %q，期望渠道配置生效", got)
	}
	if got := headers.Get("x-goog-api-key"); got != "" {
		t.Fatalf("渠道配置覆盖后不应再注入 x-goog-api-key，实际 %q", got)
	}
}

// countingResolver 记录 Resolve 被调用了几次。
type countingResolver struct {
	calls int
	cred  Credential
}

func (r *countingResolver) Resolve(context.Context, domain.Route) (Credential, error) {
	r.calls++
	return r.cred, nil
}

// TestUpstreamQueryStyleResolvesCredentialOnce 守护查询参数形态只解析一次凭据。
//
// 请求头路径与查询参数路径都会有机会解析凭据，但轮换解析每调用一次就推进一条试用序号；
// 同一轮尝试里解析两次会把组内凭据白消耗一条，大组还会提前耗尽试用上限。
func TestUpstreamQueryStyleResolvesCredentialOnce(t *testing.T) {
	resolver := &countingResolver{cred: Credential{APIKey: "sk-once"}}
	provider := NewWithResolver(resolver)
	route := domain.Route{
		Protocol:              domain.ProtocolGeminiGenerate,
		CredentialHeaderStyle: domain.CredentialHeaderQuery,
	}
	if _, err := provider.UpstreamHeaders(context.Background(), route); err != nil {
		t.Fatalf("UpstreamHeaders 返回错误：%v", err)
	}
	if _, err := provider.UpstreamQuery(context.Background(), route); err != nil {
		t.Fatalf("UpstreamQuery 返回错误：%v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("凭据解析次数 = %d，期望 1", resolver.calls)
	}
}
