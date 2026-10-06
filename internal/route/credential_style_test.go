package route

import (
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// TestParseCredentialStyle 守护渠道 config 里凭据注入形态的读取口径：宽容、未知不猜。
func TestParseCredentialStyle(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want domain.CredentialHeaderStyle
	}{
		{name: "查询参数形态", raw: `{"credential_style":"query"}`, want: domain.CredentialHeaderQuery},
		{name: "显式请求头形态", raw: `{"credential_style":"x-goog-api-key"}`, want: domain.CredentialHeaderXGoogAPIKey},
		{name: "未配置", raw: ``, want: domain.CredentialHeaderAuto},
		{name: "JSON null", raw: `null`, want: domain.CredentialHeaderAuto},
		{name: "未知取值回退", raw: `{"credential_style":"bogus"}`, want: domain.CredentialHeaderAuto},
		{name: "写坏的 JSON 回退", raw: `{"credential_style":`, want: domain.CredentialHeaderAuto},
		{name: "不认识的键照常忽略", raw: `{"headers":{"x":"y"}}`, want: domain.CredentialHeaderAuto},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCredentialStyle([]byte(tc.raw)); got != tc.want {
				t.Fatalf("parseCredentialStyle(%q) = %q，期望 %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestEndpointURLForGemini 守护端点段为空的协议只回根地址。
//
// Gemini 的端点含模型名，由适配器在调用上游时按本次请求拼；选路只拼到端点段之前。
func TestEndpointURLForGemini(t *testing.T) {
	if got := endpointURL("https://generativelanguage.googleapis.com/v1beta/", domain.ProtocolGeminiGenerate); got != "https://generativelanguage.googleapis.com/v1beta" {
		t.Fatalf("Gemini 上游根地址 = %q", got)
	}
	if got := endpointURL("https://host/api/v3", domain.ProtocolOpenAIChat); got != "https://host/api/v3/chat/completions" {
		t.Fatalf("既有方言端点段应照旧拼接，实际 %q", got)
	}
}

// TestRouteOfGeminiCarriesCredentialStyle 守护选路结果同时带上渠道方言与凭据注入形态。
func TestRouteOfGeminiCarriesCredentialStyle(t *testing.T) {
	candidate := store.RouteCandidate{
		ChannelID:   7,
		ChannelType: store.ChannelTypeGeminiGenerate,
		BaseURL:     "https://generativelanguage.googleapis.com/v1beta",
		CredGroup:   "gemini-group",
		Config:      []byte(`{"credential_style":"query"}`),
	}
	r := routeOf(candidate, domain.ProtocolGeminiGenerate)
	if r.Protocol != domain.ProtocolGeminiGenerate {
		t.Errorf("协议 = %q", r.Protocol)
	}
	if r.BaseURL != candidate.BaseURL {
		t.Errorf("根地址 = %q，期望 %q", r.BaseURL, candidate.BaseURL)
	}
	if r.CredentialHeaderStyle != domain.CredentialHeaderQuery {
		t.Errorf("凭据注入形态 = %q，期望 query", r.CredentialHeaderStyle)
	}
}

// TestRouteChainIncludesGeminiCrossProtocol 守护路由矩阵补上 Gemini 后依旧把可重建的渠道纳入跨协议段。
func TestRouteChainIncludesGeminiCrossProtocol(t *testing.T) {
	cross := []store.RouteCandidate{
		{ChannelID: 41, ChannelType: store.ChannelTypeGeminiGenerate},
	}
	routes := RouteChain(domain.ProtocolOpenAIChat, nil, cross, seqIntN(0))
	if len(routes) != 1 || routes[0].Protocol != domain.ProtocolGeminiGenerate {
		t.Fatalf("跨协议链 = %+v，期望包含 gemini_generate 渠道", routes)
	}
}
