package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
)

// 本文件是 `admin requests` 的命令层：请求记录的查询、单条详情与计数聚合。
//
// 命令层只做参数解析与输出；过滤与聚合口径在 internal/admin，取事实在 internal/store。
// 请求记录不带账户作用域：管理面是全平台视图，--account 只是一个可选过滤项。

// defaultRequestPageSize 是 requests list 未给 --limit 时的每页条数。
const defaultRequestPageSize = 20

// defaultRequestStatsGroup 是 requests stats 未给 --group-by 时的分组维度。
const defaultRequestStatsGroup = "day"

// adminRequestStatuses 是 list --status 的取值集合，与契约和业务层一致。
var adminRequestStatuses = map[string]struct{}{
	"success":   {},
	"failed":    {},
	"cancelled": {},
}

// adminRequestStatsGroups 是 stats --group-by 的取值集合。
var adminRequestStatsGroups = map[string]struct{}{
	"day":     {},
	"model":   {},
	"status":  {},
	"account": {},
}

func adminRequests(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("requests 需要动作：list / get / stats")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionList:
		return adminRequestsList(ctx, rest, env)
	case actionGet:
		return adminRequestsGet(ctx, rest, env)
	case actionStats:
		return adminRequestsStats(ctx, rest, env)
	default:
		return env.usageErrorf("requests 未知动作 %q", action)
	}
}

func adminRequestsList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin requests list")
	account := fs.Uint64(flagAccount, 0, "账户 id；不填列出全部账户")
	since := fs.String(flagSince, "", "起始时刻（含）；不填不限")
	until := fs.String(flagUntil, "", "结束时刻（含）；不填不限")
	model := fs.String(flagModel, "", "按客户端请求的模型名精确匹配；不填不过滤")
	requestID := fs.String(flagRequestID, "", "按请求标识精确匹配；不填不过滤")
	status := fs.String(flagStatus, "", "按终态过滤：success | failed | cancelled；不填不过滤")
	apiKey := fs.Uint64(flagAPIKey, 0, "按签发本次调用的密钥 id 过滤；不填不过滤")
	limit := fs.Int(flagLimit, defaultRequestPageSize, "每页条数")
	offset := fs.Int(flagOffset, 0, "偏移条数")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	sinceAt, untilAt, ok := parseRequestWindow(*since, *until, env)
	if !ok {
		return exitUsage
	}
	if *limit <= 0 {
		return env.usageError("requests list 的 --limit 必须为正")
	}
	if *offset < 0 {
		return env.usageError("requests list 的 --offset 不能为负")
	}
	if _, ok := statusValue(*status); !ok {
		return env.usageError("requests list 的 --status 取值必须是 success / failed / cancelled")
	}
	views, _, err := env.service.ListRequests(ctx, admin.RequestListQuery{
		AccountID:      *account,
		Since:          sinceAt,
		Until:          untilAt,
		RequestedModel: *model,
		RequestID:      *requestID,
		Status:         *status,
		APIKeyID:       *apiKey,
		Limit:          *limit,
		Offset:         *offset,
	})
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(views))
	for _, v := range views {
		rows = append(rows, []string{
			strconv.FormatUint(v.ID, 10), v.RequestID, strconv.FormatUint(v.AccountID, 10),
			strconv.FormatUint(v.APIKeyID, 10), formatTime(v.CreatedAt), v.Status,
			strconv.Itoa(v.HTTPStatus), formatOptionalInt(v.UpstreamStatus),
			formatOptionalString(v.FailureClass), formatOptionalString(v.ErrorCode),
			strconv.FormatInt(v.DurationMS, 10), v.RequestedModel, v.UpstreamModel,
			v.Protocol, v.UpstreamProtocol, formatBool(v.CrossProtocol), formatBool(v.Stream),
			strconv.FormatInt(v.WrittenBytes, 10), formatBool(v.PayloadAvailable),
		})
	}
	return env.emit(*asJSON,
		[]string{
			flagID, "request_id", "account_id", "api_key_id", headerCreatedAt, headerStatus,
			"http_status", "upstream_status", "failure_class", "error_code", "duration_ms",
			flagModel, headerUpstreamModel, "protocol", "upstream_protocol", "cross_protocol",
			"stream", "written_bytes", "payload_available",
		},
		rows, views)
}

func adminRequestsGet(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin requests get")
	requestID := fs.String(flagRequestID, "", "请求标识")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*requestID) == "" {
		return env.usageError("requests get 需要 --request-id")
	}
	view, err := env.service.RequestDetail(ctx, *requestID)
	if err != nil {
		return env.fail(err)
	}
	if *asJSON {
		return env.emit(true, nil, nil, view)
	}
	return writeRequestDetail(env, view)
}

func adminRequestsStats(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin requests stats")
	account := fs.Uint64(flagAccount, 0, "账户 id；不填聚合全部账户")
	since := fs.String(flagSince, "", "起始时刻（含）；不填不限")
	until := fs.String(flagUntil, "", "结束时刻（含）；不填不限")
	model := fs.String(flagModel, "", "按客户端请求的模型名精确匹配；不填不过滤")
	apiKey := fs.Uint64(flagAPIKey, 0, "按签发本次调用的密钥 id 过滤；不填不过滤")
	groupBy := fs.String(flagGroupBy, defaultRequestStatsGroup, "分组维度：day | model | status | account")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	sinceAt, untilAt, ok := parseRequestWindow(*since, *until, env)
	if !ok {
		return exitUsage
	}
	if _, ok := adminRequestStatsGroups[*groupBy]; !ok {
		return env.usageError("requests stats 的 --group-by 取值必须是 day / model / status / account")
	}
	views, err := env.service.RequestStats(ctx, admin.RequestStatsQuery{
		AccountID:      *account,
		Since:          sinceAt,
		Until:          untilAt,
		RequestedModel: *model,
		APIKeyID:       *apiKey,
		GroupBy:        *groupBy,
	})
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(views))
	for _, v := range views {
		rows = append(rows, []string{
			v.Key, strconv.FormatInt(v.Total, 10), strconv.FormatInt(v.Success, 10),
			strconv.FormatInt(v.Failed, 10), strconv.FormatInt(v.Cancelled, 10),
		})
	}
	return env.emit(*asJSON, []string{keyName, "total", "success", "failed", "cancelled"}, rows, views)
}

// parseRequestWindow 解析请求记录的 --since / --until；任一不可解析即报用法错误。
func parseRequestWindow(since, until string, env *adminEnv) (time.Time, time.Time, bool) {
	parsedSince, err := parseAdminTime(since)
	if err != nil {
		env.usageError(err.Error())
		return time.Time{}, time.Time{}, false
	}
	parsedUntil, err := parseAdminTime(until)
	if err != nil {
		env.usageError(err.Error())
		return time.Time{}, time.Time{}, false
	}
	return parsedSince, parsedUntil, true
}

// statusValue 归一化并校验 --status；空串表示不过滤。
func statusValue(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if _, ok := adminRequestStatuses[raw]; !ok {
		return "", false
	}
	return raw, true
}

// writeRequestDetail 以人类可读的分段文本输出一条请求记录与尝试时间线。
//
// 非 JSON 分支不走 emit 的表格：详情含嵌套的尝试时间线与多段 JSON 报文，
// 单张表表达不了层次。
func writeRequestDetail(env *adminEnv, view *admin.RequestDetailView) int {
	r := view.Request
	if code := env.printf("请求 %s\n", r.RequestID); code != exitOK {
		return code
	}
	fields := [][2]string{
		{"account_id", strconv.FormatUint(r.AccountID, 10)},
		{"api_key_id", strconv.FormatUint(r.APIKeyID, 10)},
		{headerCreatedAt, formatTime(r.CreatedAt)},
		{headerStatus, r.Status},
		{"http_status", strconv.Itoa(r.HTTPStatus)},
		{"upstream_status", formatOptionalInt(r.UpstreamStatus)},
		{"failure_class", formatOptionalString(r.FailureClass)},
		{"error_code", formatOptionalString(r.ErrorCode)},
		{"duration_ms", strconv.FormatInt(r.DurationMS, 10)},
		{flagModel, r.RequestedModel},
		{headerUpstreamModel, r.UpstreamModel},
		{"protocol", r.Protocol},
		{"upstream_protocol", r.UpstreamProtocol},
		{"stream", formatBool(r.Stream)},
		{"written_bytes", strconv.FormatInt(r.WrittenBytes, 10)},
		{"payload_available", formatBool(r.PayloadAvailable)},
		{"client_ip", view.ClientIP},
		{"user_agent", view.UserAgent},
	}
	for _, field := range fields {
		if code := env.printf("  %-17s %s\n", field[0], field[1]); code != exitOK {
			return code
		}
	}
	if code := env.printf("\n尝试时间线（%d 条）\n", len(view.Attempts)); code != exitOK {
		return code
	}
	for _, attempt := range view.Attempts {
		if code := env.printf("  #%d  %s  upstream_status=%s  failure_class=%s  error_code=%s  duration_ms=%d\n",
			attempt.Attempt, attempt.Outcome, formatOptionalInt(attempt.UpstreamStatus),
			formatOptionalString(attempt.FailureClass), formatOptionalString(attempt.ErrorCode),
			attempt.DurationMS); code != exitOK {
			return code
		}
	}
	sections := [][2]string{
		{"请求报文", formatJSONBlob(view.RequestShape)},
		{"上游请求报文", formatJSONBlob(view.UpstreamRequestShape)},
		{"错误响应报文", formatJSONBlob(view.ErrorResponseShape)},
		{"改写标注", formatRewrittenParts(view.RewrittenParts)},
	}
	for _, section := range sections {
		if code := env.printf("\n%s\n  %s\n", section[0], section[1]); code != exitOK {
			return code
		}
	}
	return exitOK
}

// formatOptionalInt 格式化可空整数；nil 输出占位符。
func formatOptionalInt(value *int) string {
	if value == nil {
		return "-"
	}
	return strconv.Itoa(*value)
}

// formatOptionalString 格式化可空字符串；nil 或空串输出占位符。
func formatOptionalString(value *string) string {
	if value == nil || *value == "" {
		return "-"
	}
	return *value
}

// formatJSONBlob 输出脱敏报文的原文；无报文时给占位符。
func formatJSONBlob(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "-"
	}
	return string(raw)
}

// formatRewrittenParts 输出改写标注；无标注时给占位符。
func formatRewrittenParts(parts []string) string {
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}
