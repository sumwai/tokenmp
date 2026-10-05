package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖用量落库的端到端路径：三方言的流式与非流式转发都写入 billing_usage，
// 且分量键按 issue #18 的映射规范。
//
// 上游用 httptest 模拟，流水记录用内存假存储；不连数据库、不依赖测试夹具。

const (
	// chatSSEContent 是 Chat Completions 上游流的内容帧部分。
	chatSSEContent = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"up-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"up-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"up-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"

	// chatSSEUsageFrame 是只承载用量的帧：choices 为空、usage 非空。
	// 它是 include_usage 生效后上游追加的帧，客户端未索取时不应收到。
	chatSSEUsageFrame = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"up-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":2,\"cache_write_tokens\":1},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"

	// chatSSE 是一段完整的 Chat Completions 上游流：结束原因与用量分两帧下发，
	// 用量帧的 choices 为空，是 include_usage 生效后的标准形态。
	chatSSE = chatSSEContent + chatSSEUsageFrame + "data: [DONE]\n\n"

	// chatSSEWithoutUsage 是客户端未索取用量时网关应当写回的内容：去掉用量帧，
	// [DONE] 与内容帧原样保留。
	chatSSEWithoutUsage = chatSSEContent + "data: [DONE]\n\n"

	// responsesSSE 是 Responses 上游流：用量随 response.completed 事件下发。
	responsesSSE = "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_1\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"up-model\",\"usage\":{\"input_tokens\":5,\"output_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":2,\"cache_write_tokens\":1},\"output_tokens_details\":{\"reasoning_tokens\":1}}}}\n\n"

	// anthropicSSE 是 Anthropic Messages 上游流：输入侧计数在 message_start，
	// 输出侧与结束原因在随后的 message_delta，终止标记是 message_stop。
	anthropicSSE = "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"model\":\"up-model\",\"usage\":{\"input_tokens\":5,\"cache_read_input_tokens\":2,\"cache_creation_input_tokens\":1}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	// anthropicTieredSSE 是带 cache_creation 分档明细的 Anthropic 上游流：
	// 缓存写合计 9 拆为 5 分钟档 6 与 1 小时档 3，落库时应分别入两档计费分量。
	anthropicTieredSSE = "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"model\":\"up-model\",\"usage\":{\"input_tokens\":5,\"cache_read_input_tokens\":2,\"cache_creation_input_tokens\":9,\"cache_creation\":{\"ephemeral_5m_input_tokens\":6,\"ephemeral_1h_input_tokens\":3}}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
)

// streamUsageCase 是一个方言的流式用量用例。
type streamUsageCase struct {
	name         string
	protocol     domain.Protocol
	clientBody   string
	upstreamBody string
	// wantClientBody 是期望客户端收到的响应体；为空时取 upstreamBody。
	// 客户端未索取用量帧时，上游额外的用量帧被抑制，二者因此不同。
	wantClientBody string
	// wantUsage 是期望落库的分量。三个方言的上游计数形状不同，但内部口径归一后
	// 落库分量一致：Anthropic 的 input_tokens 不含缓存，由适配器换算进 input_token。
	wantUsage map[billing.Metric]int
	// wantIncludeUsage 报告该方言是否应在发往上游的请求体里带上用量索取开关。
	wantIncludeUsage bool
}

func streamUsageCases() []streamUsageCase {
	metrics := map[billing.Metric]int{
		billing.MetricInputToken:      5,
		billing.MetricOutputToken:     3,
		billing.MetricCacheReadToken:  2,
		billing.MetricCacheWriteToken: 1,
		billing.MetricReasoningToken:  1,
		// request 由落库路径补写，不来自适配器上报（见 billing.WithRequest）。
		billing.MetricRequest: 1,
	}
	anthropicMetrics := map[billing.Metric]int{
		// Anthropic 的 input_tokens 不含缓存，内部口径把缓存并进主计数：5 + 2 + 1。
		billing.MetricInputToken:      8,
		billing.MetricOutputToken:     3,
		billing.MetricCacheReadToken:  2,
		billing.MetricCacheWriteToken: 1,
		billing.MetricRequest:         1,
	}
	return []streamUsageCase{
		{
			// 客户端未索取用量帧：上游的用量帧被抑制，用量仍照常落库。
			name:             "openai_chat",
			protocol:         domain.ProtocolOpenAIChat,
			clientBody:       `{"model":"alias","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			upstreamBody:     chatSSE,
			wantClientBody:   chatSSEWithoutUsage,
			wantUsage:        metrics,
			wantIncludeUsage: true,
		},
		{
			// 客户端自行索取用量帧：用量帧正常转发。
			name:             "openai_chat_include_usage",
			protocol:         domain.ProtocolOpenAIChat,
			clientBody:       `{"model":"alias","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`,
			upstreamBody:     chatSSE,
			wantUsage:        metrics,
			wantIncludeUsage: true,
		},
		{
			name:         "openai_responses",
			protocol:     domain.ProtocolOpenAIResponses,
			clientBody:   `{"model":"alias","input":"hi","stream":true}`,
			upstreamBody: responsesSSE,
			wantUsage:    metrics,
		},
		{
			name:         "anthropic_messages",
			protocol:     domain.ProtocolAnthropicMessages,
			clientBody:   `{"model":"alias","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":true}`,
			upstreamBody: anthropicSSE,
			wantUsage:    anthropicMetrics,
		},
		{
			// 分档缓存写：两档分别落 cache_write_5m / cache_write_1h，不分档分量不落。
			name:         "anthropic_messages_tiered_cache_write",
			protocol:     domain.ProtocolAnthropicMessages,
			clientBody:   `{"model":"alias","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":true}`,
			upstreamBody: anthropicTieredSSE,
			wantUsage: map[billing.Metric]int{
				billing.MetricInputToken:     16,
				billing.MetricOutputToken:    3,
				billing.MetricCacheReadToken: 2,
				billing.MetricCacheWrite5m:   6,
				billing.MetricCacheWrite1h:   3,
				billing.MetricRequest:        1,
			},
		},
	}
}

// newUpstreamRecorder 起一个记录请求并回固定流式响应体的上游。
func newUpstreamRecorder(t *testing.T, responseBody string) (*httptest.Server, chan recordedUpstream) {
	t.Helper()
	records := make(chan recordedUpstream, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		model, _ := fields["model"].(string)
		includeUsage := false
		if options, ok := fields["stream_options"].(map[string]any); ok {
			includeUsage, _ = options["include_usage"].(bool)
		}
		records <- recordedUpstream{path: r.URL.Path, model: model, includeUsage: includeUsage}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)
	return server, records
}

// recordingGatewayStore 装配一个带假存储与模拟上游的网关，返回网关地址与假存储。
func recordingGatewayStore(t *testing.T, upstreamURL, upstreamModel string) (*httptest.Server, *fakeGatewayStore) {
	t.Helper()
	st := &fakeGatewayStore{
		auth: activeAuth(),
		routes: []store.RouteCandidate{{
			ChannelID: 10, BaseURL: upstreamURL, CredGroup: "group-a", UpstreamModel: upstreamModel,
		}},
		credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		wantMerchantID: 1,
	}
	server := httptest.NewServer(newTestGateway(t, st).handler)
	t.Cleanup(server.Close)
	return server, st
}

// waitUsageRows 轮询等待流水行数达到 want，超时即失败。
//
// 流式响应读到 EOF 时服务端处理器已经写完流水，但轮询让断言不依赖这个时序细节。
func waitUsageRows(t *testing.T, st *fakeGatewayStore, want int) []recordedUsage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rows := st.usageSnapshot(); len(rows) >= want {
			return rows
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %d 行流水超时，当前 %d 行", want, len(st.usageSnapshot()))
	return nil
}

// assertUsageRow 断言一行流水的归属与分量。
func assertUsageRow(t *testing.T, row recordedUsage, wantModel string, want map[billing.Metric]int) {
	t.Helper()
	if row.MerchantID != activeAuth().MerchantID {
		t.Errorf("merchant_id = %d，期望 %d", row.MerchantID, activeAuth().MerchantID)
	}
	if row.AccountID != activeAuth().AccountID {
		t.Errorf("account_id = %d，期望 %d", row.AccountID, activeAuth().AccountID)
	}
	if row.ChannelID != 10 {
		t.Errorf("channel_id = %d，期望 10", row.ChannelID)
	}
	if row.APIKeyID != activeAuth().APIKeyID {
		t.Errorf("api_key_id = %d，期望 %d", row.APIKeyID, activeAuth().APIKeyID)
	}
	if row.Model != wantModel {
		t.Errorf("model = %q，期望 %q", row.Model, wantModel)
	}
	if len(row.Usage) != len(want) {
		t.Fatalf("分量 = %#v，期望 %#v", row.Usage, want)
	}
	for metric, qty := range want {
		if row.Usage[metric] != qty {
			t.Errorf("分量 %s = %d，期望 %d", metric, row.Usage[metric], qty)
		}
	}
}

// TestStreamingUsagePersistedThreeDialects 覆盖三方言与两种索取形态的流式端到端：
// 逐帧透传或用量帧抑制、用量索取开关注入、usage 落 billing_usage。
func TestStreamingUsagePersistedThreeDialects(t *testing.T) {
	for _, tt := range streamUsageCases() {
		t.Run(tt.name, func(t *testing.T) {
			upstream, records := newUpstreamRecorder(t, tt.upstreamBody)
			gatewayServer, st := recordingGatewayStore(t, upstream.URL, "up-model")

			result := doPost(t, gatewayServer.URL+tt.protocol.EndpointPath(), authSchemePrefix+testAPIKey, tt.clientBody)
			if result.status != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
			}
			// 同协议透传下客户端收到的就是上游逐帧字节；客户端未索取用量帧时，
			// 只承载用量的那一帧被抑制，因此期望体由 wantClientBody 单独给出。
			wantBody := tt.wantClientBody
			if wantBody == "" {
				wantBody = tt.upstreamBody
			}
			if string(result.body) != wantBody {
				t.Errorf("客户端响应体不符：\n实际 %s\n期望 %s", result.body, wantBody)
			}
			if !strings.HasPrefix(result.contentType, "text/event-stream") {
				t.Errorf("Content-Type = %q，期望 text/event-stream", result.contentType)
			}

			recorded := <-records
			if recorded.path != tt.protocol.EndpointSegment() {
				t.Errorf("上游路径 = %q，期望 %q", recorded.path, tt.protocol.EndpointSegment())
			}
			if recorded.model != "up-model" {
				t.Errorf("上游模型名 = %q，期望 up-model", recorded.model)
			}
			if recorded.includeUsage != tt.wantIncludeUsage {
				t.Errorf("上游用量索取开关 = %v，期望 %v", recorded.includeUsage, tt.wantIncludeUsage)
			}

			rows := waitUsageRows(t, st, 1)
			if len(rows) != 1 {
				t.Fatalf("流水行数 = %d，期望 1（一次请求一行）", len(rows))
			}
			assertUsageRow(t, rows[0], "up-model", tt.wantUsage)
		})
	}
}

// TestNonStreamingUsagePersistedThreeDialects 覆盖三方言非流式响应里的用量落库。
func TestNonStreamingUsagePersistedThreeDialects(t *testing.T) {
	metrics := map[billing.Metric]int{
		billing.MetricInputToken:      5,
		billing.MetricOutputToken:     3,
		billing.MetricCacheReadToken:  2,
		billing.MetricCacheWriteToken: 1,
		billing.MetricReasoningToken:  1,
		billing.MetricRequest:         1,
	}
	anthropicMetrics := map[billing.Metric]int{
		billing.MetricInputToken:      8,
		billing.MetricOutputToken:     3,
		billing.MetricCacheReadToken:  2,
		billing.MetricCacheWriteToken: 1,
		billing.MetricRequest:         1,
	}
	tests := []struct {
		name         string
		protocol     domain.Protocol
		clientBody   string
		responseBody string
		wantUsage    map[billing.Metric]int
	}{
		{
			name:       "openai_chat",
			protocol:   domain.ProtocolOpenAIChat,
			clientBody: `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`,
			responseBody: `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":5,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"completion_tokens_details":{"reasoning_tokens":1}}}`,
			wantUsage: metrics,
		},
		{
			name:       "openai_responses",
			protocol:   domain.ProtocolOpenAIResponses,
			clientBody: `{"model":"alias","input":"hi"}`,
			responseBody: `{"id":"resp_1","model":"up-model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],` +
				`"usage":{"input_tokens":5,"output_tokens":3,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"output_tokens_details":{"reasoning_tokens":1}}}`,
			wantUsage: metrics,
		},
		{
			name:       "anthropic_messages",
			protocol:   domain.ProtocolAnthropicMessages,
			clientBody: `{"model":"alias","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			responseBody: `{"id":"msg_1","type":"message","role":"assistant","model":"up-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",` +
				`"usage":{"input_tokens":5,"output_tokens":3,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}`,
			wantUsage: anthropicMetrics,
		},
		{
			// 非流式分档缓存写：同样分别落 cache_write_5m / cache_write_1h。
			name:       "anthropic_messages_tiered_cache_write",
			protocol:   domain.ProtocolAnthropicMessages,
			clientBody: `{"model":"alias","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			responseBody: `{"id":"msg_1","type":"message","role":"assistant","model":"up-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",` +
				`"usage":{"input_tokens":5,"output_tokens":3,"cache_read_input_tokens":2,"cache_creation_input_tokens":9,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":6,"ephemeral_1h_input_tokens":3}}}`,
			wantUsage: map[billing.Metric]int{
				billing.MetricInputToken:     16,
				billing.MetricOutputToken:    3,
				billing.MetricCacheReadToken: 2,
				billing.MetricCacheWrite5m:   6,
				billing.MetricCacheWrite1h:   3,
				billing.MetricRequest:        1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tt.responseBody)
			}))
			t.Cleanup(upstream.Close)
			gatewayServer, st := recordingGatewayStore(t, upstream.URL, "up-model")

			result := doPost(t, gatewayServer.URL+tt.protocol.EndpointPath(), authSchemePrefix+testAPIKey, tt.clientBody)
			if result.status != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
			}
			rows := waitUsageRows(t, st, 1)
			assertUsageRow(t, rows[0], "up-model", tt.wantUsage)
		})
	}
}

// TestStreamingFlushIsIncremental 断言流式首帧在上游结束前就到达客户端：
// 上游写出首帧后阻塞，客户端能在放行上游之前读到它，说明 Flush 生效。
func TestStreamingFlushIsIncremental(t *testing.T) {
	const firstFrame = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"up-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"},\"finish_reason\":null}]}\n\n"
	const rest = "data: [DONE]\n\n"

	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("上游测试服务器不支持 Flush")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, firstFrame)
		flusher.Flush()
		<-release
		_, _ = io.WriteString(w, rest)
	}))
	t.Cleanup(upstream.Close)
	gatewayServer, _ := recordingGatewayStore(t, upstream.URL, "up-model")

	body := `{"model":"alias","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		gatewayServer.URL+domain.ProtocolOpenAIChat.EndpointPath(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authorizationHeader, authSchemePrefix+testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("发送请求失败：%v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	firstSeen := make(chan struct{})
	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		tmp := make([]byte, 256)
		sent := false
		for {
			n, readErr := resp.Body.Read(tmp)
			buf.Write(tmp[:n])
			if !sent && buf.Len() >= len(firstFrame) {
				sent = true
				close(firstSeen)
			}
			if readErr != nil {
				done <- buf.Bytes()
				return
			}
		}
	}()

	select {
	case <-firstSeen:
		// 首帧已到达而上游仍被阻塞：Flush 生效。
		close(release)
	case <-time.After(3 * time.Second):
		t.Fatal("首帧未在上游结束前到达，Flush 未生效")
	}
	full := <-done
	if string(full) != firstFrame+rest {
		t.Errorf("响应体 = %q，期望 %q", full, firstFrame+rest)
	}
}

// TestStreamingUsageExtractionFailureDoesNotBlock 断言上游没给用量帧时转发照常完成：
// 提取失败只让流水缺分量，不阻断已开始的流，也不向客户端补发任何数据。
func TestStreamingUsageExtractionFailureDoesNotBlock(t *testing.T) {
	const noUsageSSE = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"up-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	upstream, _ := newUpstreamRecorder(t, noUsageSSE)
	gatewayServer, st := recordingGatewayStore(t, upstream.URL, "up-model")

	result := doPost(t, gatewayServer.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if string(result.body) != noUsageSSE {
		t.Errorf("响应体应与上游逐帧字节一致：\n实际 %s\n期望 %s", result.body, noUsageSSE)
	}
	rows := waitUsageRows(t, st, 1)
	if len(rows) != 1 {
		t.Fatalf("流水行数 = %d，期望 1（一次请求一行）", len(rows))
	}
	if len(rows[0].Usage) != 1 || rows[0].Usage[billing.MetricRequest] != 1 {
		t.Errorf("未取得 token 用量时分量应只剩落库生成的 request=1，得到 %#v", rows[0].Usage)
	}
}

// TestUsageInsertFailureDoesNotBlockForwarding 断言落库失败不影响转发：
// 流水是旁路，客户端仍拿到完整的上游响应。
func TestUsageInsertFailureDoesNotBlockForwarding(t *testing.T) {
	const responseBody = `{"id":"chatcmpl-1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(upstream.Close)
	gatewayServer, st := recordingGatewayStore(t, upstream.URL, "up-model")
	st.mu.Lock()
	st.insertErr = domain.NewError(domain.CodeInternal, "模拟落库失败")
	st.mu.Unlock()

	result := doPost(t, gatewayServer.URL+domain.ProtocolOpenAIChat.EndpointPath(),
		authSchemePrefix+testAPIKey, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if string(result.body) != responseBody {
		t.Errorf("响应体应逐字节透传：\n实际 %s\n期望 %s", result.body, responseBody)
	}
	if rows := st.usageSnapshot(); len(rows) != 0 {
		t.Errorf("落库失败时不应留下流水，得到 %#v", rows)
	}
}
