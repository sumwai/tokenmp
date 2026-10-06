package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/quota"
)

// 本文件是 `admin plan` 的命令层：add / list。
//
// 上游套餐按 (merchant, cred_group) 建模，配额行的窗口语义与下游限额同口径。
// 枚举与窗口组合在命令层先校验一次，让用法错误以退出码 2 报出；业务层再校验一次。

// planQuotaPartCount 是 --quota 参数的分段数：metric:window:limit。
const planQuotaPartCount = 3

func adminPlan(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("plan 需要动作：add / list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionAdd:
		return adminPlanAdd(ctx, rest, env)
	case actionList:
		return adminPlanList(ctx, rest, env)
	default:
		return env.usageErrorf("plan 未知动作 %q", action)
	}
}

func adminPlanAdd(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin plan add")
	merchant := fs.Uint64(flagMerchant, 0, "归属商家 id")
	credGroup := fs.String(flagCredGroup, "", "凭据分组；同厂商多端点共享一份套餐额度")
	name := fs.String(flagName, "", "套餐名")
	multiplier := fs.String(flagMultiplier, "", "套餐倍率，默认 1")
	validFrom := fs.String(flagValidFrom, "", "有效期起点；不填表示不限")
	validTo := fs.String(flagValidTo, "", "有效期终点（不含）；不填表示不限")
	var quotaArgs stringList
	fs.Var(&quotaArgs, "quota", "限额行 metric:window:limit，可重复；window 形如 calendar/day、rolling/5h，也可只写 day / 5h / total")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *merchant == 0 || strings.TrimSpace(*credGroup) == "" || strings.TrimSpace(*name) == "" {
		return env.usageError("plan add 需要 --merchant --cred-group --name，且 --quota 至少一条")
	}
	from, err := parseAdminTime(*validFrom)
	if err != nil {
		return env.usageError(err.Error())
	}
	to, err := parseAdminTime(*validTo)
	if err != nil {
		return env.usageError(err.Error())
	}
	quotas, err := parsePlanQuotaArgs(quotaArgs)
	if err != nil {
		return env.usageError(err.Error())
	}
	id, err := env.service.CreatePlan(ctx, admin.PlanInput{
		MerchantID: *merchant,
		CredGroup:  strings.TrimSpace(*credGroup),
		Name:       strings.TrimSpace(*name),
		Multiplier: strings.TrimSpace(*multiplier),
		ValidFrom:  timePtrOrNil(from),
		ValidTo:    timePtrOrNil(to),
		Quotas:     quotas,
	})
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已写入套餐 id=%d\n", id)
}

// timePtrOrNil 把可空的零值时间转成指针：零值即「不限」。
func timePtrOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// parsePlanQuotaArgs 解析可重复的 --quota 参数。
func parsePlanQuotaArgs(args []string) ([]admin.PlanQuotaInput, error) {
	if len(args) == 0 {
		return nil, errors.New("plan add 至少需要一条 --quota")
	}
	quotas := make([]admin.PlanQuotaInput, 0, len(args))
	for _, arg := range args {
		quota, err := parsePlanQuotaArg(arg)
		if err != nil {
			return nil, err
		}
		quotas = append(quotas, quota)
	}
	return quotas, nil
}

// parsePlanQuotaArg 解析一条 metric:window:limit。
//
// 用冒号而不是逗号分隔：metric 与 period 都不含冒号，而逗号在习惯上分隔同一字段的多值。
func parsePlanQuotaArg(arg string) (admin.PlanQuotaInput, error) {
	parts := strings.Split(arg, ":")
	if len(parts) != planQuotaPartCount {
		return admin.PlanQuotaInput{}, fmt.Errorf("--quota %q 必须形如 metric:window:limit", arg)
	}
	metric := billing.Metric(strings.TrimSpace(parts[0]))
	if err := billing.ValidateMetric(metric); err != nil {
		return admin.PlanQuotaInput{}, err
	}
	kind, period, err := parsePlanWindow(strings.TrimSpace(parts[1]))
	if err != nil {
		return admin.PlanQuotaInput{}, err
	}
	limit := strings.TrimSpace(parts[2])
	if limit == "" {
		return admin.PlanQuotaInput{}, fmt.Errorf("--quota %q 缺少 limit", arg)
	}
	return admin.PlanQuotaInput{Metric: metric, WindowKind: kind, Period: period, LimitAmount: limit}, nil
}

// parsePlanWindow 解析窗口：显式 kind/period，或只写 period 由周期推出 kind。
//
// 只写周期的形态覆盖常见配置：5h 一定配 rolling，day/week/month/total 一定配 calendar。
// 需要非默认组合时写全 kind/period。
func parsePlanWindow(raw string) (billing.WindowKind, billing.Period, error) {
	if kindText, periodText, found := strings.Cut(raw, "/"); found {
		kind := billing.WindowKind(strings.TrimSpace(kindText))
		period := billing.Period(strings.TrimSpace(periodText))
		if err := quota.ValidWindow(kind, period); err != nil {
			return "", "", err
		}
		return kind, period, nil
	}
	period := billing.Period(raw)
	kind := billing.WindowKindCalendar
	if period == billing.Period5h {
		kind = billing.WindowKindRolling
	}
	if err := quota.ValidWindow(kind, period); err != nil {
		return "", "", err
	}
	return kind, period, nil
}

func adminPlanList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin plan list")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	views, err := env.service.ListPlans(ctx)
	if err != nil {
		return env.fail(err)
	}
	// 表格按「一行一条限额」摊平：套餐与限额是两层结构，摊平后每行自带判定所需的全部列。
	rows := make([][]string, 0, len(views))
	for _, view := range views {
		planPrefix := []string{
			strconv.FormatUint(view.ID, 10), strconv.FormatUint(view.MerchantID, 10),
			view.CredGroup, view.Name, view.Multiplier, formatTimePtr(view.LastCheckedAt),
		}
		if len(view.Quotas) == 0 {
			rows = append(rows, append(planPrefix, "-", "-", "-", "-", "-", "-", "-", "-", "-"))
			continue
		}
		for _, q := range view.Quotas {
			rows = append(rows, append(append([]string(nil), planPrefix...),
				strconv.FormatUint(q.ID, 10), string(q.Metric), string(q.WindowKind), string(q.Period),
				q.LimitAmount, q.LastUsed, formatOptional(q.UsedPercent), formatTimePtr(q.LastCheckedAt), formatTimePtr(q.ResetsAt),
			))
		}
	}
	return env.emit(*asJSON,
		[]string{flagID, flagMerchant, headerCredGroup, flagName, flagMultiplier, "last_checked_at",
			"quota_id", flagMetric, "window_kind", flagPeriod, "limit_amount", "last_used", "used_percent",
			"quota_checked_at", "resets_at"},
		rows, views)
}
