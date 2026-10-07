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

// 本文件是 `admin plugin` 的命令层：列出按 TOKENMP_PLUGIN_FILES 配置加载的中间件。
//
// 该组只读进程配置，不连数据库：列插件不需要存储层，为了它强制要求 DSN 会把一件
// 纯配置查询变成需要数据库可用才能做的事。统计是进程内的，独立进程里列出时通常为零，
// 只有 serve 进程自己观测到的计数才有意义。

// pluginGroupName 是管理面里的插件组名。
const pluginGroupName = "plugin"

// adminPlugin 分发 plugin 组动作。
func adminPlugin(_ context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("plugin 需要动作：list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionList:
		return adminPluginList(rest, env)
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

// pluginCheckResult 是一项路径的校验结果；JSON 输出直接序列化它。
type pluginCheckResult struct {
	Name   string   `json:"name"`
	Path   string   `json:"path"`
	OK     bool     `json:"ok"`
	Hooks  []string `json:"hooks,omitempty"`
	Events []string `json:"events,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// adminPluginCheck 逐个装配给定路径并报告结果。
//
// 与 list 的分工：list 回答「当前配置加载了什么」，check 回答「这些文件能不能用」——
// 因此它接受路径参数，未给路径时才退回当前配置，写插件与 CI 校验都不必先设环境变量。
// 逐个加载而不是一次 Load 全部：一个坏文件不该遮住其余文件的结果。
func adminPluginCheck(args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin plugin check")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	paths := fs.Args()
	if len(paths) == 0 {
		paths = config.LoadPluginFiles()
	}
	if len(paths) == 0 {
		return env.usageError("plugin check 需要路径参数，或先设置 TOKENMP_PLUGIN_FILES")
	}

	results := make([]pluginCheckResult, 0, len(paths))
	failed := false
	for _, path := range paths {
		result := pluginCheckResult{Name: filepath.Base(path), Path: path}
		set, err := plugin.Load([]string{path}, plugin.Options{Logger: pluginListLogger(env.stderr)})
		if err != nil {
			failed = true
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		result.OK = true
		if infos := set.Stats(); len(infos) == 1 {
			result.Hooks = infos[0].Hooks
			result.Events = infos[0].Events
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

// adminPluginList 加载配置的中间件并输出清单与进程内统计。
func adminPluginList(args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin plugin list")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	set, err := plugin.Load(config.LoadPluginFiles(), plugin.Options{Logger: pluginListLogger(env.stderr)})
	if err != nil {
		return env.fail(err)
	}
	infos := set.Stats()
	rows := make([][]string, 0, len(infos))
	for _, info := range infos {
		rows = append(rows, []string{
			info.Name,
			info.Path,
			strings.Join(info.Hooks, ","),
			strings.Join(info.Events, ","),
			strconv.FormatInt(info.Calls, 10),
			strconv.FormatInt(info.Failures, 10),
			fmt.Sprintf("%.2f", info.AverageMS),
			info.LastError,
		})
	}
	return env.emit(*asJSON,
		[]string{flagName, "path", "hooks", "events", "calls", "failures", "average_ms", "last_error"},
		rows, infos)
}
