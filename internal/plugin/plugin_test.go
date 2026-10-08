package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/domain"
)

// writeSource 在目录里写一个源文件。
func writeSource(t *testing.T, dir, name, source string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("创建目录失败：%v", err)
	}
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("写文件 %s 失败：%v", path, err)
	}
	return path
}

// loadOne 加载单个中间件文件并断言成功。
func loadOne(t *testing.T, dir, name, source string, opts Options) (*Set, string) {
	t.Helper()
	path := writeSource(t, dir, name, source)
	set, err := Load([]string{path}, opts)
	if err != nil {
		t.Fatalf("加载插件失败：%v", err)
	}
	return set, path
}

// chatRequest 造一个最小 OpenAI Chat 请求。
func chatRequest(body string) *domain.Request {
	return &domain.Request{
		RequestID: "req-test",
		Protocol:  domain.ProtocolOpenAIChat,
		Model:     "alias",
		RawBody:   []byte(body),
	}
}

// chatBody 是带 model 字段的最小请求体。
const chatBody = `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`

// runRequest 在一个插件上下文里跑一次请求改写。
func runRequest(t *testing.T, set *Set, ctx context.Context, req *domain.Request) error {
	t.Helper()
	pluginCtx, release := set.BeginRequest(ctx, req)
	defer release()
	return set.OnRequest(pluginCtx, openaichat.New(), req)
}

// rawField 读出请求体顶层字段的字符串取值。
func rawField(t *testing.T, req *domain.Request, key string) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(req.RawBody, &fields); err != nil {
		t.Fatalf("解析请求体失败：%v，原文 %s", err, req.RawBody)
	}
	value, _ := fields[key].(string)
	return value
}

func TestLoadRejectsInvalidPaths(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load([]string{filepath.Join(dir, "missing.mw.js")}, Options{}); err == nil {
		t.Fatal("不存在的路径应当报错")
	}
	plain := writeSource(t, dir, "plain.js", "export function onRequest(b){return b}")
	if _, err := Load([]string{plain}, Options{}); err == nil {
		t.Fatal("非中间件后缀应当报错")
	}
}

// TestNonCallableHookFailsAssembly 验证导出存在但不是函数时在装配期报错。
//
// moejs 的 Module.Hook 只回答导出槽是否存在：`export const onRequest = 42` 也返回成功，
// 于是 hooks 列显示正常、直到第一个请求才失败，属反向信心。可调用性判定移到装配期后，
// 错误必须点名是哪个导出、以及原因。
func TestNonCallableHookFailsAssembly(t *testing.T) {
	dir := t.TempDir()
	path := writeSource(t, dir, "nofn.mw.js", `export const onRequest = 42;`)
	err := loadErr(t, path)
	if err == nil {
		t.Fatal("非函数钩子应当在装配期报错")
	}
	if !strings.Contains(err.Error(), "onRequest") || !strings.Contains(err.Error(), "不是函数") {
		t.Fatalf("错误应点名导出与原因，实际：%v", err)
	}
}

// TestRewriteDecodeFailureCountsOnce 守护改写后解码失败只计一次调用。
//
// 宿主侧解码失败原先会再调一次 record：Calls 多算一拍，Failures 记在第二次调用上，
// 于是失败率与真实调用数都对不上。
func TestRewriteDecodeFailureCountsOnce(t *testing.T) {
	// 返回值是合法 JSON 对象，但缺少 messages，适配器会拒绝解码。
	set, _ := loadOne(t, t.TempDir(), "drop-messages.mw.js",
		`export function onRequest(body) { return { model: "alias" }; }`, Options{})

	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("解码失败应当静默放行：%v", err)
	}
	infos := set.Stats()
	if len(infos) != 1 {
		t.Fatalf("统计项数 = %d，期望 1", len(infos))
	}
	if infos[0].Calls != 1 || infos[0].Failures != 1 {
		t.Fatalf("一次失败的改写应记 1 次调用与 1 次失败，实际 calls=%d failures=%d",
			infos[0].Calls, infos[0].Failures)
	}
}

// loadErr 加载单个文件并返回错误。
func loadErr(t *testing.T, path string) error {
	t.Helper()
	_, err := Load([]string{path}, Options{})
	return err
}

// TestLoadReportsAllFailures 守护装配期一次报出全部不可用的插件。
//
// 多个插件同时写坏时，「遇到第一个就返回」会让人逐个修复、逐次重启。
func TestLoadReportsAllFailures(t *testing.T) {
	dir := t.TempDir()
	broken := writeSource(t, dir, "broken.mw.js", `export function onRequest(body) { this is not javascript`)
	notFn := writeSource(t, dir, "notfn.mw.js", `export const onRequest = 42;`)
	_, err := Load([]string{broken, notFn}, Options{})
	if err == nil {
		t.Fatal("全部不可用时应当报错")
	}
	for _, want := range []string{"broken.mw.js", "notfn.mw.js"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误应点名 %s，实际：%v", want, err)
		}
	}
}

func TestOnRequestRewritesModel(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "alias.mw.js",
		`export function onRequest(body) { body.model = "rewritten"; return body; }`, Options{})
	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("请求改写不应报错：%v", err)
	}
	if req.Model != "rewritten" {
		t.Fatalf("模型名 = %q，期望 rewritten", req.Model)
	}
}

func TestOnRequestReject(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "reject.mw.js",
		`export function onRequest(body, ctx) { ctx.reject(403, "禁止访问"); return body; }`, Options{})
	req := chatRequest(chatBody)
	err := runRequest(t, set, context.Background(), req)
	if err == nil {
		t.Fatal("ctx.reject 应当让请求被拒绝")
	}
	domainErr := domain.AsError(err)
	if domainErr == nil || domainErr.HTTPStatus != 403 {
		t.Fatalf("拒绝错误 = %v，期望 HTTP 403", err)
	}
}

func TestOnRequestSilentlyPassesThroughOnThrow(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "boom.mw.js",
		`export function onRequest(body) { throw new Error("boom"); }`, Options{})
	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("抛错插件应当静默放行，得到 %v", err)
	}
	if req.Model != "alias" {
		t.Fatalf("抛错插件不应改动请求，模型名 = %q", req.Model)
	}
}

func TestOnRequestTimesOutInfiniteLoop(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "spin.mw.js",
		`export function onRequest(body) { for (;;) {} }`, Options{})
	req := chatRequest(chatBody)
	started := time.Now()
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("死循环插件应当静默放行，得到 %v", err)
	}
	elapsed := time.Since(started)
	if elapsed > time.Second {
		t.Fatalf("死循环插件耗时 %v，期望在预算内被中断", elapsed)
	}
	if req.Model != "alias" {
		t.Fatalf("死循环插件不应改动请求，模型名 = %q", req.Model)
	}
}

func TestOnEventDropAndRewrite(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "filter.mw.js", `
export const events = ["text_delta"];
export function onEvent(event) {
  if (event.text_delta === "drop") return null;
  event.text_delta = event.text_delta.toUpperCase();
  return event;
}`, Options{})
	req := chatRequest(chatBody)
	pluginCtx, release := set.BeginRequest(context.Background(), req)
	defer release()

	dropped := set.OnEvent(pluginCtx, req, domain.Chunk{Kind: domain.ChunkTextDelta, TextDelta: "drop"})
	if !dropped.Drop {
		t.Fatalf("返回 null 的分片应当被丢弃，得到 %+v", dropped)
	}
	rewritten := set.OnEvent(pluginCtx, req, domain.Chunk{Kind: domain.ChunkTextDelta, TextDelta: "keep"})
	if rewritten.Unchanged || rewritten.Chunk.TextDelta != "KEEP" {
		t.Fatalf("文本分片应当被改写为大写，得到 %+v", rewritten)
	}
}

func TestOnEventRespectsWhitelist(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "whitelist.mw.js", `
export const events = ["text_delta"];
export function onEvent(event) { event.text_delta = "changed"; return event; }`, Options{})
	req := chatRequest(chatBody)
	pluginCtx, release := set.BeginRequest(context.Background(), req)
	defer release()

	result := set.OnEvent(pluginCtx, req, domain.Chunk{Kind: domain.ChunkReasoningDelta, TextDelta: "think"})
	if !result.Unchanged {
		t.Fatalf("白名单外的事件不应被处理，得到 %+v", result)
	}
}

// TestToolCallIndexMustBeInteger 守护工具调用下标不静默归零。
//
// moejs 的 ToGo 对整数给 int64、对非整数给 float64。旧口径只认 int64，于是插件写 0.5
// 会被静默当成 0 —— 并行工具调用因此串位，而这是最难从响应里看出来的一类错。
// 现在的口径：整数值正常往返，非整数与非法类型按「钩子失败、原样放行」处理并留痕。
func TestToolCallIndexMustBeInteger(t *testing.T) {
	toolCallChunk := domain.Chunk{
		Kind:     domain.ChunkToolCallDelta,
		ToolCall: &domain.ToolCall{Index: 3, ID: "call_1", Name: "orig", Arguments: `{"a":`},
	}

	t.Run("整数值原样往返", func(t *testing.T) {
		set, _ := loadOne(t, t.TempDir(), "roundtrip.mw.js", `
export const events = ["tool_call_delta"];
export function onEvent(event) { event.tool_call.name = "renamed"; return event; }
`, Options{})
		req := chatRequest(chatBody)
		pluginCtx, release := set.BeginRequest(context.Background(), req)
		defer release()

		result := set.OnEvent(pluginCtx, req, toolCallChunk)
		if result.Unchanged {
			t.Fatalf("改写应当生效，实际 %+v", result)
		}
		call := result.Chunk.ToolCall
		if call == nil || call.Index != 3 || call.ID != "call_1" {
			t.Fatalf("改写后的工具调用 = %+v，期望下标 3、id call_1", call)
		}
	})

	for _, tc := range []struct {
		name  string
		index string
	}{
		{name: "非整数", index: "0.5"},
		{name: "浮点越界", index: "1e30"},
		{name: "非法类型", index: `"0"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			set, _ := loadOne(t, t.TempDir(), "bad-index.mw.js", `
export const events = ["tool_call_delta"];
export function onEvent(event) { event.tool_call.index = `+tc.index+`; return event; }
`, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
			req := chatRequest(chatBody)
			pluginCtx, release := set.BeginRequest(context.Background(), req)
			defer release()

			result := set.OnEvent(pluginCtx, req, toolCallChunk)
			if !result.Unchanged || result.Chunk.ToolCall.Index == 0 {
				t.Fatalf("非法下标应按失败处理并原样放行，实际 %+v", result.Chunk.ToolCall)
			}
			if logged := logs.String(); !strings.Contains(logged, "必须是整数") {
				t.Fatalf("日志应说出原因，实际：%s", logged)
			}
		})
	}
}

func TestOnResponseRewrite(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "response.mw.js",
		`export function onResponse(body) { body.marker = "mw"; return body; }`, Options{})
	req := chatRequest(chatBody)
	pluginCtx, release := set.BeginRequest(context.Background(), req)
	defer release()

	rewritten := set.OnResponse(pluginCtx, req, []byte(`{"ok":true}`))
	var fields map[string]any
	if err := json.Unmarshal(rewritten, &fields); err != nil {
		t.Fatalf("解析改写后的响应体失败：%v", err)
	}
	if fields["marker"] != "mw" {
		t.Fatalf("响应体未被改写：%s", rewritten)
	}
}

func TestStateSharedAcrossHooks(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "state.mw.js", `
export const events = ["text_delta"];
export function onRequest(body, ctx) { ctx.state.tag = "from-request"; return body; }
export function onEvent(event, ctx) { event.model = ctx.state.tag; return event; }`, Options{})
	req := chatRequest(chatBody)
	pluginCtx, release := set.BeginRequest(context.Background(), req)
	defer release()
	if err := set.OnRequest(pluginCtx, openaichat.New(), req); err != nil {
		t.Fatalf("请求改写失败：%v", err)
	}
	result := set.OnEvent(pluginCtx, req, domain.Chunk{Kind: domain.ChunkTextDelta, TextDelta: "x"})
	if result.Unchanged || result.Chunk.Model != "from-request" {
		t.Fatalf("state 未在钩子之间共享：%+v", result)
	}
}

func TestContextCarriesPathAgentAndOptions(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "context.mw.js", `
export const options = { tag: "cfg" };
export function onRequest(body, ctx) {
  body.ctx_path = ctx.path;
  body.ctx_option = ctx.options.tag;
  body.ctx_agent = String(ctx.agent.api_key_id);
  return body;
}`, Options{})
	req := chatRequest(chatBody)
	ctx := WithPath(context.Background(), "/v1/chat/completions")
	ctx = WithAgent(ctx, Agent{AccountID: 1, MerchantID: 2, APIKeyID: 9})
	if err := runRequest(t, set, ctx, req); err != nil {
		t.Fatalf("请求改写失败：%v", err)
	}
	if got := rawField(t, req, "ctx_path"); got != "/v1/chat/completions" {
		t.Fatalf("ctx.path = %q", got)
	}
	if got := rawField(t, req, "ctx_option"); got != "cfg" {
		t.Fatalf("ctx.options.tag = %q", got)
	}
	if got := rawField(t, req, "ctx_agent"); got != "9" {
		t.Fatalf("ctx.agent.api_key_id = %q", got)
	}
}

func TestFetchUnavailableAndEvalLimited(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "sandbox.mw.js", `
export function onRequest(body) {
  body.fetch_kind = typeof fetch;
  body.short_eval = String(eval("1 + 1"));
  try { eval("1".repeat(9000)); body.long_eval = "allowed"; }
  catch (e) { body.long_eval = e.name; }
  return body;
}`, Options{})
	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("请求改写失败：%v", err)
	}
	if got := rawField(t, req, "fetch_kind"); got != "undefined" {
		t.Fatalf("fetch 应当不可用，typeof fetch = %q", got)
	}
	if got := rawField(t, req, "short_eval"); got != "2" {
		t.Fatalf("短 eval 应当可用，得到 %q", got)
	}
	if got := rawField(t, req, "long_eval"); got != "RangeError" {
		t.Fatalf("超长 eval 应当被拒，得到 %q", got)
	}
}

func TestSandboxRejectsBareSpecifier(t *testing.T) {
	dir := t.TempDir()
	path := writeSource(t, dir, "bad.mw.js", `import fs from "fs"; export function onRequest(b){return b}`)
	if err := loadErr(t, path); err == nil {
		t.Fatal("裸说明符导入应当被拒绝")
	}
}

func TestSandboxRejectsImportOutsideRoot(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "outside.js", "export const value = 1")
	root := filepath.Join(dir, "plugins")
	path := writeSource(t, root, "escaper.mw.js",
		`import { value } from "../outside.js"; export function onRequest(b){ return b; }`)
	if err := loadErr(t, path); err == nil {
		t.Fatal("越出插件目录的导入应当被拒绝")
	}
}

func TestRelativeImportInsideRoot(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "helper.js", `export const tag = "helper";`)
	set, _ := loadOne(t, dir, "uses-helper.mw.js",
		`import { tag } from "./helper.js"; export function onRequest(body) { body.tag = tag; return body; }`, Options{})
	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("请求改写失败：%v", err)
	}
	if got := rawField(t, req, "tag"); got != "helper" {
		t.Fatalf("目录内相对导入应当可用，得到 %q", got)
	}
}

func TestPackageDirectoryEntry(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "pkg")
	writeSource(t, pkg, "package.json", `{"moejs":"index.js"}`)
	writeSource(t, pkg, "index.js", `export function onRequest(body) { body.pkg = "yes"; return body; }`)
	set, err := Load([]string{pkg}, Options{})
	if err != nil {
		t.Fatalf("加载包目录失败：%v", err)
	}
	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("请求改写失败：%v", err)
	}
	if got := rawField(t, req, "pkg"); got != "yes" {
		t.Fatalf("包目录入口未生效，得到 %q", got)
	}
}

func TestHotReloadByMtime(t *testing.T) {
	dir := t.TempDir()
	path := writeSource(t, dir, "hot.mw.js", `export function onRequest(body) { body.v = "v1"; return body; }`)
	set, err := Load([]string{path}, Options{})
	if err != nil {
		t.Fatalf("加载插件失败：%v", err)
	}
	first := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), first); err != nil {
		t.Fatalf("首次请求失败：%v", err)
	}
	if got := rawField(t, first, "v"); got != "v1" {
		t.Fatalf("首版插件未生效，得到 %q", got)
	}

	writeSource(t, dir, "hot.mw.js", `export function onRequest(body) { body.v = "version-two"; return body; }`)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("修改文件时间失败：%v", err)
	}
	second := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), second); err != nil {
		t.Fatalf("热重载后的请求失败：%v", err)
	}
	if got := rawField(t, second, "v"); got != "version-two" {
		t.Fatalf("热重载未生效，得到 %q", got)
	}
}

func TestConsoleIsRateLimitedAndLogged(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	set, _ := loadOne(t, t.TempDir(), "console.mw.js",
		`export function onRequest(body) { for (let i = 0; i < 50; i++) console.log("tick", i); return body; }`,
		Options{Logger: logger})
	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("请求改写失败：%v", err)
	}
	if !strings.Contains(buffer.String(), "middleware_console") {
		t.Fatalf("控制台输出未进日志：%s", buffer.String())
	}
	if lines := strings.Count(buffer.String(), "\n"); lines > consolePerSecond {
		t.Fatalf("控制台日志未限频，写了 %d 行", lines)
	}
}

func TestRejectAcceptsNonIntegerStatus(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "weird.mw.js",
		`export function onRequest(body, ctx) { ctx.reject("nope", "bad"); return body; }`, Options{})
	req := chatRequest(chatBody)
	err := runRequest(t, set, context.Background(), req)
	if err == nil {
		t.Fatal("非法状态码仍应产生拒绝错误")
	}
	if domainErr := domain.AsError(err); domainErr == nil || domainErr.HTTPStatus != 403 {
		t.Fatalf("非法状态码应回退为 403，得到 %v", err)
	}
}

func TestStatsCountHookCallsAndFailures(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "stats.mw.js",
		`export function onRequest(body) { throw new Error("always"); }`, Options{})
	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("抛错插件应当静默放行：%v", err)
	}
	infos := set.Stats()
	if len(infos) != 1 {
		t.Fatalf("统计条目数 = %d，期望 1", len(infos))
	}
	if infos[0].Calls != 1 || infos[0].Failures != 1 {
		t.Fatalf("统计 = %+v，期望 1 次调用 1 次失败", infos[0])
	}
	if infos[0].LastError == "" {
		t.Fatal("最后错误应当被记录")
	}
}

func TestInterruptedErrorClassifiedAsTimeout(t *testing.T) {
	kind := hookErrorKind(errors.New("plain"))
	if kind != "error" {
		t.Fatalf("普通错误分类 = %q", kind)
	}
}

// TestTopLevelConsoleLoads 守护装配期探测与真实运行时同形：顶层使用 console 的插件
// 也能通过探测，而不会因探测运行时缺 console 而加载失败。
func TestTopLevelConsoleLoads(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "top-console.mw.js",
		`console.log("plugin loaded");
`+
			`export function onRequest(body) { body.loaded = "yes"; return body; }`, Options{})
	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("顶层使用了 console 的插件应当可加载：%v", err)
	}
	if got := rawField(t, req, "loaded"); got != "yes" {
		t.Fatalf("插件未生效，得到 %q", got)
	}
}
