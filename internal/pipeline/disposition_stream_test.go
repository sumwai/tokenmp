package pipeline

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/anthropic"
	"github.com/sumwai/tokenmp/internal/adapters/gemini"
	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/adapters/openairesponses"
	"github.com/sumwai/tokenmp/internal/domain"
)

// 本文件覆盖一类只有流式才有的场景：**上游把失败信息写在流里**。
//
// 与「非 2xx 直接报错」不同，这时状态码是 200，网关已经在往客户端写字节了。
// 能不能换渠道，全看失败发生在写出第一个字节之前还是之后 —— 而这个边界由
// rebuildSink 的 startFrame 延迟发送与 passthroughSink 的 wrote 标志共同决定，
// 是两条不同的代码路径，必须分别盯住。

// sseScript 是一段流式脚本。
type sseScript struct {
	// frames 按顺序下发；每项作为一条 data 段。
	frames []string
	// headers 额外的响应头，例如 Retry-After。
	headers http.Header
	// omitDone 为真时结尾不补 [DONE]，用于模拟上游未发结束信号就断开。
	omitDone bool
}

// sseUpstream 按脚本下发 SSE 流，并记录调用次数与收到的凭据。
type sseUpstream struct {
	server *httptest.Server

	mu    sync.Mutex
	calls int
	keys  []string
}

// newSSEUpstream 起一个按脚本下发 SSE 的假上游。
func newSSEUpstream(t *testing.T, script sseScript) *sseUpstream {
	t.Helper()
	upstream := &sseUpstream{}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.mu.Lock()
		upstream.calls++
		upstream.keys = append(upstream.keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		upstream.mu.Unlock()

		for name, values := range script.headers {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, frame := range script.frames {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if !script.omitDone {
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// callCount 返回收到的请求数。
func (u *sseUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// 一个可解码的内容帧与一个结束帧，构成一段正常的流。
const (
	sseContentFrame = `{"id":"1","model":"up-model","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`
	sseFinishFrame  = `{"id":"1","model":"up-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
)

// TestStreamFailureOnFirstFrameStillSwitchesRoute 覆盖「上游把限流写在第一条流式响应里」。
//
// 这时状态码是 200，客户端已经连上了，但网关还没写出任何字节。可挽救窗口就是这个边界：
// 首个字节写出之前仍能换渠道，因此应当整体成功，客户端看到一段完整的流。
func TestStreamFailureOnFirstFrameStillSwitchesRoute(t *testing.T) {
	cases := []struct {
		name  string
		frame string
	}{
		{name: "首帧报限流", frame: `{"error":{"message":"Rate limit reached for requests"}}`},
		{name: "首帧报过载", frame: `{"error":{"type":"overloaded_error","message":"Overloaded"}}`},
		{name: "首帧报上游故障", frame: `{"error":{"message":"internal server error"}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broken := newSSEUpstream(t, sseScript{frames: []string{tc.frame}})
			healthy := newSSEUpstream(t, sseScript{frames: []string{sseContentFrame, sseFinishFrame}})
			fixture := newDispositionFixture(t, []string{broken.server.URL, healthy.server.URL}, 30*time.Second)

			written, err := fixture.runStream(t)

			if broken.callCount() != 1 {
				t.Errorf("故障渠道调用次数 = %d，期望 1", broken.callCount())
			}
			if healthy.callCount() != 1 {
				t.Errorf("首帧失败且尚未写出字节时应换渠道重试；健康渠道调用次数 = %d，期望 1", healthy.callCount())
			}
			if err != nil {
				t.Fatalf("换渠道后应整体成功，实际 %v", err)
			}
			if !strings.Contains(written, "ok") {
				t.Errorf("客户端未收到正文：%q", written)
			}
			if !strings.Contains(written, "[DONE]") {
				t.Errorf("客户端未收到结束哨兵：%q", written)
			}
		})
	}
}

// TestStreamFailureAfterContentIsTerminal 覆盖可挽救窗口的另一侧：已经写出正文后再失败。
//
// 客户端拿到的是半截流，网关能做的只有按协议降级 —— 不能换渠道，因为换了客户端会看到
// 两段内容拼接。降级方式**因协议而异**：没有流式错误帧的协议（Chat Completions）只能
// 直接关闭连接，有该帧的协议才下发结构化错误。本用例把 openai_chat 这一侧的边界钉住，
// 协议矩阵由 TestStreamErrorFrameSupportDiffersByProtocol 覆盖。
func TestStreamFailureAfterContentIsTerminal(t *testing.T) {
	broken := newSSEUpstream(t, sseScript{frames: []string{sseContentFrame, `{"error":{"message":"Rate limit reached"}}`}})
	healthy := newSSEUpstream(t, sseScript{frames: []string{sseContentFrame, sseFinishFrame}})
	fixture := newDispositionFixture(t, []string{broken.server.URL, healthy.server.URL}, 30*time.Second)

	written, err := fixture.runStream(t)

	if broken.callCount() != 1 {
		t.Errorf("故障渠道调用次数 = %d，期望 1", broken.callCount())
	}
	if healthy.callCount() != 0 {
		t.Errorf("已写出正文后不得换渠道，健康渠道调用次数 = %d，期望 0", healthy.callCount())
	}
	if err == nil {
		t.Error("半截流应向上报错")
	}
	if !strings.Contains(written, "ok") {
		t.Errorf("已写出的正文应在客户端可见：%q", written)
	}
	if strings.Contains(written, "[DONE]") {
		t.Errorf("半截流不该出现结束哨兵：%q", written)
	}
	// Chat Completions 不发明非规范的错误帧：客户端看到的是「流到此为止」。
	if strings.Contains(written, "error") {
		t.Errorf("openaichat 不该下发错误帧（本协议没有该帧）：%q", written)
	}
}

// TestRateLimitWordingDoesNotChangeDisposition 用**每次调用都不同**的报文验证处置稳定。
//
// 固定一份报文去测，只能证明「这一份被认出来了」；而真实上游的措辞是散的，且同一个厂商
// 也会改文案。这里让假上游对每次调用换一种措辞，断言的是处置不随措辞漂移：都归限流、
// 都换渠道、都**不停用凭据**（凭据没坏，只是渠道忙）。
func TestRateLimitWordingDoesNotChangeDisposition(t *testing.T) {
	wordings := []string{
		`{"error":{"message":"Rate limit reached for requests"}}`,
		`{"error":{"message":"Your account is rate limited. Please slow down."}}`,
		`{"error":{"message":"当前并发数过高，已触发限流"}}`,
		`{"error":{"message":"Too many requests in a short period"}}`,
		`{"error":{"message":"请求过于频繁，请稍后重试"}}`,
		`{"error":{"message":"当前请求过多，请稍后重试"}}`,
		`{"error":{"message":"The engine is currently overloaded, please try again later"}}`,
		`{"error":{"type":"overloaded_error"}}`,
		`{"error":{"code":"rate_limit_exceeded"}}`,
	}

	broken := newSSEWordingUpstream(t, wordings)
	healthy := newSSEUpstream(t, sseScript{frames: []string{sseContentFrame, sseFinishFrame}})
	fixture := newDispositionFixture(t, []string{broken.server.URL, healthy.server.URL}, 30*time.Second)

	// 每次请求都以限流失败开场，措辞逐次变化。
	for index := range wordings {
		written, err := fixture.runStream(t)
		if err != nil {
			t.Fatalf("第 %d 次（措辞 %q）应在换渠道后成功，实际 %v", index+1, wordings[index], err)
		}
		if !strings.Contains(written, "[DONE]") {
			t.Fatalf("第 %d 次（措辞 %q）客户端未收到完整流：%q", index+1, wordings[index], written)
		}
	}

	// 每种措辞都不该让凭据被停用。不能断言「一直是 sk-a」：轮换器每 Resolve 一次就推进游标，
	// 而一次请求会经历「故障渠道 + 健康渠道」两次 Resolve。能区分「停用」与「只是轮到下一把」
	// 的只有一件事：无冷却时那把 key 会在组内轮完一圈后回来。
	if keys := broken.keySequence(); !containsString(keys[1:], "sk-a") {
		t.Fatalf("首把 key 后面再未出现（%v）：限流停用了凭据，但它只是渠道忙", keys)
	}
	if got := fixture.breaker.Status("1").ConsecutiveFailures; got != 0 {
		t.Fatalf("渠道 1 的连续失败 = %d，期望 0：限流不该计入渠道健康度", got)
	}
}

// TestStreamErrorFrameSupportDiffersByProtocol 记录「写出字节后失败」的降级方式因协议而异。
//
// 这是各协议的线协议事实而不是缺陷：有的协议有独立的错误事件，有的没有；对没有该事件的
// 协议，网关不发明非规范形态。后果是客户端在流中途遇到上游故障时看到的东西不同：
// 一个结构化错误帧，或者一次静默的断流。钉住它，使以后新增协议时这条差异能被看见。
func TestStreamErrorFrameSupportDiffersByProtocol(t *testing.T) {
	cases := []struct {
		name    string
		adapter domain.Adapter
		want    bool
	}{
		{name: "openai_chat", adapter: openaichat.New(), want: false},
		{name: "anthropic_messages", adapter: anthropic.New(), want: true},
		{name: "openai_responses", adapter: openairesponses.New(), want: true},
		// Gemini 与 Chat Completions 一样没有错误事件，只在下发形态里表达错误。
		{name: "gemini_generate", adapter: gemini.New(), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoder, ok := tc.adapter.(domain.StreamErrorEncoder)
			if !ok {
				t.Fatalf("%s 未实现流式错误帧能力（流水线会静默降级）", tc.name)
			}
			frame, supported := encoder.EncodeStreamError(domain.NewError(domain.CodeUpstreamUnavailable, "上游不可用"))
			if supported != tc.want {
				t.Fatalf("supported = %v，期望 %v", supported, tc.want)
			}
			if supported && len(frame) == 0 {
				t.Fatal("声称支持流式错误帧却编不出帧：调用方会把空帧当成已下发")
			}
			if !supported && len(frame) != 0 {
				t.Fatal("声称不支持却给了帧")
			}
		})
	}
}

// TestUnrecognizedStreamFrameIsForwardedVerbatim 记录「看不懂的帧」的处置。
//
// 帧形状不是本协议认得的错误帧时，网关按「宁多勿丢」原样转发给客户端——宁可多给一段
// 客户端可能用不上的字节，也不猜着丢弃；该流随后按上游自己的节奏正常收尾。
//
// 代价只有一个：这一帧已写出，换渠道重试的窗口随之关闭。本用例把这个代价写清楚，
// 避免以后被当成偶发故障排查。
func TestUnrecognizedStreamFrameIsForwardedVerbatim(t *testing.T) {
	broken := newSSEUpstream(t, sseScript{frames: []string{`{}`}})
	healthy := newSSEUpstream(t, sseScript{frames: []string{sseContentFrame, sseFinishFrame}})
	fixture := newDispositionFixture(t, []string{broken.server.URL, healthy.server.URL}, 30*time.Second)

	written, _ := fixture.runStream(t)

	if !strings.Contains(written, "{}") {
		t.Errorf("看不懂的帧应原样转发给客户端：%q", written)
	}
	if !strings.Contains(written, "[DONE]") {
		t.Errorf("上游正常发了结束哨兵，客户端应收到完整流：%q", written)
	}
	if healthy.callCount() != 0 {
		t.Errorf("已写出字节后不该换渠道，健康渠道调用次数 = %d，期望 0", healthy.callCount())
	}
}

// wordingUpstream 每次调用换一种限流措辞，用于验证处置不随措辞漂移。
type wordingUpstream struct {
	server   *httptest.Server
	wordings []string

	mu    sync.Mutex
	calls int
	keys  []string
}

// newSSEWordingUpstream 起一个逐次换措辞的限流假上游。
func newSSEWordingUpstream(t *testing.T, wordings []string) *wordingUpstream {
	t.Helper()
	upstream := &wordingUpstream{wordings: wordings}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.mu.Lock()
		index := upstream.calls
		upstream.calls++
		upstream.keys = append(upstream.keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		upstream.mu.Unlock()
		if index >= len(upstream.wordings) {
			index = len(upstream.wordings) - 1
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", upstream.wordings[index])
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// keySequence 返回每次调用携带的凭据。
func (u *wordingUpstream) keySequence() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.keys...)
}
