package route

import (
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖渠道 config 里 OAuth 端点画像的解析：可用画像被识别，写坏或缺键的
// 配置退化为未声明，且选路结果把画像带给数据面。

// TestParseOAuthProfile 断言完整配置解析出全部字段并去掉首尾空白。
func TestParseOAuthProfile(t *testing.T) {
	raw := []byte(`{
		"signin_header": {"name": "x-signin"},
		"oauth": {
			"authorize_url": " https://vendor.example.com/authorize ",
			"token_url": "https://vendor.example.com/token",
			"device_url": "https://vendor.example.com/device",
			"client_id": "client-1",
			"scope": "read write"
		},
		"unknown_key": 1
	}`)
	profile := ParseOAuthProfile(raw)
	//nolint:gosec // G101：测试用的假端点地址，不是真实凭据。
	want := domain.OAuthProfile{
		AuthorizeURL: "https://vendor.example.com/authorize",
		TokenURL:     "https://vendor.example.com/token",
		DeviceURL:    "https://vendor.example.com/device",
		ClientID:     "client-1",
		Scope:        "read write",
	}
	if profile != want {
		t.Fatalf("画像 = %+v，期望 %+v", profile, want)
	}
	if !profile.Configured() || !profile.SupportsDeviceCode() || !profile.SupportsAuthorizationCode() {
		t.Fatalf("画像应被判定为可用：%+v", profile)
	}
}

// TestParseOAuthProfileDegrades 断言写坏、缺键或缺少必需端点时退化为未声明。
func TestParseOAuthProfileDegrades(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "空配置", raw: nil},
		{name: "JSON null", raw: []byte("null")},
		{name: "非法 JSON", raw: []byte("{not json")},
		{name: "缺 token_url", raw: []byte(`{"oauth":{"authorize_url":"https://a","client_id":"c"}}`)},
		{name: "缺 client_id", raw: []byte(`{"oauth":{"token_url":"https://t"}}`)},
		{name: "oauth 为空对象", raw: []byte(`{"oauth":{}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if profile := ParseOAuthProfile(tt.raw); profile != (domain.OAuthProfile{}) {
				t.Fatalf("应退化为零值，得到 %+v", profile)
			}
		})
	}
}

// TestRouteOfCarriesOAuthProfile 断言选路结果把渠道声明的画像带给数据面。
func TestRouteOfCarriesOAuthProfile(t *testing.T) {
	candidate := store.RouteCandidate{
		ChannelID:   3,
		ChannelType: store.ChannelTypeOpenAIChat,
		BaseURL:     "https://up.example.com",
		CredGroup:   "group-a",
		Config:      []byte(`{"oauth":{"token_url":"https://t","client_id":"c"}}`),
	}
	route := routeOf(candidate, domain.ProtocolOpenAIChat)
	if route.OAuthProfile.TokenURL != "https://t" || route.OAuthProfile.ClientID != "c" {
		t.Fatalf("路由未带上 OAuth 画像：%+v", route.OAuthProfile)
	}
}
