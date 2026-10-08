package plugin

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/domain"
)

// TestAsyncHookIsRejectedWithReason 守护 async 钩子被明确拦下并说清原因。
//
// 钩子必须是同步函数：宿主只取 Runtime.Call 的返回值，不 settle Promise。而 async 是 JS 里
// 最自然的写法，返回的 Promise 经 JSON 序列化是空对象，而形状校验只看首尾字节就会放行 ——
// 在 onResponse 上意味着响应体被换成 {} 直接写给客户端，且流水线不校验改写后的报文。
//
// 所以断言两件事：正文没有被替换（不能静默损坏），以及日志里说出 Promise 与「同步函数」，
// 而不是报一句指向别处的形状错误。
func TestAsyncHookIsRejectedWithReason(t *testing.T) {
	const original = `{"ok":true}`
	cases := []struct {
		name   string
		source string
		call   func(*Set, *domain.Request) string
		want   string
	}{
		{
			name: "onResponse",
			source: `
export async function onResponse(body) { return { marker: "mw" }; }
`,
			call: func(set *Set, req *domain.Request) string {
				pluginCtx, release := scopeCtx(t, set, req, "minimax")
				defer release()
				return string(set.OnResponse(pluginCtx, req, []byte(original)))
			},
			want: original,
		},
		{
			name: "onRequest",
			source: `
export async function onRequest(body) { body.marker = "mw"; return body; }
`,
			call: func(set *Set, req *domain.Request) string {
				pluginCtx, release := scopeCtx(t, set, req, "minimax")
				defer release()
				_ = set.OnRequest(pluginCtx, openaichat.New(), req)
				return string(req.RawBody)
			},
			want: chatBody,
		},
		{
			name: "onEvent",
			source: `
export const events = ["text_delta"];
export async function onEvent(event) { event.text_delta = "x"; return event; }
`,
			call: func(set *Set, req *domain.Request) string {
				pluginCtx, release := scopeCtx(t, set, req, "minimax")
				defer release()
				result := set.OnEvent(pluginCtx, req, domain.Chunk{Kind: domain.ChunkTextDelta, TextDelta: "keep"})
				return result.Chunk.TextDelta
			},
			want: "keep",
		},
		{
			name: "onStreamEnd",
			source: `
export async function onStreamEnd() { return [{ kind: "text_delta", text_delta: "tail" }]; }
`,
			call: func(set *Set, req *domain.Request) string {
				pluginCtx, release := scopeCtx(t, set, req, "minimax")
				defer release()
				chunks := set.OnStreamEnd(pluginCtx, req)
				if len(chunks) == 0 {
					return ""
				}
				return chunks[0].TextDelta
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			set, _ := loadOne(t, t.TempDir(), "async.mw.js", tc.source,
				Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
			got := tc.call(set, chatRequest(chatBody))
			if got != tc.want {
				t.Fatalf("async 钩子不得改写载荷，实际 %q，期望 %q", got, tc.want)
			}
			logged := logs.String()
			if !strings.Contains(logged, "Promise") || !strings.Contains(logged, "同步函数") {
				t.Fatalf("日志应说出 Promise 与同步函数，实际：%s", logged)
			}
		})
	}
}

// TestUnknownEventNameIsReported 守护拼错的事件名被报出。
//
// 「导出 onEvent 却没声明白名单」有明确告警，而「白名单里写了不存在的事件名」原先完全静默：
// hooks 列看起来正常、计数恒不增长。同一份配置里两种反馈强度会把排查方向带偏。
func TestUnknownEventNameIsReported(t *testing.T) {
	var logs bytes.Buffer
	loadOne(t, t.TempDir(), "typo-events.mw.js", `
export const events = ["text_delta", "text-deltas"];
export function onEvent(event) { return event; }
`, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})

	logged := logs.String()
	if !strings.Contains(logged, "text-deltas") {
		t.Fatalf("应报出无法送达的事件名，实际日志：%s", logged)
	}
	// 合法的事件名不该被一并报出来，否则告警无从判断该改哪个。
	if strings.Contains(logged, `"events":"text_delta"`) {
		t.Fatalf("合法事件名不该出现在告警里，实际日志：%s", logged)
	}
}

// TestUnknownScopeAxisIsReported 守护拼错的轴名被报出。
//
// 轴名拼错原先被直接忽略，而无法解析的作用域按「不限制」处理 —— 于是后果是范围变大，
// 与作用域要收窄的初衷相反。告警必须点名轴名，让人知道该改哪一处。
func TestUnknownScopeAxisIsReported(t *testing.T) {
	var logs bytes.Buffer
	set, _ := loadOne(t, t.TempDir(), "typo-axis.mw.js", `
export const scope = { vendors: ["minimax"], protcols: ["openai_chat"] };
export function onResponse(body) { body.marker = "mw"; return body; }
`, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})

	logged := logs.String()
	if !strings.Contains(logged, "protcols") {
		t.Fatalf("应点名无法识别的轴，实际日志：%s", logged)
	}
	// 声明了合法轴却因另一个轴拼错而整体退化为不限制：这是现行口径，本用例只固定可观测性。
	req := chatRequest(chatBody)
	pluginCtx, release := scopeCtx(t, set, req, "minimax")
	defer release()
	rewritten := set.OnResponse(pluginCtx, req, []byte(`{"ok":true}`))
	if !strings.Contains(string(rewritten), "mw") {
		t.Fatalf("作用域非法时按不限制处理，实际：%s", rewritten)
	}
}

// TestTopLevelExceptionLogsLocation 守护顶层抛异常带文件名与行号。
//
// 启动期的语法错误有 file:line:col，而顶层代码抛异常原先只有一句错误文本、没有位置，
// 两头的反馈质量倒挂。
func TestTopLevelExceptionLogsLocation(t *testing.T) {
	dir := t.TempDir()
	path := writeSource(t, dir, "top-throw.mw.js", "throw new Error(\"top-level boom\");\n")
	err := loadErr(t, path)
	if err == nil {
		t.Fatal("顶层抛异常应当加载失败")
	}
	for _, want := range []string{"top-level boom", "top-throw.mw.js:"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误缺少 %q，实际：%v", want, err)
		}
	}
}

// TestHookExceptionLogsStackAndRequestID 守护失败日志带 JS 栈与请求标识。
//
// 引擎把 Exception.Stack 留空，栈挂在错误对象的 stack 属性上；插件层不取栈时日志里
// 只有一句 "Error: boom"，在几十行的插件里等于零信息，也无法与某次转发对上。
func TestHookExceptionLogsStackAndRequestID(t *testing.T) {
	var logs bytes.Buffer
	set, _ := loadOne(t, t.TempDir(), "throw.mw.js", `
export function onRequest(body) {
  throw new Error("boom");
}
`, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})

	req := chatRequest(chatBody)
	pluginCtx, release := scopeCtx(t, set, req, "minimax")
	defer release()
	if err := set.OnRequest(pluginCtx, openaichat.New(), req); err != nil {
		t.Fatalf("钩子抛错应当静默放行：%v", err)
	}

	logged := logs.String()
	for _, want := range []string{"boom", "request_id=req-test", "throw.mw.js:"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("日志缺少 %q，实际：%s", want, logged)
		}
	}
	// 栈的首行与错误文本重复，去掉后才接上帧，否则同一句话出现两遍。
	if !strings.Contains(logged, `boom\n    at onRequest`) {
		t.Fatalf("日志应把帧接在错误文本之后，实际：%s", logged)
	}
	if strings.Contains(logged, `boom\nError: boom`) {
		t.Fatalf("错误文本不应重复，实际：%s", logged)
	}
}

// TestHookFailureLogsAreRateLimited 守护失败日志限频并保留被压掉的条数。
//
// 一次写坏的文件会让每个请求各失败一次：不限频时同一句话把日志刷满，排障现场看不到
// 别的东西。限频又不能把计数丢掉，否则「持续失败」与「偶发失败」看起来一样。
func TestHookFailureLogsAreRateLimited(t *testing.T) {
	var logs bytes.Buffer
	set, _ := loadOne(t, t.TempDir(), "always-throw.mw.js",
		`export function onRequest() { throw new Error("boom"); }`,
		Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})

	const calls = 40
	req := chatRequest(chatBody)
	pluginCtx, release := scopeCtx(t, set, req, "minimax")
	defer release()
	for i := 0; i < calls; i++ {
		if err := set.OnRequest(pluginCtx, openaichat.New(), req); err != nil {
			t.Fatalf("第 %d 次钩子抛错应当静默放行：%v", i, err)
		}
	}

	logged := logs.String()
	recorded := strings.Count(logged, "中间件钩子失败")
	if recorded == 0 {
		t.Fatal("失败日志一条都没记")
	}
	if recorded >= calls {
		t.Fatalf("%d 次调用产生了 %d 条失败日志，未限频", calls, recorded)
	}

	// 被压掉的条数在下一条放行的日志里报出，因此跨过一个限频窗口再触发一次。
	time.Sleep(1100 * time.Millisecond)
	if err := set.OnRequest(pluginCtx, openaichat.New(), req); err != nil {
		t.Fatalf("限频窗口后的钩子抛错应当静默放行：%v", err)
	}
	if !strings.Contains(logs.String(), "suppressed=") {
		t.Fatalf("限频后应报出被压掉的条数，实际：%s", logs.String())
	}
}

// TestRejectIsOnlyAvailableInOnRequest 守护「拒绝」只在请求改写上可用。
//
// reject 只注入 onRequest 的 ctx：其余三个钩子跑在流水线内部，响应可能已经开始写出，
// 用状态码终止请求已不可能。此前它在四个钩子上都注入、却只有 onRequest 读结果，
// 于是在 onEvent / onResponse 里调用既没有状态码也没有日志 —— 作者会以为拒绝生效了。
// 现在的口径是「不在那里注入」：误用当场抛错，走「钩子失败、原样放行」并留下结构化日志。
func TestRejectIsOnlyAvailableInOnRequest(t *testing.T) {
	var logs bytes.Buffer
	set, _ := loadOne(t, t.TempDir(), "reject-late.mw.js", `
export const events = ["text_delta"];
export function onEvent(event, ctx) { ctx.reject(429, "nope"); return event; }
export function onResponse(body, ctx) { ctx.reject(429, "nope"); return body; }
`, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	req := chatRequest(chatBody)
	pluginCtx, release := scopeCtx(t, set, req, "minimax")
	defer release()

	const original = `{"ok":true}`
	if got := string(set.OnResponse(pluginCtx, req, []byte(original))); got != original {
		t.Fatalf("响应体不得被替换，实际 %q", got)
	}
	set.OnEvent(pluginCtx, req, domain.Chunk{Kind: domain.ChunkTextDelta, TextDelta: "x"})

	// 两个钩子各失败一次：误用不再静默，也只是让这一次钩子作废，转发照常。
	if failures := set.Stats()[0].Failures; failures != 2 {
		t.Fatalf("失败数 = %d，期望 2（两个钩子里的 ctx.reject 都应报错）", failures)
	}
	if logged := logs.String(); !strings.Contains(logged, "中间件钩子失败") {
		t.Fatalf("误用应留下结构化日志，实际：%s", logged)
	}
}

// TestRejectStillWorksInOnRequest 守护上一条没有削弱请求改写上的拒绝。
func TestRejectStillWorksInOnRequest(t *testing.T) {
	set, _ := loadOne(t, t.TempDir(), "reject-early.mw.js",
		`export function onRequest(body, ctx) { ctx.reject(429, "quota"); return body; }`, Options{})
	req := chatRequest(chatBody)
	pluginCtx, release := scopeCtx(t, set, req, "minimax")
	defer release()

	err := set.OnRequest(pluginCtx, openaichat.New(), req)
	if err == nil {
		t.Fatal("onRequest 里的 ctx.reject 应当终止请求")
	}
	if !strings.Contains(err.Error(), "quota") {
		t.Fatalf("拒绝原因应出现在错误里，实际：%v", err)
	}
}
