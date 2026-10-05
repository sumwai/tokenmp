package main

import (
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
