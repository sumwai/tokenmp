package main

import (
	"strings"
	"testing"
	"time"
)

// 本文件覆盖管理面命令层的输出格式与退出码，不连数据库：
// 用法错误在连库前就已判定，输出格式化是纯函数。

func TestWriteTableAlignsColumns(t *testing.T) {
	var b strings.Builder
	err := writeTable(&b, []string{"id", "name"}, [][]string{
		{"1", "ab"},
		{"22", "c"},
	})
	if err != nil {
		t.Fatalf("写表格失败：%v", err)
	}
	want := "id  name\n" +
		"1   ab\n" +
		"22  c\n"
	if b.String() != want {
		t.Errorf("表格输出不符：\n实际 %q\n期望 %q", b.String(), want)
	}
}

func TestWriteTableEmptyRowsKeepsHeader(t *testing.T) {
	var b strings.Builder
	if err := writeTable(&b, []string{"id"}, nil); err != nil {
		t.Fatalf("写表格失败：%v", err)
	}
	if b.String() != "id\n" {
		t.Errorf("空结果应只打印表头，得到 %q", b.String())
	}
}

func TestParseAdminTime(t *testing.T) {
	if got, err := parseAdminTime(""); err != nil || !got.IsZero() {
		t.Errorf("空串应返回零值时间，得到 %v（err=%v）", got, err)
	}
	if got, err := parseAdminTime("2026-10-05"); err != nil || got.Format("2006-01-02") != "2026-10-05" {
		t.Errorf("日期格式解析失败：%v（err=%v）", got, err)
	}
	if _, err := parseAdminTime("2026/10/05"); err == nil {
		t.Error("非法时间应当报错")
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := formatTimePtr(nil); got != "-" {
		t.Errorf("nil 时间应为占位符，得到 %q", got)
	}
	at := time.Date(2026, 10, 5, 12, 30, 0, 0, time.UTC)
	if got := formatTimePtr(&at); got != "2026-10-05 12:30:00" {
		t.Errorf("时间格式不符：%q", got)
	}
	if got := formatUintPtr(nil); got != "-" {
		t.Errorf("nil 整数应为占位符，得到 %q", got)
	}
	if got := formatBool(true); got != "是" {
		t.Errorf("true 应渲染为「是」，得到 %q", got)
	}
}

// TestAdminUsageExitCodes 守护用法错误的退出码口径：缺参、未知组、未知动作都
// 应以用法码 2 退出，且不需要数据库配置。
func TestAdminUsageExitCodes(t *testing.T) {
	t.Setenv("TOKENMP_MYSQL_DSN", "")
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "缺组名", args: nil, wantCode: exitUsage, wantErr: "缺少管理组名"},
		{name: "未知组", args: []string{"frobnicate"}, wantCode: exitUsage, wantErr: "未知管理组"},
		{name: "缺动作", args: []string{"merchant"}, wantCode: exitUsage, wantErr: "merchant 需要动作"},
		{name: "未知动作", args: []string{"merchant", "frobnicate"}, wantCode: exitUsage, wantErr: "merchant 未知动作"},
		{name: "限额缺动作", args: []string{"quota"}, wantCode: exitUsage, wantErr: "quota 需要动作"},
		{name: "限额未知动作", args: []string{"quota", "frobnicate"}, wantCode: exitUsage, wantErr: "quota 未知动作"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			got := cmdAdmin(tt.args, strings.NewReader(""), &stdout, &stderr)
			if got != tt.wantCode {
				t.Errorf("退出码 = %d，期望 %d", got, tt.wantCode)
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr 未包含 %q，实际：%s", tt.wantErr, stderr.String())
			}
		})
	}
}

func TestAdminHelpWithoutDatabase(t *testing.T) {
	t.Setenv("TOKENMP_MYSQL_DSN", "")
	var stdout, stderr strings.Builder
	if code := cmdAdmin([]string{"help"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("退出码 = %d，期望 %d", code, exitOK)
	}
	if !strings.Contains(stdout.String(), "组与动作") {
		t.Errorf("帮助未打到 stdout：%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("帮助不应写 stderr：%s", stderr.String())
	}
}

func TestAdminRuntimeFailureWithoutDSN(t *testing.T) {
	t.Setenv("TOKENMP_MYSQL_DSN", "")
	var stdout, stderr strings.Builder
	if code := cmdAdmin([]string{"merchant", "list"}, strings.NewReader(""), &stdout, &stderr); code != exitFailure {
		t.Fatalf("退出码 = %d，期望 %d", code, exitFailure)
	}
	if !strings.Contains(stderr.String(), "配置") {
		t.Errorf("stderr 应报配置错误：%s", stderr.String())
	}
}

func TestRunDispatchesAdmin(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := runWithStdin([]string{"admin", "help"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("退出码 = %d，期望 %d", code, exitOK)
	}
	if !strings.Contains(stdout.String(), "admin <组> <动作>") {
		t.Errorf("admin 帮助未输出：%s", stdout.String())
	}
}
