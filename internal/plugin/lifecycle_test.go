package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
)

// newLoggingSet 加载一个中间件并把日志收进缓冲区。
func newLoggingSet(t *testing.T, dir, source string, opts Options) (*Set, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	path := writeSource(t, dir, "lifecycle.mw.js", source)
	set, err := Load([]string{path}, opts)
	if err != nil {
		t.Fatalf("加载插件失败：%v", err)
	}
	return set, &logs
}

// runOnce 跑一次请求改写并返回插件写入的标记取值。
func runOnce(t *testing.T, set *Set) string {
	t.Helper()
	req := chatRequest(chatBody)
	if err := runRequest(t, set, context.Background(), req); err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	return rawField(t, req, "v")
}

const lifecycleV1 = `export function onRequest(body) { body.v = "v1"; return body; }`
const lifecycleV2 = `export function onRequest(body) { body.v = "version-two"; return body; }`
const lifecycleBroken = `export function onRequest(body) { this is not javascript`

// TestReloadFailureBacksOffAndKeepsServing 守护重编译失败的退避与兜底。
//
// 写坏的文件会被一连串请求反复撞上：逐请求重试等于把编译开销平摊到每次转发上，
// 还会让同一句失败告警刷满日志。退避窗口内只尝试一次，旧产物照常服务。
func TestReloadFailureBacksOffAndKeepsServing(t *testing.T) {
	dir := t.TempDir()
	// 窗口取足够长，保证用例内不会过期。
	set, logs := newLoggingSet(t, dir, lifecycleV1, Options{ReloadBackoff: time.Minute})
	if got := runOnce(t, set); got != "v1" {
		t.Fatalf("首版插件未生效，得到 %q", got)
	}

	writeSource(t, dir, "lifecycle.mw.js", lifecycleBroken)
	for i := 0; i < 50; i++ {
		if got := runOnce(t, set); got != "v1" {
			t.Fatalf("第 %d 次请求应继续用旧产物，得到 %q", i, got)
		}
	}

	if n := strings.Count(logs.String(), "中间件热重载失败"); n != 1 {
		t.Fatalf("退避窗口内应当只尝试一次，实际 %d 次", n)
	}
	infos := set.Stats()
	if len(infos) != 1 {
		t.Fatalf("统计项数 = %d，期望 1", len(infos))
	}
	if infos[0].ReloadFailures != 1 {
		t.Fatalf("连续失败次数 = %d，期望 1", infos[0].ReloadFailures)
	}
	if infos[0].ReloadError == "" {
		t.Fatal("应当能读到最近一次失败原因")
	}
	if !infos[0].Stale {
		t.Fatal("文件指纹已变化时应当可读地标记为在跑旧版本")
	}
}

// TestReloadRecoversAfterBackoff 守护退避窗口过后重新编译并换成新产物。
//
// 退避不能变成「永久放弃」：文件修好后必须能自动回到新版本。
func TestReloadRecoversAfterBackoff(t *testing.T) {
	dir := t.TempDir()
	set, _ := newLoggingSet(t, dir, lifecycleV1, Options{ReloadBackoff: 20 * time.Millisecond})

	writeSource(t, dir, "lifecycle.mw.js", lifecycleBroken)
	if got := runOnce(t, set); got != "v1" {
		t.Fatalf("编译失败时应继续用旧产物，得到 %q", got)
	}

	writeSource(t, dir, "lifecycle.mw.js", lifecycleV2)
	time.Sleep(60 * time.Millisecond)
	if got := runOnce(t, set); got != "version-two" {
		t.Fatalf("退避窗口过后应当换成新产物，得到 %q", got)
	}

	infos := set.Stats()
	if infos[0].ReloadFailures != 0 || infos[0].ReloadError != "" || infos[0].Stale {
		t.Fatalf("成功重载后失败状态应清空，实际 %+v", infos[0])
	}
	if infos[0].ReloadedAt == "" {
		t.Fatal("成功重载后应当能读到最近一次重载时刻")
	}
}

// TestStatsDoesNotTriggerReload 守护状态读取没有副作用。
//
// 读清单原先经 current() 走热重载路径：文件一变，一次清单查询就会顺带触发重编译。
// 状态读取只应回答此刻在跑什么。
func TestStatsDoesNotTriggerReload(t *testing.T) {
	dir := t.TempDir()
	set, logs := newLoggingSet(t, dir, lifecycleV1, Options{ReloadBackoff: time.Minute})
	if got := runOnce(t, set); got != "v1" {
		t.Fatalf("首版插件未生效，得到 %q", got)
	}

	writeSource(t, dir, "lifecycle.mw.js", lifecycleV2)
	infos := set.Stats()
	if len(infos) != 1 {
		t.Fatalf("统计项数 = %d，期望 1", len(infos))
	}
	if !infos[0].Stale {
		t.Fatal("尚未重编译时应标记为在跑旧版本")
	}
	if logged := logs.String(); strings.Contains(logged, "热重载") {
		t.Fatalf("状态读取不应触发重编译，实际日志：%s", logged)
	}
	// hooks 取自当前在跑的产物，仍应是旧版本的导出。
	if len(infos[0].Hooks) != 1 || infos[0].Hooks[0] != hookOnRequest {
		t.Fatalf("状态应描述当前产物，实际 hooks=%v", infos[0].Hooks)
	}
}

// TestRuntimeImportDependencyTriggersReload 守护动态 import() 的文件参与热重载指纹。
//
// 这类文件不在启动期的静态依赖图里：不补登记，改它既不会触发换代，也不会清掉池里
// 已经装载旧模块的运行时，插件会一直停在旧版本。
func TestRuntimeImportDependencyTriggersReload(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "value.js", `export const value = "one";`)
	entry := writeSource(t, dir, "dyn.mw.js", `
const { value } = await import("./value.js");
export function onRequest(body) { body.v = value; return body; }
`)
	set, err := Load([]string{entry}, Options{ReloadBackoff: time.Minute})
	if err != nil {
		t.Fatalf("加载插件失败：%v", err)
	}
	if got := runOnce(t, set); got != "one" {
		t.Fatalf("首版依赖未生效，得到 %q", got)
	}

	writeSource(t, dir, "value.js", `export const value = "second";`)
	if got := runOnce(t, set); got != "second" {
		t.Fatalf("动态导入的文件变化应触发重载，得到 %q", got)
	}
}

// TestForcedReloadIgnoresFingerprint 守护强制重载是不依赖指纹的兜底。
//
// 指纹是「修改时间 + 大小」：等长内容加复原的时间戳就落在它看不见的区间里。
// 强制重载必须连编译缓存一起丢，否则缓存会把旧模块还回来。
func TestForcedReloadIgnoresFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := writeSource(t, dir, "forced.mw.js", `export function onRequest(body) { body.v = "v1"; return body; }`)
	set, err := Load([]string{path}, Options{ReloadBackoff: time.Minute})
	if err != nil {
		t.Fatalf("加载插件失败：%v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取文件状态失败：%v", err)
	}
	if got := runOnce(t, set); got != "v1" {
		t.Fatalf("首版插件未生效，得到 %q", got)
	}

	// 等长内容 + 复原修改时间：指纹看不出变化。
	writeSource(t, dir, "forced.mw.js", `export function onRequest(body) { body.v = "v2"; return body; }`)
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("复原文件时间失败：%v", err)
	}
	if got := runOnce(t, set); got != "v1" {
		t.Fatalf("指纹相同的改动不应被惰性重载发现，得到 %q", got)
	}

	set.Reload()
	if got := runOnce(t, set); got != "v2" {
		t.Fatalf("强制重载应换成新产物，得到 %q", got)
	}
}

// TestSkipIsCountedAndThrottled 守护「转发放行但插件未介入」这件事可被看见。
//
// 取用失败（运行时建不出来）在正常插件上难以复现，这里直接驱动记录路径：
// 要断言的是计数与限频，不是复现引擎级故障。
func TestSkipIsCountedAndThrottled(t *testing.T) {
	set, logs := newLoggingSet(t, t.TempDir(), lifecycleV1, Options{})
	if len(set.items) != 1 {
		t.Fatalf("中间件数 = %d，期望 1", len(set.items))
	}
	middleware := set.items[0]

	const calls = 40
	for i := 0; i < calls; i++ {
		middleware.noteSkip(errors.New("运行时建不出来"))
	}

	infos := set.Stats()
	if infos[0].Skipped != calls {
		t.Fatalf("跳过计数 = %d，期望 %d", infos[0].Skipped, calls)
	}
	if infos[0].SkipError == "" {
		t.Fatal("应当能读到最近一次取用失败的原因")
	}
	recorded := strings.Count(logs.String(), "中间件取用失败")
	if recorded == 0 || recorded >= calls {
		t.Fatalf("%d 次取用失败产生了 %d 条日志，未限频", calls, recorded)
	}

	time.Sleep(1100 * time.Millisecond)
	middleware.noteSkip(errors.New("运行时建不出来"))
	if !strings.Contains(logs.String(), "suppressed=") {
		t.Fatalf("限频后应报出被压掉的条数，实际：%s", logs.String())
	}
}

// TestOnReloadFiresOnSuccessfulReload 守护产物换代通知只在成功换代时触发。
//
// 装配方靠它把「换代之后才成立」的检查再跑一遍；编译失败时产物没变，不该触发。
func TestOnReloadFiresOnSuccessfulReload(t *testing.T) {
	dir := t.TempDir()
	set, _ := newLoggingSet(t, dir, lifecycleV1, Options{ReloadBackoff: time.Minute})
	fired := 0
	set.OnReload(func() { fired++ })

	writeSource(t, dir, "lifecycle.mw.js", lifecycleBroken)
	if got := runOnce(t, set); got != "v1" {
		t.Fatalf("编译失败时应继续用旧产物，得到 %q", got)
	}
	if fired != 0 {
		t.Fatalf("编译失败不应触发换代通知，实际 %d 次", fired)
	}

	writeSource(t, dir, "lifecycle.mw.js", lifecycleV2)
	set.Reload()
	if fired != 1 {
		t.Fatalf("成功换代应触发一次通知，实际 %d 次", fired)
	}
}

// TestConcurrentRequestsDuringReload 守护并发取用与重编译之间没有数据竞争。
//
// 单飞闸改变了两把锁的嵌套关系（编译不再持锁），并发路径必须仍然只产出可用产物。
// 断言交给 -race 与「每个请求都拿到旧产物」两条。
func TestConcurrentRequestsDuringReload(t *testing.T) {
	dir := t.TempDir()
	set, _ := newLoggingSet(t, dir, lifecycleV1, Options{ReloadBackoff: time.Minute})
	writeSource(t, dir, "lifecycle.mw.js", lifecycleBroken)

	const workers = 16
	const perWorker = 20
	failures := make(chan string, workers*perWorker)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				req := chatRequest(chatBody)
				pluginCtx, release := set.BeginRequest(context.Background(), req)
				err := set.OnRequest(pluginCtx, openaichat.New(), req)
				release()
				if err != nil {
					failures <- err.Error()
					continue
				}
				var fields map[string]any
				if jsonErr := json.Unmarshal(req.RawBody, &fields); jsonErr != nil {
					failures <- jsonErr.Error()
					continue
				}
				if fields["v"] != "v1" {
					failures <- "重编译失败时应继续用旧产物"
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Fatalf("并发取用不合格：%s", msg)
	}
}
