package main

import (
	"errors"
	"runtime/debug"
	"strings"
	"testing"
)

const (
	fullSHA  = "6593e21a1b2c3d4e5f60718293a4b5c6d7e8f901"
	shortSHA = "6593e21"
	testGo   = "go1.27.0"
)

// buildInfoWith 构造带指定 buildinfo settings 的 BuildInfo，用于覆盖 VCS 信息的两条来源。
func buildInfoWith(settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{Settings: settings}
}

func TestResolveBuildFacts(t *testing.T) {
	tests := []struct {
		name            string
		injectedVersion string
		injectedCommit  string
		injectedDate    string
		info            *debug.BuildInfo
		wantVersion     string
		wantCommit      string
		wantDate        string
		wantModified    bool
		wantVersionWhy  string
	}{
		{
			name:            "注入版本号时优先采用，忽略 buildinfo",
			injectedVersion: "v1.2.3",
			info:            buildInfoWith(debug.BuildSetting{Key: "vcs.revision", Value: fullSHA}),
			wantVersion:     "v1.2.3",
			wantCommit:      shortSHA,
			wantVersionWhy:  "注入值来自 tag，比提交哈希可信",
		},
		{
			name:            "未注入版本号时退化为提交短哈希",
			injectedVersion: "",
			info:            buildInfoWith(debug.BuildSetting{Key: "vcs.revision", Value: fullSHA}),
			wantVersion:     shortSHA,
			wantCommit:      shortSHA,
			wantVersionWhy:  "报一个能定位代码的值，好过报一个对不上任何产物的版本号",
		},
		{
			name:            "既无注入也无 VCS 信息时退化为 dev",
			injectedVersion: "",
			info:            nil,
			wantVersion:     unknownVersion,
			wantCommit:      "",
			wantVersionWhy:  "两者都取不到才退到字面量兜底",
		},
		{
			name:            "buildinfo 存在但无 vcs.revision 时仍退化为 dev",
			injectedVersion: "",
			info:            buildInfoWith(debug.BuildSetting{Key: "GOARCH", Value: "amd64"}),
			wantVersion:     unknownVersion,
			wantCommit:      "",
			wantVersionWhy:  "有 buildinfo 不等于有 VCS 信息，不能混为一谈",
		},
		{
			name:            "注入的 commit 优先于 buildinfo 的 vcs.revision",
			injectedVersion: "v1.0.0",
			injectedCommit:  fullSHA,
			info:            buildInfoWith(debug.BuildSetting{Key: "vcs.revision", Value: "0000000000000000000000000000000000000000"}),
			wantVersion:     "v1.0.0",
			wantCommit:      shortSHA,
			wantVersionWhy:  "GoReleaser 注入的 commit 与 tag 同源，比工具链推断的可靠",
		},
		{
			name:            "vcs.modified 为 true 时标记工作区已改动",
			injectedVersion: "v1.0.0",
			info: buildInfoWith(
				debug.BuildSetting{Key: "vcs.revision", Value: fullSHA},
				debug.BuildSetting{Key: "vcs.modified", Value: "true"},
			),
			wantVersion:  "v1.0.0",
			wantCommit:   shortSHA,
			wantModified: true,
		},
		{
			name:            "vcs.modified 为非 true 字面量时不标记改动",
			injectedVersion: "v1.0.0",
			info: buildInfoWith(
				debug.BuildSetting{Key: "vcs.revision", Value: fullSHA},
				debug.BuildSetting{Key: "vcs.modified", Value: "false"},
			),
			wantVersion:  "v1.0.0",
			wantCommit:   shortSHA,
			wantModified: false,
		},
		{
			name:            "短于 7 位的提交哈希原样保留，不越界截取",
			injectedVersion: "v1.0.0",
			info:            buildInfoWith(debug.BuildSetting{Key: "vcs.revision", Value: "abc"}),
			wantVersion:     "v1.0.0",
			wantCommit:      "abc",
		},
		{
			name:            "注入的构建日期原样透传",
			injectedVersion: "v1.0.0",
			injectedDate:    "2026-10-05T12:00:00Z",
			wantVersion:     "v1.0.0",
			wantDate:        "2026-10-05T12:00:00Z",
		},
		{
			name:            "未注入构建日期时保持为空",
			injectedVersion: "v1.0.0",
			wantVersion:     "v1.0.0",
			wantDate:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveBuildFacts(tt.injectedVersion, tt.injectedCommit, tt.injectedDate, tt.info, testGo)

			if got.Version != tt.wantVersion {
				t.Errorf("Version = %q，期望 %q（%s）", got.Version, tt.wantVersion, tt.wantVersionWhy)
			}
			if got.Commit != tt.wantCommit {
				t.Errorf("Commit = %q，期望 %q", got.Commit, tt.wantCommit)
			}
			if got.Date != tt.wantDate {
				t.Errorf("Date = %q，期望 %q", got.Date, tt.wantDate)
			}
			if got.Modified != tt.wantModified {
				t.Errorf("Modified = %v，期望 %v", got.Modified, tt.wantModified)
			}
			if got.Go != testGo {
				t.Errorf("Go = %q，期望原样透传 %q", got.Go, testGo)
			}
		})
	}
}

func TestVersionText(t *testing.T) {
	tests := []struct {
		name      string
		facts     buildFacts
		wantLines []string
	}{
		{
			name:  "有提交且工作区干净时为两行",
			facts: buildFacts{Version: "v1.2.3", Commit: shortSHA, Go: testGo},
			wantLines: []string{
				"tokenmp v1.2.3 (go go1.27.0)",
				"提交 6593e21",
			},
		},
		{
			name:  "工作区已改动时提交行带标记",
			facts: buildFacts{Version: "v1.2.3", Commit: shortSHA, Modified: true, Go: testGo},
			wantLines: []string{
				"tokenmp v1.2.3 (go go1.27.0)",
				"提交 6593e21（工作区已改动）",
			},
		},
		{
			name:  "无提交信息时整行省略，退成一行",
			facts: buildFacts{Version: unknownVersion, Go: testGo},
			wantLines: []string{
				"tokenmp dev (go go1.27.0)",
			},
		},
		{
			name: "注入构建日期时多一行",
			facts: buildFacts{
				Version: "v1.2.3",
				Commit:  shortSHA,
				Date:    "2026-10-05T12:00:00Z",
				Go:      testGo,
			},
			wantLines: []string{
				"tokenmp v1.2.3 (go go1.27.0)",
				"提交 6593e21",
				"构建 2026-10-05T12:00:00Z",
			},
		},
		{
			name:  "无提交信息但有日期时省略的是提交行而不是日期行",
			facts: buildFacts{Version: "v1.0.0", Date: "2026-10-05T12:00:00Z", Go: testGo},
			wantLines: []string{
				"tokenmp v1.0.0 (go go1.27.0)",
				"构建 2026-10-05T12:00:00Z",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 逐行比对而非整体比对：整体比对在行数不符时只能报出两坨文本，
			// 逐行能直接指出是哪一行多、少或不同。
			got := strings.Split(strings.TrimRight(versionText(tt.facts), "\n"), "\n")
			if len(got) != len(tt.wantLines) {
				t.Fatalf("输出 %d 行，期望 %d 行\n实际输出:\n%s", len(got), len(tt.wantLines), versionText(tt.facts))
			}
			for i := range tt.wantLines {
				if got[i] != tt.wantLines[i] {
					t.Errorf("第 %d 行 = %q，期望 %q", i+1, got[i], tt.wantLines[i])
				}
			}
		})
	}
}

// failingWriter 是一个永远写失败的 io.Writer，用于验证写失败时的退出码语义。
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// TestCmdVersionFailsWhenOutputWriteFails 守护「退出码 0 意味着版本信息确实交付了」。
// 若这里不报失败，`tokenmp version > file` 在磁盘写满时会静默留个空文件并报成功。
func TestCmdVersionFailsWhenOutputWriteFails(t *testing.T) {
	var stderr strings.Builder
	code := cmdVersion(failingWriter{err: errors.New("磁盘写满")}, &stderr)

	if code != exitFailure {
		t.Errorf("退出码 = %d，期望 %d", code, exitFailure)
	}
	if !strings.Contains(stderr.String(), "磁盘写满") {
		t.Errorf("stderr 应说明失败原因，实际为 %q", stderr.String())
	}
}
