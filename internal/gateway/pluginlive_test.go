package gateway

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/plugin"
)

// writePluginFile 写一个中间件文件并返回路径。
func writePluginFile(t *testing.T, dir, name, source string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("写中间件失败：%v", err)
	}
	return path
}

// TestGatewaySkipsBrokenPluginAndReloads 守护网关按本机清单装配。
//
// 两条行为：装不上的插件只记 ERROR 并跳过（不拦住进程启动），以及换装会重读清单 ——
// 后者是「改插件不必重启」的落点。
func TestGatewaySkipsBrokenPluginAndReloads(t *testing.T) {
	dir := t.TempDir()
	good := writePluginFile(t, dir, "good.mw.js", "export function onResponse(body) { return body; }\n")
	broken := writePluginFile(t, dir, "broken.mw.js", "export function onResponse(body) { this is not javascript\n")
	stateFile := filepath.Join(dir, "plugins.json")
	entryGood := plugin.Entry{Name: "good.mw.js", Path: good, Enabled: true}
	entryBroken := plugin.Entry{Name: "broken.mw.js", Path: broken, Enabled: true}
	if err := plugin.SaveRegistry(stateFile, &plugin.Registry{Plugins: []plugin.Entry{entryGood, entryBroken}}); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}

	var logs syncBuffer
	gw, err := newGateway(&fakeGatewayStore{}, gatewayOptions{
		CompleteTimeout: 5 * time.Second,
		PluginStateFile: stateFile,
		PluginLogger:    slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatalf("装不上的插件不应拦住装配：%v", err)
	}
	t.Cleanup(gw.Close)

	if !strings.Contains(logs.String(), "broken.mw.js") {
		t.Fatalf("装不上的插件应被记下来，实际日志：%s", logs.String())
	}
	if got := len(gw.plugins.Current().Stats()); got != 1 {
		t.Fatalf("集合应只含能装的那个，实际 %d 个", got)
	}

	// 清单里换成一个新插件：换装后应只见新集合，不必重启。
	second := writePluginFile(t, dir, "second.mw.js", "export function onResponse(body) { return body; }\n")
	entrySecond := plugin.Entry{Name: "second.mw.js", Path: second, Enabled: true}
	if err := plugin.SaveRegistry(stateFile, &plugin.Registry{Plugins: []plugin.Entry{entryGood, entrySecond}}); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	gw.ReloadPlugins()
	if got := len(gw.plugins.Current().Stats()); got != 2 {
		t.Fatalf("换装后集合应有 2 个插件，实际 %d 个", got)
	}
}
