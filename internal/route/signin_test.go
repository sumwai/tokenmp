package route

import (
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// TestParseSigninHeader 覆盖渠道 config 里信标头映射的宽容解析。
func TestParseSigninHeader(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want domain.SigninHeader
	}{
		{name: "未配置", raw: "", want: domain.SigninHeader{}},
		{name: "JSON null", raw: "null", want: domain.SigninHeader{}},
		{
			name: "完整声明",
			raw:  `{"signin_header":{"name":"x-login-state","values":{"expired":"expired","kept":"kept","renewed":"renewed"}}}`,
			want: domain.SigninHeader{Name: "x-login-state", Expired: "expired", Kept: "kept", Renewed: "renewed"},
		},
		{
			name: "头名两侧空白被裁掉",
			raw:  `{"signin_header":{"name":"  x-login-state  ","values":{"expired":"e"}}}`,
			want: domain.SigninHeader{Name: "x-login-state", Expired: "e"},
		},
		{
			name: "只有部分取值",
			raw:  `{"signin_header":{"name":"x-login-state","values":{"kept":"ok"}}}`,
			want: domain.SigninHeader{Name: "x-login-state", Kept: "ok"},
		},
		{
			name: "未知键照常忽略",
			raw:  `{"other_key":1,"signin_header":{"name":"x-login-state","values":{"expired":"e"},"extra":true}}`,
			want: domain.SigninHeader{Name: "x-login-state", Expired: "e"},
		},
		{name: "缺少头名", raw: `{"signin_header":{"values":{"expired":"e"}}}`, want: domain.SigninHeader{}},
		{name: "头名全空白", raw: `{"signin_header":{"name":"   ","values":{"expired":"e"}}}`, want: domain.SigninHeader{}},
		{name: "非法 JSON", raw: `{"signin_header":`, want: domain.SigninHeader{}},
		{name: "结构类型不符", raw: `{"signin_header":"x"}`, want: domain.SigninHeader{}},
		{name: "取值类型不符", raw: `{"signin_header":{"name":1}}`, want: domain.SigninHeader{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSigninHeader([]byte(tt.raw)); got != tt.want {
				t.Fatalf("parseSigninHeader = %+v，期望 %+v", got, tt.want)
			}
		})
	}
}

// TestRouteOfCarriesSigninHeader 断言选路结果带上渠道 config 声明的信标头映射。
func TestRouteOfCarriesSigninHeader(t *testing.T) {
	candidate := store.RouteCandidate{
		ChannelID: 7,
		BaseURL:   "https://upstream.example/api/v1",
		CredGroup: "group-a",
		Config:    []byte(`{"signin_header":{"name":"x-login-state","values":{"expired":"E","kept":"K","renewed":"R"}}}`),
	}
	got := routeOf(candidate, domain.ProtocolOpenAIChat)
	want := domain.SigninHeader{Name: "x-login-state", Expired: "E", Kept: "K", Renewed: "R"}
	if got.SigninHeader != want {
		t.Fatalf("SigninHeader = %+v，期望 %+v", got.SigninHeader, want)
	}
}

// TestRouteOfWithoutSigninHeader 断言未声明信标头的渠道映射为零值，判定回退到状态码启发式。
func TestRouteOfWithoutSigninHeader(t *testing.T) {
	candidate := store.RouteCandidate{ChannelID: 7, BaseURL: "https://upstream.example", CredGroup: "group-a"}
	got := routeOf(candidate, domain.ProtocolOpenAIChat)
	if got.SigninHeader.Configured() {
		t.Fatalf("未声明的渠道不应带上信标头配置，实际 %+v", got.SigninHeader)
	}
}
