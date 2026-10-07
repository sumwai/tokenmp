package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/domain"
)

// TestScopeSpecAllowsAxes 守护作用域的判定语义：多轴取与、同轴取或、空轴不限制。
func TestScopeSpecAllowsAxes(t *testing.T) {
	cases := []struct {
		name        string
		spec        scopeSpec
		model       string
		protocol    string
		vendor      string
		vendorKnown bool
		want        bool
	}{
		{name: "零值不限制", spec: scopeSpec{}, model: "任意", protocol: "openai_chat", vendorKnown: false, want: true},
		{name: "单轴命中", spec: scopeSpec{models: set("a")}, model: "a", want: true},
		{name: "单轴未命中", spec: scopeSpec{models: set("a")}, model: "b", want: false},
		{name: "同轴取或", spec: scopeSpec{models: set("a", "b")}, model: "b", want: true},
		{name: "多轴取与", spec: scopeSpec{models: set("a"), protocols: set("openai_chat")}, model: "a", protocol: "anthropic_messages", want: false},
		{name: "多轴同时命中", spec: scopeSpec{models: set("a"), protocols: set("openai_chat")}, model: "a", protocol: "openai_chat", want: true},
		{
			name: "厂商轴命中", spec: scopeSpec{vendors: set("minimax")},
			vendor: "minimax", vendorKnown: true, want: true,
		},
		{
			name: "厂商轴未命中", spec: scopeSpec{vendors: set("minimax")},
			vendor: "zai", vendorKnown: true, want: false,
		},
		{
			// 选路之前厂商未知：不能因为声明了 vendors 就把请求体改写一并跳过。
			name: "厂商未知时该轴不参与判定", spec: scopeSpec{vendors: set("minimax")},
			vendor: "", vendorKnown: false, want: true,
		},
		{
			name: "厂商轴与模型轴同时约束", spec: scopeSpec{models: set("a"), vendors: set("minimax")},
			model: "a", vendor: "zai", vendorKnown: true, want: false,
		},
		{name: "取值前后空白不算差异", spec: scopeSpec{models: set("a")}, model: " a ", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.spec.allows(tc.model, tc.protocol, tc.vendor, tc.vendorKnown)
			if got != tc.want {
				t.Fatalf("allows = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// set 把若干取值装成集合。
func set(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

// scopeCtx 造一个带选路事实的插件上下文。
func scopeCtx(t *testing.T, set *Set, req *domain.Request, vendor string) (context.Context, func()) {
	t.Helper()
	ctx := domain.WithRouteFacts(context.Background(), domain.RouteFacts{Vendor: vendor})
	pluginCtx, release := set.BeginRequest(ctx, req)
	return pluginCtx, release
}

// TestScopeSkipsOutOfScopeHooks 守护越界时不调用钩子 —— 用调用计数断言，而不是只看返回值。
//
// 只看返回值分不清「调用了但没改」与「根本没调用」，而省掉整次调用正是作用域的全部意义。
func TestScopeSkipsOutOfScopeHooks(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "scoped.mw.js", `
export const scope = { vendors: ["minimax"] };
export function onResponse(body) { body.marker = "mw"; return body; }
`, Options{})
	req := chatRequest(chatBody)

	inScope, releaseIn := scopeCtx(t, set, req, "minimax")
	defer releaseIn()
	rewritten := set.OnResponse(inScope, req, []byte(`{"ok":true}`))
	var fields map[string]any
	if err := json.Unmarshal(rewritten, &fields); err != nil {
		t.Fatalf("解析响应体失败：%v", err)
	}
	if fields["marker"] != "mw" {
		t.Fatalf("作用域内的请求未被改写：%s", rewritten)
	}

	outOfScope, releaseOut := scopeCtx(t, set, req, "zai")
	defer releaseOut()
	untouched := set.OnResponse(outOfScope, req, []byte(`{"ok":true}`))
	if string(untouched) != `{"ok":true}` {
		t.Fatalf("作用域外的请求不该被改写：%s", untouched)
	}

	stats := set.Stats()
	if len(stats) != 1 {
		t.Fatalf("中间件数 = %d，期望 1", len(stats))
	}
	if stats[0].Calls != 1 {
		t.Fatalf("钩子调用次数 = %d，期望 1（作用域外的那次不该进入 JS 运行时）", stats[0].Calls)
	}
}

// TestScopeAppliesToStreamEnd 守护流末补发同样受作用域约束。
//
// 它每请求被调用一次，且跑在终结路径上；漏掉这个门会让越界的请求也在末尾付一次调用。
func TestScopeAppliesToStreamEnd(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "scoped.mw.js", `
export const scope = { vendors: ["minimax"] };
export function onStreamEnd() { return [{ kind: "text_delta", text_delta: "tail" }]; }
`, Options{})
	req := chatRequest(chatBody)

	outOfScope, releaseOut := scopeCtx(t, set, req, "zai")
	defer releaseOut()
	if flushed := set.OnStreamEnd(outOfScope, req); len(flushed) != 0 {
		t.Fatalf("作用域外不该补发任何分片，实际 %v", flushed)
	}

	inScope, releaseIn := scopeCtx(t, set, req, "minimax")
	defer releaseIn()
	if flushed := set.OnStreamEnd(inScope, req); len(flushed) != 1 {
		t.Fatalf("作用域内应补发一个分片，实际 %v", flushed)
	}
}

// TestScopeVendorAxisIgnoredBeforeRouting 守护 onRequest 上厂商轴不参与判定。
//
// 请求体改写发生在选路之前，那一刻还不知道会走哪条渠道；因为声明了 vendors 就跳过改写，
// 会让「按厂商限定」变成「永不生效」。
func TestScopeVendorAxisIgnoredBeforeRouting(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "request-scope.mw.js", `
export const scope = { vendors: ["minimax"] };
export function onRequest(body) { body.model = "rewritten"; return body; }
`, Options{})
	req := chatRequest(chatBody)

	// 刻意不挂选路事实：与生产路径一致，onRequest 跑在选路之前。
	pluginCtx, release := set.BeginRequest(context.Background(), req)
	defer release()
	if err := set.OnRequest(pluginCtx, openaichat.New(), req); err != nil {
		t.Fatalf("OnRequest 失败：%v", err)
	}
	if req.Model != "rewritten" {
		t.Fatalf("厂商轴不该在选路前拦下请求体改写，实际 model = %q", req.Model)
	}
}

// TestScopeWithoutDeclarationCallsEveryTime 守护未声明 scope 时行为与引入作用域之前一致。
func TestScopeWithoutDeclarationCallsEveryTime(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "plain.mw.js",
		`export function onResponse(body) { body.marker = "mw"; return body; }`, Options{})
	req := chatRequest(chatBody)

	for _, vendor := range []string{"minimax", "zai", ""} {
		pluginCtx, release := scopeCtx(t, set, req, vendor)
		rewritten := set.OnResponse(pluginCtx, req, []byte(`{"ok":true}`))
		release()
		var fields map[string]any
		if err := json.Unmarshal(rewritten, &fields); err != nil {
			t.Fatalf("解析响应体失败：%v", err)
		}
		if fields["marker"] != "mw" {
			t.Fatalf("未声明作用域时每个请求都该被改写（vendor=%q）：%s", vendor, rewritten)
		}
	}
	if stats := set.Stats(); stats[0].Calls != 3 {
		t.Fatalf("钩子调用次数 = %d，期望 3", stats[0].Calls)
	}
}

// TestScopeInvalidShapeIsUnrestricted 守护作用域写坏时按不限制处理并留痕。
//
// 静默失效正是作用域要避免的失败形态：写坏它只该让判定退化为「不限制」，并留下一条告警。
func TestScopeInvalidShapeIsUnrestricted(t *testing.T) {
	var logs bytes.Buffer
	set, _ := loadOne(t, t.TempDir(), "broken-scope.mw.js", `
export const scope = { vendors: "minimax" };
export function onResponse(body) { body.marker = "mw"; return body; }
`, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	req := chatRequest(chatBody)

	pluginCtx, release := scopeCtx(t, set, req, "zai")
	defer release()
	rewritten := set.OnResponse(pluginCtx, req, []byte(`{"ok":true}`))
	if !strings.Contains(string(rewritten), "mw") {
		t.Fatalf("作用域非法时应退化为不限制：%s", rewritten)
	}
	if !strings.Contains(logs.String(), "scope") {
		t.Fatalf("作用域非法应留下告警，实际日志：%s", logs.String())
	}
}

// TestScopeDeclarationsExposed 守护作用域取值可被装配方读到，供启动期漂移检查使用。
func TestScopeDeclarationsExposed(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "declared.mw.js", `
export const scope = { vendors: ["minimax", "zai"], models: ["MiniMax-M3"] };
export function onResponse(body) { return body; }
`, Options{})
	declarations := set.ScopeDeclarations()
	if len(declarations) != 1 {
		t.Fatalf("声明数 = %d，期望 1", len(declarations))
	}
	values := declarations[0].Values
	if len(values[AxisVendors]) != 2 || len(values[AxisModels]) != 1 {
		t.Fatalf("按轴展开的声明 = %v", values)
	}
	if _, ok := values[AxisProtocols]; ok {
		t.Fatalf("未声明的轴不该出现：%v", values)
	}
}

// TestScopeDeclarationsSkipUnscopedMiddleware 守护未声明作用域的中间件不出现在结果里。
func TestScopeDeclarationsSkipUnscopedMiddleware(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "plain.mw.js",
		`export function onResponse(body) { return body; }`, Options{})
	if declarations := set.ScopeDeclarations(); len(declarations) != 0 {
		t.Fatalf("未声明作用域时不该有声明，实际 %v", declarations)
	}
}
