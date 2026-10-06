package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/sumwai/tokenmp/internal/adapters/gemini"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件把跨协议路由矩阵扩到四方言：serve_test.go 的矩阵与流式重建矩阵覆盖既有三方言，
// 这里补齐 Gemini 与三种方言的所有组合（同协议命中与两个方向的跨协议命中，流式与非流式），
// 与既有矩阵合起来构成四方言 × 流式/非流的完整矩阵。
//
// 不修改 serve_test.go：那里的样本与假上游按请求体里的 model / stream 字段判定，
// Gemini 的模型名与流式形态在 URL 路径上，需要独立的假上游识别口径。

// geminiMatrixDialects 是四方言路由矩阵的样本：既有三方言取 serve_test.go 的样本，
// Gemini 补上路径形态的客户端请求体与上游应答。
func geminiMatrixDialects() []e2eDialect {
	dialects := append([]e2eDialect{}, e2eDialects()...)
	return append(dialects, e2eDialect{
		protocol:            domain.ProtocolGeminiGenerate,
		adapter:             gemini.New(),
		requestBody:         `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
		streamRequestBody:   `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
		upstreamResponse:    geminiResponseBody,
		upstreamStream:      geminiStreamFrames,
		upstreamHeader:      "x-goog-api-key",
		upstreamHeaderValue: "sk-upstream",
		streamTextMarker:    `"text":"hello"`,
		// Gemini 的 SSE 流没有终止事件，结束由带 finishReason 的收尾帧表达。
		streamTerminator: `"finishReason":"STOP"`,
	})
}

// geminiMatrixClientEndpoint 返回某方言在本次调用里使用的客户端端点路径。
// 固定端点方言取注册路径；Gemini 的端点含模型名与流式后缀。
func geminiMatrixClientEndpoint(protocol domain.Protocol, stream bool) string {
	if protocol == domain.ProtocolGeminiGenerate {
		if stream {
			return "/v1beta/models/alias:streamGenerateContent?alt=sse"
		}
		return "/v1beta/models/alias:generateContent"
	}
	return protocol.EndpointPath()
}

// geminiMatrixUpstreamPath 返回某上游方言本次被调用时应命中的路径。
func geminiMatrixUpstreamPath(protocol domain.Protocol, stream bool) string {
	if protocol == domain.ProtocolGeminiGenerate {
		if stream {
			return "/models/up-model:streamGenerateContent"
		}
		return "/models/up-model:generateContent"
	}
	return protocol.EndpointSegment()
}

// geminiMatrixModelFromPath 从上游路径 /models/{model}:{method} 里取出模型名与流式形态。
func geminiMatrixModelFromPath(path string) (string, bool, bool) {
	rest, ok := strings.CutPrefix(path, "/models/")
	if !ok {
		return "", false, false
	}
	model, method, ok := strings.Cut(rest, ":")
	if !ok || model == "" {
		return "", false, false
	}
	switch method {
	case "generateContent":
		return model, false, true
	case "streamGenerateContent":
		return model, true, true
	default:
		return "", false, false
	}
}

// geminiMatrixRecording 是假上游收到的一次调用快照。
type geminiMatrixRecording struct {
	path   string
	query  url.Values
	header http.Header
	model  string
	stream bool
}

// geminiMatrixUpstream 是四方言路由矩阵的假上游：按网关实际打到的路径判定上游方言，
// 回该方言的报文，并记录路径、模型名、流式形态与凭据头。
type geminiMatrixUpstream struct {
	mu    sync.Mutex
	last  geminiMatrixRecording
	calls int
}

// snapshot 返回最后一次调用快照与累计调用次数。
func (u *geminiMatrixUpstream) snapshot() (geminiMatrixRecording, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.last, u.calls
}

// newGeminiMatrixUpstream 起一个按上游方言回应的假上游并注册关闭。
func newGeminiMatrixUpstream(t *testing.T) (*httptest.Server, *geminiMatrixUpstream) {
	t.Helper()
	recorder := &geminiMatrixUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		dialect, model, stream, ok := geminiMatrixResolve(r.URL.Path, raw)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		recorder.mu.Lock()
		recorder.last = geminiMatrixRecording{
			path:   r.URL.Path,
			query:  r.URL.Query(),
			header: r.Header.Clone(),
			model:  model,
			stream: stream,
		}
		recorder.calls++
		recorder.mu.Unlock()

		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, dialect.upstreamStream)
			return
		}
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, dialect.upstreamResponse)
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

// geminiMatrixResolve 由上游路径与请求体判定本次调用命中的上游方言、模型名与流式形态。
//
// Gemini 的模型名与流式形态在路径上；其余方言在请求体里。
func geminiMatrixResolve(path string, body []byte) (e2eDialect, string, bool, bool) {
	dialects := geminiMatrixDialects()
	if model, stream, ok := geminiMatrixModelFromPath(path); ok {
		return geminiMatrixDialectForStatic(dialects, domain.ProtocolGeminiGenerate), model, stream, true
	}
	var fields map[string]any
	_ = json.Unmarshal(body, &fields)
	model, _ := fields["model"].(string)
	stream, _ := fields["stream"].(bool)
	for _, dialect := range dialects {
		if dialect.protocol == domain.ProtocolGeminiGenerate {
			continue
		}
		if strings.HasSuffix(path, dialect.protocol.EndpointSegment()) {
			return dialect, model, stream, true
		}
	}
	return e2eDialect{}, "", false, false
}

// geminiMatrixDialectForStatic 是无 *testing.T 的样本查找，供假上游 handler 使用。
func geminiMatrixDialectForStatic(dialects []e2eDialect, protocol domain.Protocol) e2eDialect {
	for _, dialect := range dialects {
		if dialect.protocol == protocol {
			return dialect
		}
	}
	return e2eDialect{}
}

// TestGatewayGeminiRoutingMatrix 覆盖 Gemini 与四方言的同协议/跨协议命中，流式与非流式。
//
// 既有三方言之间的组合由 serve_test.go 的路由矩阵与流式重建矩阵覆盖，这里只跑至少一侧是
// Gemini 的组合，两份矩阵合起来即四方言 × 流式/非流式。断言：上游端点按上游方言拼接、
// 模型名替换成渠道上游模型名、凭据按上游方言注入、客户端拿到本方言的合法响应体。
func TestGatewayGeminiRoutingMatrix(t *testing.T) {
	dialects := geminiMatrixDialects()
	for _, client := range dialects {
		for _, upstream := range dialects {
			if client.protocol != domain.ProtocolGeminiGenerate && upstream.protocol != domain.ProtocolGeminiGenerate {
				continue
			}
			for _, stream := range []bool{false, true} {
				sameProtocol := client.protocol == upstream.protocol
				name := string(client.protocol) + "/"
				if sameProtocol {
					name += "same_protocol"
				} else {
					name += "from_" + string(upstream.protocol)
				}
				if stream {
					name += "/stream"
				} else {
					name += "/non_stream"
				}
				t.Run(name, func(t *testing.T) {
					upstreamServer, recorder := newGeminiMatrixUpstream(t)
					candidate := store.RouteCandidate{
						ChannelID: 10, ChannelType: store.ChannelType(upstream.protocol),
						BaseURL: upstreamServer.URL, CredGroup: "group-a", UpstreamModel: "up-model",
					}
					st := &fakeGatewayStore{
						auth:           activeAuth(),
						credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
						wantMerchantID: 1,
					}
					if sameProtocol {
						st.routes = []store.RouteCandidate{candidate}
					} else {
						st.crossRoutes = []store.RouteCandidate{candidate}
					}
					server := httptest.NewServer(newTestGateway(t, st).handler)
					defer server.Close()

					body := client.requestBody
					if stream {
						body = client.streamRequestBody
					}
					result := doPost(t, server.URL+geminiMatrixClientEndpoint(client.protocol, stream),
						authSchemePrefix+testAPIKey, body)
					if result.status != http.StatusOK {
						t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
					}
					assertGeminiMatrixClientResponse(t, client, upstream, stream, sameProtocol, result)

					recorded, calls := recorder.snapshot()
					if calls != 1 {
						t.Fatalf("上游调用次数 = %d，期望 1", calls)
					}
					if wantPath := geminiMatrixUpstreamPath(upstream.protocol, stream); recorded.path != wantPath {
						t.Errorf("上游路径 = %q，期望 %q", recorded.path, wantPath)
					}
					if upstream.protocol == domain.ProtocolGeminiGenerate && stream && recorded.query.Get("alt") != "sse" {
						t.Errorf("Gemini 流式上游应带 alt=sse，实际查询串 %q", recorded.query.Encode())
					}
					if recorded.model != "up-model" {
						t.Errorf("上游模型名 = %q，期望 up-model", recorded.model)
					}
					if recorded.stream != stream {
						t.Errorf("上游流式形态 = %v，期望 %v", recorded.stream, stream)
					}
					if got := recorded.header.Get(upstream.upstreamHeader); got != upstream.upstreamHeaderValue {
						t.Errorf("%s = %q，期望 %q", upstream.upstreamHeader, got, upstream.upstreamHeaderValue)
					}
				})
			}
		}
	}
}

// assertGeminiMatrixClientResponse 断言客户端拿到本方言的合法响应体：
// 同协议命中逐字节透传上游报文，跨协议命中是重建后的本方言结构。
func assertGeminiMatrixClientResponse(t *testing.T, client, upstream e2eDialect, stream, sameProtocol bool, result httpResult) {
	t.Helper()
	if stream {
		assertGeminiMatrixStreamFrames(t, client, result)
	} else if result.contentType != client.adapter.ContentType() {
		t.Errorf("Content-Type = %q，期望 %q", result.contentType, client.adapter.ContentType())
	}

	if sameProtocol {
		want := upstream.upstreamResponse
		if stream {
			want = upstream.upstreamStream
		}
		if string(result.body) != want {
			t.Errorf("同协议命中应逐字节透传上游响应：\n实际 %s\n期望 %s", result.body, want)
		}
		return
	}
	if string(result.body) == upstream.upstreamResponse || string(result.body) == upstream.upstreamStream {
		t.Error("跨协议响应不应与上游原始响应逐字节相同")
	}
	if !stream {
		assertClientDialectText(t, client, result.body)
	}
}

// assertGeminiMatrixStreamFrames 断言重建后的流是客户端方言：Content-Type 与文本、结束标记齐备。
func assertGeminiMatrixStreamFrames(t *testing.T, client e2eDialect, result httpResult) {
	t.Helper()
	if result.contentType != client.adapter.StreamContentType() {
		t.Errorf("Content-Type = %q，期望 %q", result.contentType, client.adapter.StreamContentType())
	}
	text := string(result.body)
	if !strings.Contains(text, client.streamTextMarker) {
		t.Errorf("客户端流缺少 %q：%s", client.streamTextMarker, text)
	}
	if !strings.Contains(text, client.streamTerminator) {
		t.Errorf("客户端流缺少 %q：%s", client.streamTerminator, text)
	}
}
