package gateway

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/plugin"
	"github.com/sumwai/tokenmp/internal/store"
)

// stubScopeReader 是漂移检查的配置读取面替身。
type stubScopeReader struct {
	channels []store.Channel
	maps     []store.ModelMap
	err      error
}

func (s stubScopeReader) ListChannels(context.Context) ([]store.Channel, error) {
	return s.channels, s.err
}

func (s stubScopeReader) ListModelMaps(context.Context) ([]store.ModelMap, error) {
	return s.maps, s.err
}

// loadScopedMiddleware 写一个声明了作用域的中间件并加载它。
func loadScopedMiddleware(t *testing.T, scope string) *plugin.Set {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "scoped.mw.js")
	source := scope + "\nexport function onResponse(body) { return body; }\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("写中间件失败：%v", err)
	}
	set, err := plugin.Load([]string{path}, plugin.Options{})
	if err != nil {
		t.Fatalf("加载中间件失败：%v", err)
	}
	return set
}

// syncBuffer 是可并发读写的日志缓冲。
//
// 重载后的漂移检查跑在后台 goroutine 上，测试一边等结果一边读日志，
// 直接用 bytes.Buffer 会与写入方竞争。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// notifyingScopeReader 在每次读取渠道时通知一次，用于观察检查是否真的跑起来。
type notifyingScopeReader struct{ called chan struct{} }

func (n notifyingScopeReader) ListChannels(context.Context) ([]store.Channel, error) {
	select {
	case n.called <- struct{}{}:
	default:
	}
	return nil, nil
}

func (n notifyingScopeReader) ListModelMaps(context.Context) ([]store.ModelMap, error) {
	return nil, nil
}

// TestScheduleScopeDriftCheckRunsInBackground 守护换代后的漂移检查会在后台跑出结果。
//
// 重载发生在某个请求的 goroutine 上，检查要读配置；它必须在别处执行，
// 且结果与启动期那一遍同口径（缺失的名字被点名）。
func TestScheduleScopeDriftCheckRunsInBackground(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	set := loadScopedMiddleware(t, `export const scope = { vendors: ["gone"] };`)
	reader := stubScopeReader{channels: []store.Channel{{Vendor: "minimax", Type: store.ChannelTypeOpenAIChat}}}

	gw := &Gateway{}
	gw.scheduleScopeDriftCheck(logger, set, reader)

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "gone") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), "gone") {
		t.Fatalf("后台检查应报出缺失的名字，实际日志：%s", logs.String())
	}
}

// TestScheduleScopeDriftCheckSkipsWhileRunning 守护已有一轮在跑时不再排一轮。
//
// 连续重载不该堆起一串配置查询。
func TestScheduleScopeDriftCheckSkipsWhileRunning(t *testing.T) {
	set := loadScopedMiddleware(t, `export const scope = { vendors: ["gone"] };`)
	gw := &Gateway{}
	gw.scopeCheck.Store(true) // 假装已有一轮在跑

	called := make(chan struct{}, 1)
	gw.scheduleScopeDriftCheck(slog.New(slog.DiscardHandler), set, notifyingScopeReader{called: called})

	select {
	case <-called:
		t.Fatal("已有一轮在跑时不应再排一轮")
	case <-time.After(50 * time.Millisecond):
	}
}

// writeRegistryEntry 写一个中间件文件并返回它的清单条目。
func writeRegistryEntry(t *testing.T, dir, name, source string) plugin.Entry {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(source+"\nexport function onResponse(body) { return body; }\n"), 0o600); err != nil {
		t.Fatalf("写中间件失败：%v", err)
	}
	return plugin.Entry{Name: name, Path: path, Enabled: true}
}

// writeRegistryFile 写本机插件清单。
func writeRegistryFile(t *testing.T, path string, entries ...plugin.Entry) {
	t.Helper()
	if err := plugin.SaveRegistry(path, &plugin.Registry{Plugins: entries}); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
}

// TestReloadPluginsRereadsRegistry 守护外部触发会重读清单并换装。
//
// 新增、停用、删除与文件修好后的重新装配都靠这条路径，不需要重启进程。
func TestReloadPluginsRereadsRegistry(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "plugins.json")
	first := writeRegistryEntry(t, dir, "first.mw.js", "")
	writeRegistryFile(t, stateFile, first)

	live, err := plugin.NewLive(stateFile, plugin.Options{})
	if err != nil {
		t.Fatalf("初次装配失败：%v", err)
	}
	if got := len(live.Current().Stats()); got != 1 {
		t.Fatalf("初次装配应有 1 个插件，实际 %d", got)
	}

	swapped := 0
	live.SetOnSwap(func() { swapped++ })
	second := writeRegistryEntry(t, dir, "second.mw.js", "")
	writeRegistryFile(t, stateFile, first, second)

	gw := &Gateway{plugins: live}
	gw.ReloadPlugins()
	if got := len(live.Current().Stats()); got != 2 {
		t.Fatalf("换装后应有 2 个插件，实际 %d", got)
	}
	if swapped != 1 {
		t.Fatalf("换装回调应触发一次，实际 %d", swapped)
	}

	var nilGateway *Gateway
	nilGateway.ReloadPlugins()
	(&Gateway{}).ReloadPlugins()
}

// TestWarnScopeDrift 守护「声明了却在当前配置里不存在」的名字会被报出来。
//
// 三个名字轴都靠运营在数据里命名：改一次 vendor 标签就会让依赖它的中间件静默失效，
// 而静默不匹配是这类设计最大的成本。存在的那一个不该报，缺失的必须报。
func TestWarnScopeDrift(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	set := loadScopedMiddleware(t, `export const scope = { vendors: ["minimax", "gone"], models: ["MiniMax-M3"], protocols: ["openai_chat"] };`)

	reader := stubScopeReader{
		channels: []store.Channel{
			{Vendor: "minimax", Type: store.ChannelTypeOpenAIChat},
			{Vendor: "zai", Type: store.ChannelTypeAnthropicMessages},
		},
		maps: []store.ModelMap{{Model: "MiniMax-M3", Enabled: true}},
	}
	warnScopeDrift(context.Background(), logger, set, reader)

	logged := logs.String()
	if !strings.Contains(logged, "gone") {
		t.Fatalf("缺失的 vendor 应当被报出来，实际日志：%s", logged)
	}
	if strings.Contains(logged, `value=minimax`) {
		t.Fatalf("存在的 vendor 不该被报，实际日志：%s", logged)
	}
	if strings.Contains(logged, "MiniMax-M3") || strings.Contains(logged, "openai_chat") {
		t.Fatalf("存在的模型名与方言不该被报，实际日志：%s", logged)
	}
}

// TestWarnScopeDriftSkipsDisabledModelMap 守护只有启用的模型映射才算「存在」。
//
// 停用一条映射之后，按该模型限定的中间件就再也命中不了它，这属于真实漂移。
func TestWarnScopeDriftSkipsDisabledModelMap(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	set := loadScopedMiddleware(t, `export const scope = { models: ["retired-model"] };`)

	reader := stubScopeReader{maps: []store.ModelMap{{Model: "retired-model", Enabled: false}}}
	warnScopeDrift(context.Background(), logger, set, reader)

	if !strings.Contains(logs.String(), "retired-model") {
		t.Fatalf("被停用的模型名应当被报出来，实际日志：%s", logs.String())
	}
}

// TestWarnScopeDriftToleratesReadFailure 守护读配置失败只告警、不拦启动。
func TestWarnScopeDriftToleratesReadFailure(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	set := loadScopedMiddleware(t, `export const scope = { vendors: ["minimax"] };`)

	warnScopeDrift(context.Background(), logger, set, stubScopeReader{err: context.DeadlineExceeded})

	if !strings.Contains(logs.String(), "漂移检查跳过") {
		t.Fatalf("读取失败应留下一条告警，实际日志：%s", logs.String())
	}
}
