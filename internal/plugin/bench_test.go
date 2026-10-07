package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
)

// benchBody 是最小 OpenAI Chat 请求体。
const benchBody = `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`

// writeBenchPlugin 在 dir 里写一个中间件文件。
func writeBenchPlugin(b *testing.B, dir, name, source string) string {
	b.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		b.Fatalf("写文件 %s 失败：%v", path, err)
	}
	return path
}

// benchAcquire 量一次取用：BeginRequest 到 release。
func benchAcquire(b *testing.B, set *Set) {
	b.Helper()
	req := &domain.Request{
		RequestID: "bench",
		Protocol:  domain.ProtocolOpenAIChat,
		Model:     "alias",
		RawBody:   []byte(benchBody),
	}
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, release := set.BeginRequest(ctx, req)
		release()
	}
}

// BenchmarkAcquireSingleFile 记录单文件中间件每次取用的开销。
//
// 取用路径每次都核对依赖文件的指纹（statFile 取「修改时间 + 大小」），这是热重载的代价：
// 单文件插件每次取用一次 stat。把开销记下来，供将来判断是否值得给指纹检查加间隔时对照 ——
// 那种节流会把重载延迟从「下一个请求」变成「最多 N 秒后」，不该凭印象决定。
func BenchmarkAcquireSingleFile(b *testing.B) {
	set, err := Load([]string{writeBenchPlugin(b, b.TempDir(), "single.mw.js",
		`export function onRequest(body) { return body; }`)}, Options{})
	if err != nil {
		b.Fatalf("加载插件失败：%v", err)
	}
	benchAcquire(b, set)
}

// BenchmarkAcquireManyFiles 记录入口加若干静态导入时每次取用的开销。
//
// 与单文件版的差值即「每个依赖文件一次 stat」的单价：模块数越多，取用成本越高，
// 且这段成本在中间件的锁内完成。
func BenchmarkAcquireManyFiles(b *testing.B) {
	dir := b.TempDir()
	const modules = 8
	var entry strings.Builder
	for i := 0; i < modules; i++ {
		name := fmt.Sprintf("dep%d.js", i)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(fmt.Sprintf("export const v%d = %d;\n", i, i)), 0o600); err != nil {
			b.Fatalf("写文件 %s 失败：%v", path, err)
		}
		fmt.Fprintf(&entry, "import { v%d } from \"./%s\";\n", i, name)
	}
	entry.WriteString("export function onRequest(body) { return body; }\n")

	set, err := Load([]string{writeBenchPlugin(b, dir, "many.mw.js", entry.String())}, Options{})
	if err != nil {
		b.Fatalf("加载插件失败：%v", err)
	}
	benchAcquire(b, set)
}
