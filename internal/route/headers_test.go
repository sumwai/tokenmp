package route

import (
	"net/http"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// TestParseHeaders 守护渠道 config 里静态请求头的读取口径：宽容、空头名丢弃。
//
// 「宽容」的含义与 parseCredentialStyle / parseSigninHeader 一致：config 是人工写入的
// JSON 列，任何读不出来的形态都退化为「本渠道没有额外请求头」，不能让整次选路失败。
func TestParseHeaders(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{name: "单个头", raw: `{"headers":{"x-static-header":"1111"}}`, want: map[string]string{"X-Static-Header": "1111"}},
		{name: "多个头", raw: `{"headers":{"x-a":"1","x-b":"2"}}`, want: map[string]string{"X-A": "1", "X-B": "2"}},
		{name: "头名规整为标准形式", raw: `{"headers":{"x-trace-id":"t"}}`, want: map[string]string{"X-Trace-Id": "t"}},
		{name: "空值保留", raw: `{"headers":{"x-a":""}}`, want: map[string]string{"X-A": ""}},
		{name: "未配置", raw: ``, want: nil},
		{name: "空对象", raw: `{}`, want: nil},
		{name: "JSON null", raw: `null`, want: nil},
		{name: "headers 为空对象", raw: `{"headers":{}}`, want: nil},
		{name: "写坏的 JSON", raw: `{"headers":`, want: nil},
		{name: "headers 类型不符", raw: `{"headers":["x"]}`, want: nil},
		{name: "头值类型不符", raw: `{"headers":{"x-a":1}}`, want: nil},
		{name: "空头名丢弃", raw: `{"headers":{"  ":"v","x-keep":"1"}}`, want: map[string]string{"X-Keep": "1"}},
		{name: "全是空头名", raw: `{"headers":{"":"v"}}`, want: nil},
		{name: "保留头名丢弃", raw: `{"headers":{"Authorization":"Bearer x","x-static-header":"1"}}`, want: map[string]string{"X-Static-Header": "1"}},
		{name: "保留头名大小写不敏感", raw: `{"headers":{"content-type":"text/plain"}}`, want: nil},
		{name: "不认识的键照常忽略", raw: `{"credential_style":"query","headers":{"x-a":"1"}}`, want: map[string]string{"X-A": "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseHeaders([]byte(tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("parseHeaders(%q) = %v，期望 %v", tc.raw, got, tc.want)
			}
			for name, want := range tc.want {
				if value := got.Get(name); value != want {
					t.Fatalf("parseHeaders(%q) 的 %s = %q，期望 %q", tc.raw, name, value, want)
				}
			}
		})
	}
}

// TestRouteOfCarriesStaticHeaders 守护静态请求头从渠道 config 落到选路结果。
//
// 落到 Route.Headers 之后才由凭据提供者合并进上游请求头，本测试只守住这一跳。
// 头名在测试里取中性值：真实场景是 opencode-go 的 x-opencode-session 一类固定头，
// 那个字面量会被 gosec 的硬编码凭据启发式命中，而这里考察的是机制不是头名。
func TestRouteOfCarriesStaticHeaders(t *testing.T) {
	candidate := store.RouteCandidate{
		ChannelID:   9,
		ChannelType: store.ChannelTypeOpenAIChat,
		BaseURL:     "https://upstream.example.com/v1",
		CredGroup:   "grp",
		Config:      []byte(`{"headers":{"x-static-header":"1111"}}`),
	}
	r := routeOf(candidate, domain.ProtocolOpenAIChat)
	if got := r.Headers.Get("x-static-header"); got != "1111" {
		t.Fatalf("选路结果的静态请求头 = %q，期望 1111", got)
	}
}

// TestRouteOfWithoutStaticHeadersKeepsNil 守护未配置 headers 时选路结果不带额外请求头。
//
// 零行为变化是这次改动的契约之一：多数渠道不配 headers，它们的上游线报文必须逐字节不变。
func TestRouteOfWithoutStaticHeadersKeepsNil(t *testing.T) {
	candidate := store.RouteCandidate{
		ChannelID:   1,
		ChannelType: store.ChannelTypeOpenAIChat,
		BaseURL:     "https://host/v1",
		CredGroup:   "grp",
	}
	if r := routeOf(candidate, domain.ProtocolOpenAIChat); r.Headers != nil {
		t.Fatalf("未配置 headers 时选路结果的请求头 = %v，期望 nil", r.Headers)
	}
}

// TestWithClientHeaderOverridesClientWins 守护「有则透传」：客户端带了的声明头取客户端值。
func TestWithClientHeaderOverridesClientWins(t *testing.T) {
	client := http.Header{"X-Static-Header": []string{"from-client", "second"}}
	routes := WithClientHeaderOverrides(
		[]domain.Route{{ChannelID: 1, Headers: http.Header{"X-Static-Header": []string{"from-config"}}}},
		client,
	)
	if got := routes[0].Headers.Values("X-Static-Header"); len(got) != 2 || got[0] != "from-client" || got[1] != "second" {
		t.Fatalf("上游静态头 = %v，期望透传客户端的两个取值", got)
	}
}

// TestWithClientHeaderOverridesKeepsDefaultWhenClientSilent 守护「无则兜底」。
func TestWithClientHeaderOverridesKeepsDefaultWhenClientSilent(t *testing.T) {
	routes := WithClientHeaderOverrides(
		[]domain.Route{{ChannelID: 1, Headers: http.Header{"X-Static-Header": []string{"from-config"}}}},
		http.Header{"X-Other": []string{"v"}},
	)
	if got := routes[0].Headers.Get("X-Static-Header"); got != "from-config" {
		t.Fatalf("上游静态头 = %q，期望保留渠道默认值", got)
	}
}

// TestWithClientHeaderOverridesIgnoresUndeclaredClientHeaders 守护透传范围只到声明过的头名。
//
// 声明名即操作者授权的范围。让未声明的客户端头进上游，等于把鉴权头、Cookie 一类
// 全部开放给客户端。
func TestWithClientHeaderOverridesIgnoresUndeclaredClientHeaders(t *testing.T) {
	client := http.Header{"X-Undeclared": []string{"leak"}, "Authorization": []string{"Bearer leaked"}}
	routes := WithClientHeaderOverrides(
		[]domain.Route{{ChannelID: 1, Headers: http.Header{"X-Static-Header": []string{"from-config"}}}},
		client,
	)
	if got := routes[0].Headers.Get("X-Undeclared"); got != "" {
		t.Fatalf("未声明的客户端头进了上游：X-Undeclared = %q", got)
	}
	if got := routes[0].Headers.Get("Authorization"); got != "" {
		t.Fatalf("客户端鉴权头进了上游：Authorization = %q", got)
	}
}

// TestWithClientHeaderOverridesWithoutClientHeadersKeepsRoutes 守护客户端无请求头时不动选路结果。
//
// 无客户端头是常见情形（多数请求不带自定义头），返回原切片避免每次选路都多一次分配。
func TestWithClientHeaderOverridesWithoutClientHeadersKeepsRoutes(t *testing.T) {
	routes := []domain.Route{{ChannelID: 1, Headers: http.Header{"X-Static-Header": []string{"from-config"}}}}
	got := WithClientHeaderOverrides(routes, nil)
	if len(got) != 1 || got[0].Headers.Get("X-Static-Header") != "from-config" {
		t.Fatalf("选路结果被改动：%+v", got)
	}
}

// TestWithClientHeaderOverridesLeavesUndeclaredChannelsAlone 守护声明头为空的渠道不被写入请求头。
func TestWithClientHeaderOverridesLeavesUndeclaredChannelsAlone(t *testing.T) {
	routes := WithClientHeaderOverrides(
		[]domain.Route{{ChannelID: 1, Headers: http.Header{"X-Static-Header": []string{"from-config"}}}, {ChannelID: 2}},
		http.Header{"X-Static-Header": []string{"from-client"}},
	)
	if routes[1].Headers != nil {
		t.Fatalf("未声明静态头的渠道不应被写入请求头：%v", routes[1].Headers)
	}
}
