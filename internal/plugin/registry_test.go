package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRegistryRoundTrip 验证清单写入后能原样读回。
func TestRegistryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "plugins.json")
	registry := &Registry{Plugins: []Entry{
		{Name: "first.mw.js", Path: "/opt/a.mw.js", Enabled: true},
		{Name: "second.mw.js", Path: "/opt/b.mw.js", Enabled: false},
	}}
	if err := SaveRegistry(path, registry); err != nil {
		t.Fatalf("写入清单失败：%v", err)
	}
	loaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("读取清单失败：%v", err)
	}
	if len(loaded.Plugins) != 2 {
		t.Fatalf("条目数 = %d，期望 2", len(loaded.Plugins))
	}
	for i, want := range registry.Plugins {
		if loaded.Plugins[i] != want {
			t.Errorf("第 %d 项 = %+v，期望 %+v", i, loaded.Plugins[i], want)
		}
	}
}

// TestLoadRegistryMissingFile 验证清单不存在时得到空清单而不是错误。
//
// 首次部署时文件还不存在，此时应当是「没注册任何插件」，而不是启动失败。
func TestLoadRegistryMissingFile(t *testing.T) {
	registry, err := LoadRegistry(filepath.Join(t.TempDir(), "plugins.json"))
	if err != nil {
		t.Fatalf("文件不存在不应报错：%v", err)
	}
	if len(registry.Plugins) != 0 {
		t.Fatalf("条目数 = %d，期望 0", len(registry.Plugins))
	}
}

// TestLoadRegistryRejectsBrokenContent 验证清单形状非法时报错。
//
// 清单由本机命令写入：读不出来就意味着有人手改坏了，静默忽略会让插件无声地不生效。
func TestLoadRegistryRejectsBrokenContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "不是 JSON", content: "{"},
		{name: "键名不认识", content: `{"plugins":[{"name":"a","path":"/a","enabled":true,"extra":1}]}`},
		{name: "名字为空", content: `{"plugins":[{"name":"","path":"/a","enabled":true}]}`},
		{name: "路径为空", content: `{"plugins":[{"name":"a","path":"","enabled":true}]}`},
		{name: "名字重复", content: `{"plugins":[{"name":"a","path":"/a","enabled":true},{"name":"a","path":"/b","enabled":true}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plugins.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("写清单失败：%v", err)
			}
			if _, err := LoadRegistry(path); err == nil {
				t.Fatal("非法清单应当报错")
			}
		})
	}
}

// TestRegistryMutations 验证增删改的约束。
func TestRegistryMutations(t *testing.T) {
	registry := &Registry{}
	first := Entry{Name: "a", Path: "/a.mw.js", Enabled: true}
	if err := registry.Add(first); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if err := registry.Add(Entry{Name: "a", Path: "/other.mw.js"}); err == nil {
		t.Error("重名应当报错")
	}
	// 同一文件重复注册会让它的钩子被调用两遍，必须拦下。
	if err := registry.Add(Entry{Name: "b", Path: "/a.mw.js"}); err == nil {
		t.Error("重路径应当报错")
	}
	if err := registry.Add(Entry{Name: "", Path: "/c.mw.js"}); err == nil {
		t.Error("缺名字应当报错")
	}

	if err := registry.SetEnabled("a", false); err != nil {
		t.Fatalf("停用失败：%v", err)
	}
	if entry, ok := registry.Find("a"); !ok || entry.Enabled {
		t.Fatalf("停用未生效：%+v", entry)
	}
	if err := registry.SetEnabled("missing", true); err == nil {
		t.Error("改不存在的项应当报错")
	}

	if err := registry.Remove("a"); err != nil {
		t.Fatalf("移除失败：%v", err)
	}
	if len(registry.Plugins) != 0 {
		t.Fatalf("移除后仍有 %d 项", len(registry.Plugins))
	}
	if err := registry.Remove("a"); err == nil {
		t.Error("移除不存在的项应当报错")
	}
}

// TestEnabledPathsKeepsOrder 验证取用路径按注册顺序给出，并跳过停用项。
//
// 顺序就是装配顺序：多个插件的同名钩子按顺序依次改写，先后不同结果不同。
func TestEnabledPathsKeepsOrder(t *testing.T) {
	registry := &Registry{Plugins: []Entry{
		{Name: "c", Path: "/c.mw.js", Enabled: true},
		{Name: "b", Path: "/b.mw.js", Enabled: false},
		{Name: "a", Path: "/a.mw.js", Enabled: true},
	}}
	got := registry.EnabledPaths()
	want := []string{"/c.mw.js", "/a.mw.js"}
	if len(got) != len(want) {
		t.Fatalf("路径 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项 = %q，期望 %q", i, got[i], want[i])
		}
	}
}

// TestSaveRegistryRejectsInvalidEntry 验证非法条目不会被写进文件。
func TestSaveRegistryRejectsInvalidEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.json")
	registry := &Registry{Plugins: []Entry{{Name: "", Path: "/a.mw.js"}}}
	if err := SaveRegistry(path, registry); err == nil {
		t.Fatal("非法条目应当拒绝保存")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("拒绝保存时不应留下文件")
	}
}
