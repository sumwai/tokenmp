package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sumwai/tokenmp/internal/config"
	"github.com/sumwai/tokenmp/internal/plugin"
)

// 本文件是 `admin plugin` 的命令层：维护本机的中间件清单，并做离线校验。
//
// 该组只读写清单文件，不连数据库：清单是本机事实（插件路径只在这台机器上成立），
// 为了列插件或停用一个坏插件而要求 DSN 可用，会把最需要它的场合挡在门外。
// 清单文件的路径取自 TOKENMP_PLUGIN_STATE_FILE，默认见 internal/config。

// pluginGroupName 是管理面里的插件组名。
const pluginGroupName = "plugin"

// adminPlugin 分发 plugin 组动作。
func adminPlugin(_ context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("plugin 需要动作：add / list / enable / disable / del / check")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionList:
		return adminPluginList(rest, env)
	case actionAdd:
		return adminPluginAdd(rest, env)
	case actionEnable:
		return adminPluginSetEnabled(rest, env, true)
	case actionDisable:
		return adminPluginSetEnabled(rest, env, false)
	case actionDel:
		return adminPluginDel(rest, env)
	case "check":
		return adminPluginCheck(rest, env)
	default:
		return env.usageErrorf("plugin 未知动作 %q", action)
	}
}

// pluginListLogger 构造写向 stderr 的 logger。
//
// 传零值 Options 时内部落到 DiscardHandler，于是「scope 非法」「导出 onEvent 却没声明
// events」「作用域取值在当前配置里不存在」这类告警全部消失 —— 而离线检查插件正是发现
// 它们的场合。告警走 stderr，不混进 stdout 的清单表。
func pluginListLogger(stderr io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(stderr, nil))
}

// pluginProbe 是一次装配探测的结果。
type pluginProbe struct {
	Name   string
	OK     bool
	Hooks  []string
	Events []string
	Error  string
}

// probePlugin 装配一个路径并收集结果，不改动清单。
//
// 名字取插件层自己算出的入口名（入口文件名或包目录名），与运行时报告的一致；
// 装配失败时退回路径的基名，好在输出里定位。
func probePlugin(path string, env *adminEnv) pluginProbe {
	set, err := plugin.Load([]string{path}, plugin.Options{Logger: pluginListLogger(env.stderr)})
	if err != nil {
		return pluginProbe{Name: filepath.Base(path), Error: err.Error()}
	}
	probe := pluginProbe{Name: filepath.Base(path), OK: true}
	if infos := set.Stats(); len(infos) == 1 {
		probe.Name = infos[0].Name
		probe.Hooks = infos[0].Hooks
		probe.Events = infos[0].Events
	}
	return probe
}

// loadRegistry 读出本机清单。
func loadRegistry() (*plugin.Registry, error) {
	return plugin.LoadRegistry(config.LoadPluginStateFile())
}

// saveRegistry 写回本机清单。
func saveRegistry(registry *plugin.Registry) error {
	return plugin.SaveRegistry(config.LoadPluginStateFile(), registry)
}

// adminPluginAdd 注册一个插件：先装配校验，通过后才写入清单。
//
// 校验前置的理由与启动期一致：装不上的条目写进清单只会让 serve 起不来，
// 而在 add 这一步拦住时，报错信息与「哪一个文件、什么原因」是一对一的。
func adminPluginAdd(args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin plugin add")
	name := fs.String(flagName, "", "插件名字；缺省取入口文件名或包目录名")
	disabled := fs.Bool("disabled", false, "注册但不启用")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return env.usageError("plugin add 需要一个插件文件或包目录路径")
	}
	abs, err := filepath.Abs(rest[0])
	if err != nil {
		return env.fail(fmt.Errorf("解析路径失败：%w", err))
	}
	probe := probePlugin(abs, env)
	if !probe.OK {
		return env.fail(fmt.Errorf("%s 装不上，未写入清单：%s", abs, probe.Error))
	}

	entryName := strings.TrimSpace(*name)
	if entryName == "" {
		entryName = probe.Name
	}
	registry, err := loadRegistry()
	if err != nil {
		return env.fail(err)
	}
	entry := plugin.Entry{Name: entryName, Path: abs, Enabled: !*disabled}
	if err := registry.Add(entry); err != nil {
		return env.fail(err)
	}
	if err := saveRegistry(registry); err != nil {
		return env.fail(err)
	}
	return env.printf("已注册 %s（%s，%s）\n", entry.Name, entry.Path, enabledText(entry.Enabled))
}

// adminPluginSetEnabled 启用或停用一个已注册的插件。
func adminPluginSetEnabled(args []string, env *adminEnv, enabled bool) int {
	action := actionDisable
	if enabled {
		action = actionEnable
	}
	fs := env.newFlagSet("admin plugin " + action)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return env.usageErrorf("plugin %s 需要一个插件名字", action)
	}
	registry, err := loadRegistry()
	if err != nil {
		return env.fail(err)
	}
	if err := registry.SetEnabled(rest[0], enabled); err != nil {
		return env.fail(err)
	}
	if err := saveRegistry(registry); err != nil {
		return env.fail(err)
	}
	return env.printf("%s %s\n", enabledText(enabled), rest[0])
}

// adminPluginDel 从清单里移除一项；插件文件本身不动。
func adminPluginDel(args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin plugin del")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return env.usageError("plugin del 需要一个插件名字")
	}
	registry, err := loadRegistry()
	if err != nil {
		return env.fail(err)
	}
	if err := registry.Remove(rest[0]); err != nil {
		return env.fail(err)
	}
	if err := saveRegistry(registry); err != nil {
		return env.fail(err)
	}
	return env.printf("已移除 %s（插件文件未删除）\n", rest[0])
}

// pluginListRow 是清单里一项的可观测快照；JSON 输出直接序列化它。
type pluginListRow struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Enabled bool     `json:"enabled"`
	OK      bool     `json:"ok"`
	Hooks   []string `json:"hooks,omitempty"`
	Events  []string `json:"events,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// adminPluginList 输出本机清单，并对每一项做一次装配探测。
//
// 停用项也探测：运营停用一个插件往往是因为它装不上，能一眼看出「现在能不能重新启用」
// 比只显示一个 enabled=false 有用。
func adminPluginList(args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin plugin list")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		return env.usageError("plugin list 不接受位置参数")
	}
	registry, err := loadRegistry()
	if err != nil {
		return env.fail(err)
	}

	rows := make([]pluginListRow, 0, len(registry.Plugins))
	table := make([][]string, 0, len(registry.Plugins))
	for _, entry := range registry.Plugins {
		probe := probePlugin(entry.Path, env)
		rows = append(rows, pluginListRow{
			Name: entry.Name, Path: entry.Path, Enabled: entry.Enabled,
			OK: probe.OK, Hooks: probe.Hooks, Events: probe.Events, Error: probe.Error,
		})
		verdict, detail := "ok", strings.Join(probe.Hooks, ",")
		if !probe.OK {
			verdict, detail = "fail", probe.Error
		}
		table = append(table, []string{
			entry.Name, entry.Path, strconv.FormatBool(entry.Enabled), verdict, detail,
			strings.Join(probe.Events, ","),
		})
	}
	headers := []string{flagName, "path", "enabled", "result", "hooks", "events"}
	if code := env.emit(*asJSON, headers, table, rows); code != exitOK {
		return code
	}
	if len(rows) == 0 {
		return env.printf("清单为空：用 `%s admin plugin add <路径>` 注册插件\n", programName)
	}
	return exitOK
}

// pluginCheckResult 是一项路径的校验结果；JSON 输出直接序列化它。
type pluginCheckResult struct {
	Name   string   `json:"name"`
	Path   string   `json:"path"`
	OK     bool     `json:"ok"`
	Hooks  []string `json:"hooks,omitempty"`
	Events []string `json:"events,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// adminPluginCheck 逐个装配给定路径并报告结果，不读写清单。
//
// 它回答的是「这个文件能不能用」，因此接受任意路径：写插件时不必先注册，
// 注册前想验证、或校验一份还没放进部署目录的草稿，都走这里。
func adminPluginCheck(args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin plugin check")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	paths := fs.Args()
	if len(paths) == 0 {
		return env.usageError("plugin check 需要至少一个插件文件或包目录路径")
	}

	results := make([]pluginCheckResult, 0, len(paths))
	failed := false
	for _, path := range paths {
		probe := probePlugin(path, env)
		result := pluginCheckResult{
			Name: probe.Name, Path: path, OK: probe.OK,
			Hooks: probe.Hooks, Events: probe.Events, Error: probe.Error,
		}
		if !probe.OK {
			failed = true
		}
		results = append(results, result)
	}

	rows := make([][]string, 0, len(results))
	for _, result := range results {
		verdict, detail := "ok", strings.Join(result.Hooks, ",")
		if !result.OK {
			verdict, detail = "fail", result.Error
		}
		rows = append(rows, []string{
			result.Name, result.Path, verdict, detail, strings.Join(result.Events, ","),
		})
	}
	if code := env.emit(*asJSON, []string{flagName, "path", "result", "hooks", "events"}, rows, results); code != exitOK {
		return code
	}
	if failed {
		return exitFailure
	}
	return exitOK
}

// enabledText 把启用状态翻成输出用的文案。
func enabledText(enabled bool) string {
	if enabled {
		return "已启用"
	}
	return "已停用"
}
