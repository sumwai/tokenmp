//go:build e2e

// 中间件插件层的端到端验证：起真实网关，加载真实插件文件，用进程内假上游观察
// 请求改写与流式事件过滤的实际效果。
//
// 覆盖三类样例与两类故障注入：
//
//  1. 模型别名：onRequest 改写 model，路由按改写后的模型命中渠道；
//  2. 参数覆盖：onRequest 补写 temperature，上游实际收到；
//  3. 流式事件过滤：onEvent 按 ctx.model 丢弃文本事件，客户端收不到该内容；
//  4. 死循环插件在预算内被中断且转发不受影响；
//  5. 抛错插件静默放行；
//  6. 沙箱：fetch 不可用、超长 eval 被拒。
//
// 构建标签与 make e2e 口径一致，需要真实 MySQL。测试自带数据清理。
package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/gateway"
	"github.com/sumwai/tokenmp/internal/plugin"
	"github.com/sumwai/tokenmp/internal/store"
)

// pluginMiddlewareSource 是剧本加载的唯一中间件文件。
//
// 全部行为按 ctx.model 分派，使同一个插件文件可以覆盖多个样例，也保证各用例
// 互不影响：一个用例的模型名只触发它自己的分支。
const pluginMiddlewareSource = `
export const events = ["text_delta"];

export function onRequest(body, ctx) {
  const model = ctx.model;
  if (model === "mw-alias") {
    body.model = "mw-target";
  } else if (model === "mw-param") {
    body.temperature = 1.5;
  } else if (model === "mw-spin") {
    for (;;) {}
  } else if (model === "mw-throw") {
    throw new Error("boom");
  } else if (model === "mw-sandbox") {
    body.sandbox_fetch = typeof fetch;
    try {
      eval("1".repeat(9000));
      body.sandbox_eval = "allowed";
    } catch (e) {
      body.sandbox_eval = e.name;
    }
  }
  return body;
}

export function onEvent(event, ctx) {
  if (ctx.model === "mw-filter") {
    return null;
  }
  return event;
}

export function onResponse(body, ctx) {
  if (ctx.model === "mw-response") {
    body.plugin_marker = "on-response";
  }
  return body;
}
`

// pluginModelMap 是剧本的请求模型与上游模型对应关系。
// mw-alias 刻意不在其中：它必须靠插件改写成 mw-target 才可能选到渠道。
var pluginModelMap = []struct {
	request  string
	upstream string
}{
	{"mw-target", "up-mw-target"},
	{"mw-param", "up-mw-param"},
	{"mw-filter", "up-mw-filter"},
	{"mw-spin", "up-mw-spin"},
	{"mw-throw", "up-mw-throw"},
	{"mw-sandbox", "up-mw-sandbox"},
	{"mw-response", "up-mw-response"},
}

// pluginUpstream 是记录收到的请求体与模型名的假上游。
type pluginUpstream struct {
	server   *httptest.Server
	adapters map[domain.Protocol]domain.Adapter

	mu     sync.Mutex
	bodies map[string]map[string]any
}

// newPluginUpstream 起一个假上游并注册关闭。
func newPluginUpstream(t *testing.T) *pluginUpstream {
	t.Helper()
	upstream := &pluginUpstream{
		adapters: map[domain.Protocol]domain.Adapter{
			domain.ProtocolOpenAIChat: openaichat.New(),
		},
		bodies: map[string]map[string]any{},
	}
	upstream.server = httptest.NewServer(http.HandlerFunc(upstream.handle))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// handle 记录请求体与模型名后回一个固定应答。
func (u *pluginUpstream) handle(w http.ResponseWriter, r *http.Request) {
	protocol, ok := e2eProtocolForPath(r.URL.Path)
	if !ok {
		http.Error(w, "假上游不支持该路径", http.StatusNotFound)
		return
	}
	adapter := u.adapters[protocol]
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "假上游读取请求体失败", http.StatusInternalServerError)
		return
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		http.Error(w, "假上游无法解析请求体", http.StatusBadRequest)
		return
	}
	model, _ := fields["model"].(string)
	stream, _ := fields["stream"].(bool)
	u.record(model, fields)

	response := e2eFakeResponse(model)
	if !stream {
		body, encodeErr := adapter.EncodeResponse(response)
		if encodeErr != nil {
			http.Error(w, "假上游编码响应失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", adapter.ContentType())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	frames, err := e2eBuildStreamFrames(adapter.NewStream(), model, response)
	if err != nil {
		http.Error(w, "假上游编码流式响应失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", adapter.StreamContentType())
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, frame := range frames {
		if _, err := w.Write(frame); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// record 记下某模型名最后一次收到的请求体。
func (u *pluginUpstream) record(model string, body map[string]any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.bodies[model] = body
}

// body 返回某模型名最后一次收到的请求体。
func (u *pluginUpstream) body(model string) map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.bodies[model]
}

// TestE2EPluginMiddleware 是中间件插件层的端到端剧本入口。
func TestE2EPluginMiddleware(t *testing.T) {
	dsn := os.Getenv(e2eDSNEnv)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过中间件插件端到端剧本", e2eDSNEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	st, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("关闭连接失败：%v", err)
		}
	})
	e2eDropAllTables(t, st)
	t.Cleanup(func() { e2eDropAllTables(t, st) })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	upstream := newPluginUpstream(t)
	svc := admin.New(st)
	merchantID, accountID, key := pluginOnboard(t, ctx, svc, upstream.server.URL)

	// 插件文件写在临时目录，加载路径即为网关配置里的路径。
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "middleware.mw.js")
	if err := os.WriteFile(pluginPath, []byte(pluginMiddlewareSource), 0o600); err != nil {
		t.Fatalf("写插件文件失败：%v", err)
	}

	gatewayURL, stopServe := pluginStartServe(t, st, pluginPath)
	defer stopServe()

	endpoint := gatewayURL + domain.ProtocolOpenAIChat.EndpointPath()

	t.Run("模型别名改写影响路由", func(t *testing.T) {
		result := e2ePost(t, endpoint, key, e2eRequest(domain.ProtocolOpenAIChat, "mw-alias", false))
		if result.status != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
		}
		if upstream.body("up-mw-target") == nil {
			t.Fatal("插件改写后的模型未命中上游模型 up-mw-target")
		}
	})

	t.Run("参数覆盖写入上游请求", func(t *testing.T) {
		result := e2ePost(t, endpoint, key, e2eRequest(domain.ProtocolOpenAIChat, "mw-param", false))
		if result.status != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
		}
		body := upstream.body("up-mw-param")
		if got, _ := body["temperature"].(float64); got != 1.5 {
			t.Fatalf("上游收到的 temperature = %v，期望 1.5", body["temperature"])
		}
	})

	t.Run("流式事件过滤丢弃文本", func(t *testing.T) {
		kept := e2ePost(t, endpoint, key, e2eRequest(domain.ProtocolOpenAIChat, "mw-alias", true))
		if kept.status != http.StatusOK {
			t.Fatalf("对照流状态码 = %d，响应体 %s", kept.status, kept.body)
		}
		if !strings.Contains(string(kept.body), e2eUpstreamText) {
			t.Fatalf("对照流应保留文本，响应体 %s", kept.body)
		}
		dropped := e2ePost(t, endpoint, key, e2eRequest(domain.ProtocolOpenAIChat, "mw-filter", true))
		if dropped.status != http.StatusOK {
			t.Fatalf("过滤流状态码 = %d，响应体 %s", dropped.status, dropped.body)
		}
		if strings.Contains(string(dropped.body), e2eUpstreamText) {
			t.Fatalf("文本事件应被丢弃，响应体 %s", dropped.body)
		}
	})

	t.Run("死循环插件被中断且不阻断转发", func(t *testing.T) {
		started := time.Now()
		result := e2ePost(t, endpoint, key, e2eRequest(domain.ProtocolOpenAIChat, "mw-spin", false))
		elapsed := time.Since(started)
		if result.status != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
		}
		if elapsed > 3*time.Second {
			t.Fatalf("死循环插件耗时 %v，转发被显著拖慢", elapsed)
		}
		if upstream.body("up-mw-spin") == nil {
			t.Fatal("死循环插件未阻断转发：上游应收到请求")
		}
	})

	t.Run("抛错插件静默放行", func(t *testing.T) {
		result := e2ePost(t, endpoint, key, e2eRequest(domain.ProtocolOpenAIChat, "mw-throw", false))
		if result.status != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
		}
		if upstream.body("up-mw-throw") == nil {
			t.Fatal("抛错插件应静默放行：上游应收到请求")
		}
	})

	t.Run("沙箱限制生效", func(t *testing.T) {
		result := e2ePost(t, endpoint, key, e2eRequest(domain.ProtocolOpenAIChat, "mw-sandbox", false))
		if result.status != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
		}
		body := upstream.body("up-mw-sandbox")
		if got, _ := body["sandbox_fetch"].(string); got != "undefined" {
			t.Fatalf("fetch 应当不可用，typeof fetch = %q", got)
		}
		if got, _ := body["sandbox_eval"].(string); got != "RangeError" {
			t.Fatalf("超长 eval 应当被拒，得到 %q", got)
		}
	})

	t.Run("非流式响应改写", func(t *testing.T) {
		result := e2ePost(t, endpoint, key, e2eRequest(domain.ProtocolOpenAIChat, "mw-response", false))
		if result.status != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
		}
		var fields map[string]any
		if err := json.Unmarshal(result.body, &fields); err != nil {
			t.Fatalf("客户端响应不是 JSON：%v，原文 %s", err, result.body)
		}
		if fields["plugin_marker"] != "on-response" {
			t.Fatalf("onResponse 未改写客户端响应：%s", result.body)
		}
	})

	// 用主键引用一次，确保入驻步骤产出的账户真正参与本剧本的断言。
	if merchantID == 0 || accountID == 0 {
		t.Fatal("入驻结果不完整")
	}
}

// pluginOnboard 走一遍最小入驻：商家、渠道、凭据、模型映射、定价、账户、密钥与账本。
func pluginOnboard(t *testing.T, ctx context.Context, svc *admin.Service, upstreamURL string) (merchantID, accountID uint64, key string) {
	t.Helper()

	merchantID, err := svc.CreateMerchant(ctx, "mw-partner", "中间件剧本商家", store.MerchantKindPartner)
	e2eMust(t, err)

	const credGroup = "mw-group"
	channelID, err := svc.CreateChannel(ctx, admin.ChannelInput{
		MerchantID: merchantID,
		Name:       "mw-chat",
		Vendor:     "fake",
		Type:       store.ChannelTypeOpenAIChat,
		CredGroup:  credGroup,
		BaseURL:    upstreamURL,
	})
	e2eMust(t, err)

	_, err = svc.AddCredential(ctx, admin.CredentialInput{
		MerchantID: merchantID,
		Group:      credGroup,
		Name:       "primary",
		APIKey:     e2eUpstreamKeyFor(domain.ProtocolOpenAIChat),
	})
	e2eMust(t, err)

	effectiveAt := time.Now().Add(-time.Minute)
	for _, mapping := range pluginModelMap {
		_, err = svc.SetModelMap(ctx, admin.ModelMapInput{
			ChannelID:       channelID,
			Model:           mapping.request,
			UpstreamModel:   mapping.upstream,
			PriceMultiplier: "1",
		})
		e2eMust(t, err)
		_, err = svc.PublishPricing(ctx, admin.PublishPricingInput{
			MerchantID:  merchantID,
			Model:       mapping.upstream,
			EffectiveAt: effectiveAt,
			Components:  e2ePricingComponents(),
		})
		e2eMust(t, err)
	}

	accountID, err = svc.CreateAccount(ctx, admin.AccountInput{
		Code:              "mw-account",
		Name:              "中间件剧本账户",
		DefaultMerchantID: &merchantID,
	})
	e2eMust(t, err)

	issued, err := svc.IssueKey(ctx, admin.IssueKeyInput{
		AccountID: accountID, MerchantID: &merchantID, Name: "mw-main",
	})
	e2eMust(t, err)

	_, err = svc.CreditBucket(ctx, admin.CreditBucketInput{
		AccountID:  accountID,
		MerchantID: merchantID,
		Unit:       billing.UnitSettleToken,
		Amount:     "1000000",
		Fallback:   billing.FallbackChargeBalance,
		Source:     billing.SourceRecharge,
	})
	e2eMust(t, err)

	return merchantID, accountID, issued.Plaintext
}

// pluginStartServe 用生产装配函数起真实监听，并按剧本的中间件文件写一份本机清单。
func pluginStartServe(t *testing.T, st *store.Store, pluginPath string) (string, func()) {
	t.Helper()
	stateFile := filepath.Join(t.TempDir(), "plugins.json")
	registry := &plugin.Registry{Plugins: []plugin.Entry{{
		Name: filepath.Base(pluginPath), Path: pluginPath, Enabled: true,
	}}}
	if err := plugin.SaveRegistry(stateFile, registry); err != nil {
		t.Fatalf("写插件清单失败：%v", err)
	}

	gw, err := gateway.New(st, gateway.Options{
		CompleteTimeout: 10 * time.Second,
		PluginStateFile: stateFile,
	})
	e2eMust(t, err)

	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		gw.Close()
		t.Fatalf("监听临时端口失败：%v", err)
	}
	server := &http.Server{Handler: gw.Handler(), ReadHeaderTimeout: gateway.ReadHeaderTimeout}
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gateway.Run(serveCtx, server, ln) }()

	url := "http://" + ln.Addr().String()
	e2eWaitForOK(t, url+gateway.HealthzPath)
	stop := func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("关闭服务端失败：%v", err)
		}
		gw.Close()
	}
	return url, stop
}
