package main

import (
	"context"
	"strconv"

	"github.com/sumwai/tokenmp/internal/admin"
)

// 本文件是 `admin usage` 与 `admin adjust` 的命令层。

func adminUsage(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("usage 需要动作：list")
	}
	if args[0] != actionList {
		return env.usageErrorf("usage 未知动作 %q", args[0])
	}
	fs := env.newFlagSet("admin usage list")
	account := fs.Uint64(flagAccount, 0, "账户 id；不填列出全部")
	since := fs.String(flagSince, "", "起始时刻；不填不限")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	sinceAt, err := parseAdminTime(*since)
	if err != nil {
		return env.usageError(err.Error())
	}
	records, err := env.service.ListUsage(ctx, *account, sinceAt)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(records))
	for _, r := range records {
		rows = append(rows, []string{
			strconv.FormatUint(r.ID, 10), formatTime(r.CreatedAt),
			strconv.FormatUint(r.AccountID, 10), strconv.FormatUint(r.ChannelID, 10),
			r.Model, r.GrossAmount, r.Multiplier, string(r.Usage), string(r.Settlement),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, headerCreatedAt, flagAccount, flagChannel, flagModel, "gross_amount", flagMultiplier, usageName, "settlement"},
		rows, records)
}

func adminAdjust(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("adjust 需要动作：add / list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionAdd:
		return adminAdjustAdd(ctx, rest, env)
	case actionList:
		return adminAdjustList(ctx, rest, env)
	default:
		return env.usageErrorf("adjust 未知动作 %q", action)
	}
}

func adminAdjustAdd(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin adjust add")
	account := fs.Uint64(flagAccount, 0, "账户 id")
	amount := fs.String(flagAmount, "", "调整数量，正数补扣、负数退费")
	reason := fs.String(flagReason, "", "原因")
	operator := fs.String(flagOperator, "", "操作者")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	id, err := env.service.AddAdjustment(ctx, admin.AdjustmentInput{
		AccountID: *account,
		Amount:    *amount,
		Reason:    *reason,
		Operator:  *operator,
	})
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已写入调账 id=%d\n", id)
}

func adminAdjustList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin adjust list")
	account := fs.Uint64(flagAccount, 0, "账户 id；不填列出全部")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	adjustments, err := env.service.ListAdjustments(ctx, *account)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(adjustments))
	for _, a := range adjustments {
		rows = append(rows, []string{
			strconv.FormatUint(a.ID, 10), strconv.FormatUint(a.AccountID, 10),
			a.DeltaAmount, a.Reason, a.Operator, formatTime(a.CreatedAt),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagAccount, "delta_amount", flagReason, flagOperator, headerCreatedAt}, rows, adjustments)
}
