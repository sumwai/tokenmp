package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/domain"
)

// stubHeaders 是 HeaderProvider 的空实现：本文件只考察响应体解码路径，不关心请求头。
type stubHeaders struct{}

func (stubHeaders) UpstreamHeaders(context.Context, domain.Route) (http.Header, error) {
	return http.Header{}, nil
}

// openAIChatAdapters 返回只认 Chat Completions 报文的适配器查找函数。
func openAIChatAdapters(domain.Protocol) (domain.Adapter, error) { return openaichat.New(), nil }

// TestCompleteExposesUpstreamStatus 断言上游非 2xx 时错误携带实际上游状态码。
//
// 状态码是排障事实：错误分级给出的是面向客户端的 502/504，不能代替上游真实返回的码。
func TestCompleteExposesUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"slow down"}`))
	}))
	defer server.Close()

	client, err := New(Options{Headers: stubHeaders{}, Adapters: openAIChatAdapters})
	if err != nil {
		t.Fatalf("构造上游客户端失败: %v", err)
	}
	route := domain.Route{UpstreamID: "u1", Protocol: domain.ProtocolOpenAIChat, BaseURL: server.URL}
	_, callErr := client.Complete(context.Background(), route, &domain.Request{Model: "m"}, []byte(`{"model":"m"}`))
	if callErr == nil {
		t.Fatal("上游返回 429 时应报错")
	}
	if code := domain.AsError(callErr).Code; code != domain.CodeUpstreamRateLimited {
		t.Errorf("错误码 = %q，期望 %q", code, domain.CodeUpstreamRateLimited)
	}
	var carrier interface{ UpstreamStatus() int }
	if !errors.As(callErr, &carrier) {
		t.Fatalf("错误应携带上游状态码能力，实际类型为 %T", callErr)
	}
	if got := carrier.UpstreamStatus(); got != http.StatusTooManyRequests {
		t.Errorf("上游状态码 = %d，期望 429", got)
	}
}

// TestNewFillsStreamTimeoutDefaults 断言未配置时流式两级超时取默认值，渠道配置优先。
func TestNewFillsStreamTimeoutDefaults(t *testing.T) {
	client, err := New(Options{Headers: stubHeaders{}, Adapters: openAIChatAdapters})
	if err != nil {
		t.Fatalf("构造上游客户端失败: %v", err)
	}
	if client.streamFirstByteTimeout != defaultStreamFirstByteTimeout {
		t.Errorf("首字节超时 = %s，期望 %s", client.streamFirstByteTimeout, defaultStreamFirstByteTimeout)
	}
	if got := client.idleTimeoutFor(domain.Route{}); got != defaultChannelTimeout {
		t.Errorf("空闲读超时 = %s，期望 %s", got, defaultChannelTimeout)
	}
	if got := client.idleTimeoutFor(domain.Route{Timeout: 3 * time.Second}); got != 3*time.Second {
		t.Errorf("渠道配置应优先：空闲读超时 = %s，期望 3s", got)
	}
}

// TestCompleteAttachesUpstreamBodyToDecodeFailure 守护「上游响应无法解析」的排障信息。
//
// 上游用 2xx 回了不符合本协议的正文时（典型成因：url 写错被重定向到官网首页，
// 或上游用 200 回了错误信封），只报一个笼统原因无法定位，必须把上游正文片段带进详情。
// 详情只进服务端日志，不进面向客户端的错误体。
func TestCompleteAttachesUpstreamBodyToDecodeFailure(t *testing.T) {
	const html = `<html><head><title>301 Moved Permanently</title></head><body>nginx</body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	}))
	defer server.Close()

	client, err := New(Options{Headers: stubHeaders{}, Adapters: openAIChatAdapters})
	if err != nil {
		t.Fatalf("构造上游客户端失败: %v", err)
	}

	route := domain.Route{
		UpstreamID: "u1",
		Protocol:   domain.ProtocolOpenAIChat,
		BaseURL:    server.URL,
	}
	_, callErr := client.Complete(context.Background(), route, &domain.Request{Model: "m"}, []byte(`{"model":"m"}`))
	if callErr == nil {
		t.Fatal("上游返回非本协议的正文时必须报错，实际返回了成功")
	}
	domainErr := domain.AsError(callErr)
	if domainErr == nil {
		t.Fatalf("错误必须是 domain.Error，实际类型为 %T", callErr)
	}
	if domainErr.Code != domain.CodeUpstreamUnavailable {
		t.Errorf("错误码 = %q，期望 %q", domainErr.Code, domain.CodeUpstreamUnavailable)
	}
	if !strings.Contains(domainErr.Detail, "301 Moved Permanently") {
		t.Errorf("排障详情应包含上游正文片段，实际为 %q", domainErr.Detail)
	}
}
