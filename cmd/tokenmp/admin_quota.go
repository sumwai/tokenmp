package main

import (
	"context"
	"strconv"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/quota"
)

// 本文件是 `admin quota` 的命令层：add / list / del / reset。
//
// 枚举与窗口组合在命令层先校验一次，让用法错误以退出码 2 报出；业务层再校验一次，
// 两处共用 internal/billing 与 internal/quota 的白名单和组合表。

func adminQuota(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("quota 需要动作：add / list / del / reset")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionAdd:
		return adminQuotaAdd(ctx, rest, env)
	case actionList:
		return adminQuotaList(ctx, rest, env)
	case actionDel:
		return adminQuotaDel(ctx, rest, env)
	case actionReset:
		return adminQuotaReset(ctx, rest, env)
	default:
		return env.usageErrorf("quota 未知动作 %q", action)
	}
}

func adminQuotaAdd(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin quota add")
	scope := fs.String(flagScope, "", "范围：account | api_key | channel | plan")
	scopeID := fs.Uint64(flagScopeID, 0, "范围实体 id")
	metric := fs.String(flagMetric, "", "计量指标：input_token | … | request")
	window := fs.String(flagWindow, "", "窗口类型：rolling | calendar")
	period := fs.String(flagPeriod, "", "周期：5h | day | week | month | total")
	limit := fs.String(flagLimit, "", "限额上限")
	action := fs.String(flagAction, "", "超限处置：reject | throttle")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	quotaScope := billing.Scope(*scope)
	if err := billing.ValidateScope(quotaScope); err != nil {
		return env.usageError(err.Error())
	}
	quotaMetric := billing.Metric(*metric)
	if err := billing.ValidateMetric(quotaMetric); err != nil {
		return env.usageError(err.Error())
	}
	quotaWindow, quotaPeriod := billing.WindowKind(*window), billing.Period(*period)
	if err := quota.ValidWindow(quotaWindow, quotaPeriod); err != nil {
		return env.usageError(err.Error())
	}
	quotaAction := billing.Action(*action)
	if err := billing.ValidateAction(quotaAction); err != nil {
		return env.usageError(err.Error())
	}
	id, err := env.service.CreateQuota(ctx, admin.QuotaInput{
		Scope:       quotaScope,
		ScopeID:     *scopeID,
		Metric:      quotaMetric,
		WindowKind:  quotaWindow,
		Period:      quotaPeriod,
		LimitAmount: *limit,
		Action:      quotaAction,
	})
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已写入限额 id=%d\n", id)
}

func adminQuotaList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin quota list")
	account := fs.Uint64(flagAccount, 0, "账户 id；不填列出全部")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	// 只按 account 维度过滤：限额表允许其它 scope，但账户维度是运维最常用的查询入口。
	var scope billing.Scope
	var scopeID uint64
	if *account != 0 {
		scope, scopeID = billing.ScopeAccount, *account
	}
	views, err := env.service.ListQuotas(ctx, scope, scopeID)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(views))
	for _, v := range views {
		rows = append(rows, []string{
			strconv.FormatUint(v.ID, 10), string(v.Scope), strconv.FormatUint(v.ScopeID, 10),
			string(v.Metric), string(v.WindowKind), string(v.Period), v.LimitAmount, string(v.Action),
			formatOptional(v.Used), formatOptional(v.Remaining),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagScope, headerScopeID, flagMetric, "window_kind", flagPeriod, "limit_amount", flagAction, "used", "remaining"},
		rows, views)
}

func adminQuotaDel(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin quota del")
	id := fs.Uint64(flagID, 0, "限额 id")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := env.service.DeleteQuota(ctx, *id); err != nil {
		return env.fail(err)
	}
	return env.printf("已删除限额 id=%d\n", *id)
}

func adminQuotaReset(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin quota reset")
	id := fs.Uint64(flagID, 0, "限额 id")
	baseline := fs.String(flagBaseline, "", "重置基准时刻；不填取当前时刻")
	reason := fs.String(flagReason, "", "原因")
	operator := fs.String(flagOperator, "", "操作者")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	baselineAt, err := parseAdminTime(*baseline)
	if err != nil {
		return env.usageError(err.Error())
	}
	eventID, err := env.service.ResetQuota(ctx, admin.ResetQuotaInput{
		QuotaID:    *id,
		BaselineAt: baselineAt,
		Reason:     *reason,
		Operator:   *operator,
	})
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已重置限额 quota=%d event=%d\n", *id, eventID)
}

// formatOptional 渲染可空字符串；nil 输出占位符。
func formatOptional(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}
