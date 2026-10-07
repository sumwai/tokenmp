package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
)

// liveValue 在一个 Live 上跑一次请求改写，返回插件写入的标记。
func liveValue(t *testing.T, live *Live) string {
	t.Helper()
	req := chatRequest(chatBody)
	ctx, release := live.BeginRequest(context.Background(), req)
	defer release()
	if err := live.OnRequest(ctx, openaichat.New(), req); err != nil {
		t.Fatalf("请求改写失败：%v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(req.RawBody, &fields); err != nil {
		t.Fatalf("解析请求体失败：%v", err)
	}
	value, _ := fields["v"].(string)
	return value
}

// saveRegistry 写一份清单并让用例失败于写入错误。
func saveRegistry(t *testing.T, path string, entries ...Entry) {
	t.Helper()
	if err := SaveRegistry(path, &Registry{Plugins: entries}); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
}

// TestLoadAvailableSkipsBroken 守护运行期装配跳过坏项、保留其余项与顺序。
//
// 严格版 Load 仍是整批失败：注册前校验与离线检查要的正是「这些文件都必须能用」。
func TestLoadAvailableSkipsBroken(t *testing.T) {
	dir := t.TempDir()
	first := writeSource(t, dir, "first.mw.js", `export function onRequest(body) { body.v = (body.v ?? "") + "1"; return body; }`)
	broken := writeSource(t, dir, "broken.mw.js", `export function onRequest(body) { this is not javascript`)
	last := writeSource(t, dir, "last.mw.js", `export function onRequest(body) { body.v = (body.v ?? "") + "3"; return body; }`)
	paths := []string{first, broken, last}

	set, failures := LoadAvailable(paths, Options{})
	if len(failures) != 1 {
		t.Fatalf("失败项数 = %d，期望 1", len(failures))
	}
	if failures[0].Path != broken || failures[0].Reason == nil {
		t.Fatalf("失败项应带路径与原因，实际 %+v", failures[0])
	}
	if got := len(set.Stats()); got != 2 {
		t.Fatalf("集合应有 2 个插件，实际 %d", got)
	}

	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("请求改写失败：%v", err)
	}
	if got := rawField(t, req, "v"); got != "13" {
		t.Fatalf("集合内顺序应与传入一致，得到 %q", got)
	}

	if _, err := Load(paths, Options{}); err == nil {
		t.Fatal("严格版应当在有坏项时整批失败")
	}
}

// TestLiveReloadPicksUpRegistryChanges 守护清单的增删改在换装后生效。
//
// 这是「不重启进程就能改插件」的核心：新增、停用、删掉，以及把写坏的文件修好。
func TestLiveReloadPicksUpRegistryChanges(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "plugins.json")
	a := writeSource(t, dir, "a.mw.js", `export function onRequest(body) { body.v = (body.v ?? "") + "a"; return body; }`)
	b := writeSource(t, dir, "b.mw.js", `export function onRequest(body) { body.v = (body.v ?? "") + "b"; return body; }`)
	entryA := Entry{Name: "a.mw.js", Path: a, Enabled: true}
	entryB := Entry{Name: "b.mw.js", Path: b, Enabled: true}

	saveRegistry(t, stateFile, entryA)
	live, err := NewLive(stateFile, Options{})
	if err != nil {
		t.Fatalf("初次装配失败：%v", err)
	}
	if got := liveValue(t, live); got != "a" {
		t.Fatalf("初次装配应只跑 a，得到 %q", got)
	}

	saveRegistry(t, stateFile, entryA, entryB)
	live.Reload()
	if got := liveValue(t, live); got != "ab" {
		t.Fatalf("新增后应跑 a+b，得到 %q", got)
	}

	disabled := entryA
	disabled.Enabled = false
	saveRegistry(t, stateFile, disabled, entryB)
	live.Reload()
	if got := liveValue(t, live); got != "b" {
		t.Fatalf("停用 a 后应只跑 b，得到 %q", got)
	}

	// 重新启用但把文件写坏：坏的那一个被跳过，其余照常。
	writeSource(t, dir, "a.mw.js", `export function onRequest(body) { this is not javascript`)
	saveRegistry(t, stateFile, entryA, entryB)
	live.Reload()
	if got := liveValue(t, live); got != "b" {
		t.Fatalf("装不上的插件应被跳过，得到 %q", got)
	}

	saveRegistry(t, stateFile, entryB)
	live.Reload()
	if got := liveValue(t, live); got != "b" {
		t.Fatalf("删除 a 后应只跑 b，得到 %q", got)
	}
}

// TestLiveKeepsSetWhenRegistryBroken 守护清单读不出来时保留当前集合。
//
// 配置读坏的那一瞬间把插件全部摘掉，比继续用旧清单更危险。
func TestLiveKeepsSetWhenRegistryBroken(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "plugins.json")
	a := writeSource(t, dir, "a.mw.js", `export function onRequest(body) { body.v = "a"; return body; }`)
	saveRegistry(t, stateFile, Entry{Name: "a.mw.js", Path: a, Enabled: true})

	live, err := NewLive(stateFile, Options{})
	if err != nil {
		t.Fatalf("初次装配失败：%v", err)
	}
	if err := os.WriteFile(stateFile, []byte("{"), 0o600); err != nil {
		t.Fatalf("写坏清单失败：%v", err)
	}
	live.Reload()
	if got := liveValue(t, live); got != "a" {
		t.Fatalf("清单读坏时应保留当前集合，得到 %q", got)
	}
}

// TestLiveReloadPicksUpFingerprintInvisibleChange 守护换装不依赖文件指纹。
//
// 指纹是「修改时间 + 大小」：等长内容加复原的时间戳落在它看不见的区间里，惰性重编译
// 不会发现。换装是重新装配一整代（新的集合、新的编译缓存），因此能拿到它。
func TestLiveReloadPicksUpFingerprintInvisibleChange(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "plugins.json")
	path := writeSource(t, dir, "forced.mw.js", `export function onRequest(body) { body.v = "v1"; return body; }`)
	saveRegistry(t, stateFile, Entry{Name: "forced.mw.js", Path: path, Enabled: true})

	live, err := NewLive(stateFile, Options{ReloadBackoff: time.Minute})
	if err != nil {
		t.Fatalf("初次装配失败：%v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取文件状态失败：%v", err)
	}
	if got := liveValue(t, live); got != "v1" {
		t.Fatalf("首版插件未生效，得到 %q", got)
	}

	// 等长内容 + 复原修改时间：指纹看不出变化。
	writeSource(t, dir, "forced.mw.js", `export function onRequest(body) { body.v = "v2"; return body; }`)
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("复原文件时间失败：%v", err)
	}
	if got := liveValue(t, live); got != "v1" {
		t.Fatalf("指纹相同的改动不该被惰性重编译发现，得到 %q", got)
	}

	live.Reload()
	if got := liveValue(t, live); got != "v2" {
		t.Fatalf("换装应换成新产物，得到 %q", got)
	}
}

// TestLiveWithoutStateFileIsEmpty 守护没配清单时是空集合而不是报错。
func TestLiveWithoutStateFileIsEmpty(t *testing.T) {
	live, err := NewLive("", Options{})
	if err != nil {
		t.Fatalf("空路径不应报错：%v", err)
	}
	if !live.Empty() {
		t.Fatal("空路径应当是空集合")
	}
	req := chatRequest(chatBody)
	ctx, release := live.BeginRequest(context.Background(), req)
	defer release()
	if err := live.OnRequest(ctx, openaichat.New(), req); err != nil {
		t.Fatalf("空集合不应报错：%v", err)
	}
}
