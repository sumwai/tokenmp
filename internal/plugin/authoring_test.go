package plugin

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

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
