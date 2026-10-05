package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
)

// 构建期注入的变量，由 Makefile 的 build-binary 目标通过
// `-ldflags "-X main.version=..."` 写入；GoReleaser 另行注入 commit 与 date。
//
// 三者都保持「空即未知」的语义，不做零值假设：未注入时由二进制内嵌的 VCS 信息兜底，
// 而不是报出一个假的版本号。
//
// 三个都必须被真正读到：`-X` 作用在不存在的符号上是**静默失效**的 ——
// 构建照常成功、退出码 0、没有任何告警，只是注入值不生效。声明一个没人读的变量，
// 等于给这条静默失效留了个入口。因此这里宁愿把 date 也输出出来，也不留空声明。
//
// 注意 -X 只对包级变量生效，命中常量时同样静默失效，所以这里不能用 const。
var (
	version = ""
	commit  = ""
	date    = ""
)

// unknownVersion 是版本号彻底取不到时的兜底字面量。
const unknownVersion = "dev"

// shortCommitLen 是提交哈希的短形态长度。7 位是 git 默认的 abbrev 长度，
// 足以在人类可读与唯一性之间取平衡。
const shortCommitLen = 7

// buildFacts 是一次版本查询得到的全部事实。
//
// 拆成结构体而不是直接打印，是为了让取值逻辑（含各级兜底）能被单独测试 ——
// 这部分逻辑的正确性无法靠肉眼检查输出看出来。
type buildFacts struct {
	Version  string // 版本号：注入值 → 提交短哈希 → dev
	Commit   string // 完整提交哈希；空表示二进制里没有 VCS 信息
	Modified bool   // 构建时工作区是否有未提交改动
	Date     string // 构建日期；空表示未注入（本机构建即为空）
	Go       string // 构建该二进制所用的 Go 版本
}

// resolveBuildFacts 按注入值、VCS 信息、运行时信息组装版本事实。
//
// 版本号的三级兜底顺序是有意的：注入值来自 tag，最可信；拿不到时退到提交哈希，
// 至少能定位到代码；两者都没有才退到 dev。提交行与版本号分开取值，
// 因为「没注入版本号」不等于「没有提交信息」—— 两者来源不同。
func resolveBuildFacts(injectedVersion, injectedCommit, injectedDate string, info *debug.BuildInfo, goVersion string) buildFacts {
	facts := buildFacts{Go: goVersion, Date: injectedDate}

	// VCS 信息来自 GoReleaser 注入的 commit，或 Go 工具链内嵌的 buildinfo。
	// 两者都取不到时保持为空，调用方据此省略提交行。
	facts.Commit = injectedCommit
	var vcsModified bool
	if info != nil {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if facts.Commit == "" {
					facts.Commit = s.Value
				}
			case "vcs.modified":
				vcsModified = s.Value == "true"
			}
		}
	}
	facts.Modified = vcsModified

	switch {
	case injectedVersion != "":
		facts.Version = injectedVersion
	case facts.Commit != "":
		// 没注入版本号时用提交短哈希兜底：报出一个能定位代码的值，
		// 好过报出一个与任何产物都对不上的版本号。
		facts.Version = truncateCommit(facts.Commit)
	default:
		facts.Version = unknownVersion
	}

	facts.Commit = truncateCommit(facts.Commit)

	return facts
}

// truncateCommit 把提交哈希截成短形态。
// 完整 40 位在人类阅读场景里没有额外信息量，需要完整值时可以从仓库查。
func truncateCommit(sha string) string {
	if len(sha) > shortCommitLen {
		return sha[:shortCommitLen]
	}
	return sha
}

// readBuildInfo 读取当前二进制的内嵌构建信息。
// 包一层是为了让 resolveBuildFacts 保持纯函数、可在测试里喂入构造好的输入。
func readBuildInfo() *debug.BuildInfo {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return info
}

// versionText 把版本事实写成人类可读的若干行。
//
// 提交行与构建日期行在对应事实缺失时**整行省略**，而不打印「提交 未知」——
// 后者会让人以为查询失败，实际是这个二进制本就不带该信息
// （例如构建时未拷 .git，或本机构建没注入 date）。行数变化本身即是信号。
func versionText(facts buildFacts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s (go %s)\n", programName, facts.Version, facts.Go)

	switch {
	case facts.Commit == "":
		// 没有 VCS 信息，整行省略。
	case facts.Modified:
		fmt.Fprintf(&b, "提交 %s（工作区已改动）\n", facts.Commit)
	default:
		fmt.Fprintf(&b, "提交 %s\n", facts.Commit)
	}

	if facts.Date != "" {
		fmt.Fprintf(&b, "构建 %s\n", facts.Date)
	}

	return b.String()
}

// cmdVersion 实现 `tokenmp version`。
//
// 输出写失败时报错并非零退出：退出码 0 的含义是「版本信息已经交到调用方手上」，
// 写不出去就不该报成功 —— 否则 `tokenmp version > file` 在磁盘写满时会静默留个空文件。
func cmdVersion(stdout, stderr io.Writer) int {
	facts := resolveBuildFacts(version, commit, date, readBuildInfo(), runtime.Version())
	if _, err := io.WriteString(stdout, versionText(facts)); err != nil {
		// 向 stderr 报告 stderr 自身写失败没有补救手段，故显式丢弃返回值。
		// 写成 `_, _ =` 而不是省略，是为了让 errcheck 放行并让读者看到这是有意的。
		_, _ = fmt.Fprintf(stderr, "写出版本信息失败：%v\n", err)
		return exitFailure
	}
	return exitOK
}
