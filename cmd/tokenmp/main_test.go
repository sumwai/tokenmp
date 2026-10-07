package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantCode     int
		wantInStdout string
		wantInStderr string
	}{
		{
			name:         "version 子命令报版本号并成功退出",
			args:         []string{"version"},
			wantCode:     exitOK,
			wantInStdout: programName + " ",
		},
		{
			name:         "help 打到 stdout 并成功退出",
			args:         []string{"help"},
			wantCode:     exitOK,
			wantInStdout: "用法：",
		},
		{
			name:         "-h 等价于 help",
			args:         []string{"-h"},
			wantCode:     exitOK,
			wantInStdout: "用法：",
		},
		{
			name:         "无参数时用法打到 stderr 并以用法码退出",
			args:         nil,
			wantCode:     exitUsage,
			wantInStderr: "用法：",
		},
		{
			name:         "未知子命令报错并以用法码退出",
			args:         []string{"frobnicate"},
			wantCode:     exitUsage,
			wantInStderr: `未知子命令 "frobnicate"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			got := run(tt.args, &stdout, &stderr)

			if got != tt.wantCode {
				t.Errorf("退出码 = %d，期望 %d", got, tt.wantCode)
			}
			if tt.wantInStdout != "" && !strings.Contains(stdout.String(), tt.wantInStdout) {
				t.Errorf("stdout 未包含 %q\n实际 stdout:\n%s", tt.wantInStdout, stdout.String())
			}
			if tt.wantInStderr != "" && !strings.Contains(stderr.String(), tt.wantInStderr) {
				t.Errorf("stderr 未包含 %q\n实际 stderr:\n%s", tt.wantInStderr, stderr.String())
			}
		})
	}
}

// TestUsageWritesToGivenStream 守护用法文本只写到调用方指定的那个流。
// 两个流都收下同一份文本会让 `tokenmp help 2>/dev/null` 这类用法失去意义。
func TestUsageWritesToGivenStream(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := run([]string{"help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("退出码 = %d，期望 %d", code, exitOK)
	}
	if stdout.Len() == 0 {
		t.Error("help 应写 stdout，实际为空")
	}
	if stderr.Len() != 0 {
		t.Errorf("help 不应写 stderr，实际写到：%q", stderr.String())
	}
}

// TestServeFailsFastOnMissingConfig 守护 serve 的配置缺失退出码。
//
// 配置缺失在读配置阶段就该以运行失败码退出，不触碰数据库；
// 退出码与其它子命令的运行失败口径一致（1），而不是用法码。
func TestServeFailsFastOnMissingConfig(t *testing.T) {
	t.Setenv("TOKENMP_MYSQL_DSN", "")
	var stdout, stderr strings.Builder
	if code := run([]string{"serve"}, &stdout, &stderr); code != exitFailure {
		t.Errorf("退出码 = %d，期望 %d", code, exitFailure)
	}
	if !strings.Contains(stderr.String(), "配置") {
		t.Errorf("stderr 应报配置错误，实际：%s", stderr.String())
	}
}

// usePluginStateFile 把 `admin plugin` 的清单指向临时文件，并清掉 DSN：
// plugin 组只读写本机清单，缺 DSN 也应可用。
func usePluginStateFile(t *testing.T) string {
	t.Helper()
	t.Setenv("TOKENMP_MYSQL_DSN", "")
	stateFile := filepath.Join(t.TempDir(), "plugins.json")
	t.Setenv("TOKENMP_PLUGIN_STATE_FILE", stateFile)
	return stateFile
}

// writePluginSource 写一个中间件文件。
func writePluginSource(t *testing.T, path, source string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("写插件文件失败：%v", err)
	}
	return path
}

// runAdminPlugin 跑一次 plugin 动作，返回退出码与输出。
func runAdminPlugin(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := run(append([]string{"admin", "plugin"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestAdminPluginRegistryLifecycle 守护清单的增删改查全程不连数据库。
//
// plugin 组只读写本机清单：缺 DSN 时也要能列、能停用 —— 插件装不上导致 serve 起不来时，
// 停用它的路径正是这条。
func TestAdminPluginRegistryLifecycle(t *testing.T) {
	stateFile := usePluginStateFile(t)
	path := filepath.Join(t.TempDir(), "list.mw.js")
	writePluginSource(t, path, `export const events = ["text_delta"];
`+
		`export function onRequest(body) { return body; }
`+
		`export function onEvent(event) { return event; }
`)

	if code, stdout, stderr := runAdminPlugin(t, "add", path); code != exitOK {
		t.Fatalf("注册退出码 = %d，期望 %d，stderr：%s", code, exitOK, stderr)
	} else if !strings.Contains(stdout, "已注册") {
		t.Errorf("注册输出应说明结果，实际：%s", stdout)
	}

	if code, stdout, stderr := runAdminPlugin(t, "list"); code != exitOK {
		t.Fatalf("列清单退出码 = %d，期望 %d，stderr：%s", code, exitOK, stderr)
	} else {
		for _, want := range []string{"list.mw.js", "true", "onEvent", "ok"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("清单输出缺少 %q，实际：%s", want, stdout)
			}
		}
	}
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("清单文件应当已写入：%v", err)
	}

	if code, stdout, _ := runAdminPlugin(t, "disable", "list.mw.js"); code != exitOK || !strings.Contains(stdout, "已停用") {
		t.Fatalf("停用失败：退出码 %d，输出 %s", code, stdout)
	}
	if _, stdout, _ := runAdminPlugin(t, "list"); !strings.Contains(stdout, "false") {
		t.Fatalf("停用后清单应显示 enabled=false，实际：%s", stdout)
	}

	if code, stdout, _ := runAdminPlugin(t, "enable", "list.mw.js"); code != exitOK || !strings.Contains(stdout, "已启用") {
		t.Fatalf("启用失败：退出码 %d，输出 %s", code, stdout)
	}

	if code, stdout, _ := runAdminPlugin(t, "del", "list.mw.js"); code != exitOK || !strings.Contains(stdout, "已移除") {
		t.Fatalf("移除失败：退出码 %d，输出 %s", code, stdout)
	}
	if _, stdout, _ := runAdminPlugin(t, "list"); !strings.Contains(stdout, "清单为空") {
		t.Fatalf("移除后清单应为空，实际：%s", stdout)
	}
}

// TestAdminPluginAddRejectsUnusablePath 守护装不上的插件不会被写进清单。
//
// 写进去只会让 serve 起不来，而在 add 这一步拦住时，报错与文件是一对一的。
func TestAdminPluginAddRejectsUnusablePath(t *testing.T) {
	stateFile := usePluginStateFile(t)
	bad := writePluginSource(t, filepath.Join(t.TempDir(), "bad.mw.js"),
		"export function onRequest(body) { this is not javascript\n")

	code, _, stderr := runAdminPlugin(t, "add", bad)
	if code != exitFailure {
		t.Fatalf("退出码 = %d，期望 %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "装不上") {
		t.Errorf("stderr 应说明未写入清单的原因，实际：%s", stderr)
	}
	if _, err := os.Stat(stateFile); err == nil {
		t.Fatal("装不上时不应创建或改动清单")
	}
}

// TestAdminPluginCheckReportsVerdicts 守护离线校验逐项给结论、按结果定退出码。
//
// 与 list 的分工在这里体现：check 接受任意路径、不读清单，也不需要数据库；
// 一个坏文件不影响其余文件的结论。
func TestAdminPluginCheckReportsVerdicts(t *testing.T) {
	dir := t.TempDir()
	good := writePluginSource(t, filepath.Join(dir, "good.mw.js"),
		"export function onRequest(body) { return body; }\n")
	bad := writePluginSource(t, filepath.Join(dir, "bad.mw.js"),
		"export function onRequest(body) { this is not javascript\n")

	t.Setenv("TOKENMP_MYSQL_DSN", "")
	t.Setenv("TOKENMP_PLUGIN_STATE_FILE", filepath.Join(t.TempDir(), "plugins.json"))

	t.Run("全部可用", func(t *testing.T) {
		var stdout, stderr strings.Builder
		if code := run([]string{"admin", "plugin", "check", good}, &stdout, &stderr); code != exitOK {
			t.Fatalf("退出码 = %d，期望 %d，stderr：%s", code, exitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ok") || !strings.Contains(stdout.String(), "onRequest") {
			t.Errorf("输出应含结论与钩子，实际：%s", stdout.String())
		}
	})

	t.Run("一项坏仍报出其余项", func(t *testing.T) {
		var stdout, stderr strings.Builder
		if code := run([]string{"admin", "plugin", "check", good, bad}, &stdout, &stderr); code != exitFailure {
			t.Fatalf("退出码 = %d，期望 %d", code, exitFailure)
		}
		logged := stdout.String()
		if !strings.Contains(logged, "good.mw.js") || !strings.Contains(logged, "bad.mw.js") {
			t.Errorf("两个文件都应在结论里，实际：%s", logged)
		}
		if !strings.Contains(logged, "fail") {
			t.Errorf("坏文件应标记为 fail，实际：%s", logged)
		}
	})

	t.Run("未给路径", func(t *testing.T) {
		var stdout, stderr strings.Builder
		if code := run([]string{"admin", "plugin", "check"}, &stdout, &stderr); code != exitUsage {
			t.Fatalf("退出码 = %d，期望 %d", code, exitUsage)
		}
	})
}

// TestAdminPluginListReportsBrokenEntry 守护清单里装不上的项被报出来而不是让 list 失败。
//
// list 的职责是显示状态：退出码 0 表示「列出来了」，结论列才是给脚本看的信号；
// 需要按结论定退出码的场合用 check。清单本身读不出来则仍按配置错误报出。
func TestAdminPluginListReportsBrokenEntry(t *testing.T) {
	stateFile := usePluginStateFile(t)
	content := `{"plugins":[{"name":"missing.mw.js","path":"/nope/missing.mw.js","enabled":true}]}`
	if err := os.WriteFile(stateFile, []byte(content), 0o600); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}

	code, stdout, stderr := runAdminPlugin(t, "list")
	if code != exitOK {
		t.Fatalf("退出码 = %d，期望 %d，stderr：%s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "fail") || !strings.Contains(stdout, "missing.mw.js") {
		t.Errorf("输出应点名装不上的项，实际：%s", stdout)
	}
}

// TestAdminPluginListRejectsBrokenRegistry 守护清单文件本身读不出来时报错。
func TestAdminPluginListRejectsBrokenRegistry(t *testing.T) {
	stateFile := usePluginStateFile(t)
	if err := os.WriteFile(stateFile, []byte("{"), 0o600); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}

	code, _, stderr := runAdminPlugin(t, "list")
	if code != exitFailure {
		t.Fatalf("退出码 = %d，期望 %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "错误") {
		t.Errorf("stderr 应报清单错误，实际：%s", stderr)
	}
}
