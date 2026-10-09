package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
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
		{name: "结算缺动作", args: []string{"settlement"}, wantCode: exitUsage, wantErr: "settlement 需要动作"},
		{name: "结算未知动作", args: []string{"settlement", "frobnicate"}, wantCode: exitUsage, wantErr: "settlement 未知动作"},
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

// TestAdminQuotaListScopeFlags 覆盖 quota list 的维度过滤参数校验：
// --scope 与 --scope-id 必须成对，未知 scope 在连库前被拒绝。
func TestAdminQuotaListScopeFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{
			name:     "scope 缺 scope-id",
			args:     []string{"quota", actionList, "--scope", string(billing.ScopeAPIKey)},
			wantCode: exitUsage, wantErr: "--scope 与 --scope-id 必须成对给出",
		},
		{
			name:     "scope-id 缺 scope",
			args:     []string{"quota", actionList, "--scope-id", "7"},
			wantCode: exitUsage, wantErr: "--scope 与 --scope-id 必须成对给出",
		},
		{
			name:     "未知 scope",
			args:     []string{"quota", actionList, "--scope", "tenant", "--scope-id", "7"},
			wantCode: exitUsage, wantErr: "未知的规则范围",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := dispatchAdmin(context.Background(), tt.args, &adminEnv{stdout: &stdout, stderr: &stderr})
			if code != tt.wantCode {
				t.Errorf("退出码 = %d，期望 %d", code, tt.wantCode)
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr 未包含 %q，实际：%s", tt.wantErr, stderr.String())
			}
		})
	}
}

// TestAdminSettlementListWindowFlags 覆盖 settlement list 的账期参数校验：
// --from 与 --to 必须成对，且终点必须晚于起点，都在连库前判定。
func TestAdminSettlementListWindowFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{
			name:     "只给起点",
			args:     []string{"settlement", actionList, "--from", "2026-09-01"},
			wantCode: exitUsage, wantErr: "要么都给，要么都不给",
		},
		{
			name:     "只给终点",
			args:     []string{"settlement", actionList, "--to", "2026-10-01"},
			wantCode: exitUsage, wantErr: "要么都给，要么都不给",
		},
		{
			name:     "终点不晚于起点",
			args:     []string{"settlement", actionList, "--from", "2026-10-01", "--to", "2026-09-01"},
			wantCode: exitUsage, wantErr: "--to 必须晚于 --from",
		},
		{
			name:     "时间参数不可解析",
			args:     []string{"settlement", actionList, "--from", "2026/09/01"},
			wantCode: exitUsage, wantErr: "无法解析",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := dispatchAdmin(context.Background(), tt.args, &adminEnv{stdout: &stdout, stderr: &stderr})
			if code != tt.wantCode {
				t.Errorf("退出码 = %d，期望 %d", code, tt.wantCode)
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr 未包含 %q，实际：%s", tt.wantErr, stderr.String())
			}
		})
	}
}

// TestAdminRequestsUsageErrors 覆盖 requests 组的用法校验：缺参、未知动作、非法
// 过滤值与非法时刻都在连库前判定。用例都停在业务层之前，因此无需 service。
func TestAdminRequestsUsageErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "缺动作", args: []string{requestsName}, wantCode: exitUsage, wantErr: "requests 需要动作"},
		{name: "未知动作", args: []string{requestsName, "frobnicate"}, wantCode: exitUsage, wantErr: "requests 未知动作"},
		{name: "get 缺 request-id", args: []string{requestsName, actionGet}, wantCode: exitUsage, wantErr: "需要 --request-id"},
		{name: "list 非法终态", args: []string{requestsName, actionList, "--status", "nope"}, wantCode: exitUsage, wantErr: "--status 取值"},
		{name: "list 非法条数", args: []string{requestsName, actionList, "--limit", "0"}, wantCode: exitUsage, wantErr: "--limit 必须为正"},
		{name: "list 非法偏移", args: []string{requestsName, actionList, "--offset", "-1"}, wantCode: exitUsage, wantErr: "--offset 不能为负"},
		{name: "list 非法时刻", args: []string{requestsName, actionList, "--since", "2026/01/01"}, wantCode: exitUsage, wantErr: "无法解析"},
		{name: "stats 非法分组", args: []string{requestsName, actionStats, "--group-by", "week"}, wantCode: exitUsage, wantErr: "--group-by 取值"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := dispatchAdmin(context.Background(), tt.args, &adminEnv{stdout: &stdout, stderr: &stderr})
			if code != tt.wantCode {
				t.Errorf("退出码 = %d，期望 %d", code, tt.wantCode)
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr 未包含 %q，实际：%s", tt.wantErr, stderr.String())
			}
		})
	}
}

// TestFormatRequestHelpers 覆盖请求详情与列表用到的可空值 / JSON 格式化。
func TestFormatRequestHelpers(t *testing.T) {
	if got := formatOptionalInt(nil); got != "-" {
		t.Errorf("nil 整数应为占位符，得到 %q", got)
	}
	status := 429
	if got := formatOptionalInt(&status); got != "429" {
		t.Errorf("整数格式不符：%q", got)
	}
	if got := formatOptionalString(nil); got != "-" {
		t.Errorf("nil 字符串应为占位符，得到 %q", got)
	}
	empty := ""
	if got := formatOptionalString(&empty); got != "-" {
		t.Errorf("空串应为占位符，得到 %q", got)
	}
	if got := formatJSONBlob(nil); got != "-" {
		t.Errorf("无报文应为占位符，得到 %q", got)
	}
	if got := formatRewrittenParts(nil); got != "-" {
		t.Errorf("无改写标注应为占位符，得到 %q", got)
	}
	if got := formatRewrittenParts([]string{"request_model"}); got != "request_model" {
		t.Errorf("改写标注格式不符：%q", got)
	}
}

// TestWriteRequestDetailOutput 断言 get 的人类可读输出包含摘要、尝试时间线与脱敏报文。
func TestWriteRequestDetailOutput(t *testing.T) {
	var stdout, stderr strings.Builder
	env := &adminEnv{stdout: &stdout, stderr: &stderr}
	upstreamStatus := 429
	failureClass := "rate_limit"
	view := &admin.RequestDetailView{
		Request: admin.RequestView{
			ID: 5, RequestID: "req-7f3a", AccountID: 1, APIKeyID: 9,
			CreatedAt: time.Date(2026, 10, 5, 12, 30, 0, 0, time.UTC),
			Status:    "failed", HTTPStatus: 502, UpstreamStatus: &upstreamStatus,
			FailureClass: &failureClass, DurationMS: 85, RequestedModel: "claude-sonnet",
			UpstreamModel: "claude-3-5-sonnet", Protocol: "anthropic_messages",
			UpstreamProtocol: "anthropic_messages", Stream: true, PayloadAvailable: true,
		},
		Attempts: []admin.RequestAttemptView{{
			Attempt: 1, Outcome: "failed", UpstreamStatus: &upstreamStatus,
			FailureClass: &failureClass, DurationMS: 85,
		}},
		RequestShape:   json.RawMessage(`{"model":"claude-sonnet"}`),
		RewrittenParts: []string{"request_model"},
		ClientIP:       "203.0.113.7",
		UserAgent:      "client/1.0",
	}
	if code := writeRequestDetail(env, view); code != exitOK {
		t.Fatalf("退出码 = %d，期望 %d", code, exitOK)
	}
	out := stdout.String()
	for _, want := range []string{
		"请求 req-7f3a", "account_id", "upstream_status", "client_ip", "203.0.113.7",
		"尝试时间线（1 条）", "#1", "rate_limit", "请求报文", `{"model":"claude-sonnet"}`,
		"改写标注", "request_model",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q：\n%s", want, out)
		}
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
