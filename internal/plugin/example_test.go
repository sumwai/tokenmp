package plugin

import (
	"path/filepath"
	"testing"
)

// TestExampleMiddlewareAssembles 守护仓库内示例插件始终可装配。
//
// 示例是文档里那份「think 标签搬运」中间件的可运行版本。没有这条用例，引擎或宿主
// 契约定变化时最先失效的就是它，而且没人会注意到 —— 它不在任何配置里，也不被转发路径引用。
func TestExampleMiddlewareAssembles(t *testing.T) {
	pattern := filepath.Join("..", "..", "examples", "*.mw.js")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("枚举示例失败：%v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("没有找到示例插件：%s", pattern)
	}

	set, err := Load(paths, Options{})
	if err != nil {
		t.Fatalf("示例插件应当可装配：%v", err)
	}
	for _, info := range set.Stats() {
		if len(info.Hooks) == 0 {
			t.Fatalf("示例 %s 至少应导出一个钩子", info.Name)
		}
	}
}
