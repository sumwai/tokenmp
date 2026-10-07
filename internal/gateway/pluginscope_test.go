package gateway

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
