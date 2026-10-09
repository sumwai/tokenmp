package main

import (
	"context"
	"strconv"

	"github.com/sumwai/tokenmp/internal/admin"
)

// 本文件是 `admin settlement` 的命令层：商家分账口径的读写与按账期出账。
//
// 命令层只做参数解析与输出；口径（抽成率取值域、账期边界、四处金额的关系）
// 在 internal/settlement，取事实与装配在 internal/admin。

func adminSettlement(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("settlement 需要动作：set / get / list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionSet:
		return adminSettlementSet(ctx, rest, env)
	case actionGet:
		return adminSettlementGet(ctx, rest, env)
	case actionList:
		return adminSettlementList(ctx, rest, env)
	default:
		return env.usageErrorf("settlement 未知动作 %q", action)
	}
}

func adminSettlementSet(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin settlement set")
	merchant := fs.Uint64(flagMerchant, 0, "商家 id")
	rate := fs.String(flagCommissionRate, "", "平台抽成率，取值 [0,1)；不填保留现值")
	period := fs.String(flagPeriod, "", "结算账期：day | week | month；不填保留现值")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	info, err := env.service.SetMerchantSettleInfo(ctx, admin.SettleInfoInput{
		MerchantID:     *merchant,
		CommissionRate: *rate,
		Period:         *period,
	})
	if err != nil {
		return env.fail(err)
	}
	view := info.View(*merchant)
	return env.printf("已写入商家 %d 的分账口径：抽成率 %s，账期 %s\n",
		view.MerchantID, view.CommissionRate, view.Period)
}

func adminSettlementGet(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin settlement get")
	merchant := fs.Uint64(flagMerchant, 0, "商家 id")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	info, err := env.service.MerchantSettleInfo(ctx, *merchant)
	if err != nil {
		return env.fail(err)
	}
	view := info.View(*merchant)
	rows := [][]string{{strconv.FormatUint(view.MerchantID, 10), view.CommissionRate, view.Period}}
	return env.emit(*asJSON, []string{headerMerchantID, headerCommissionRate, flagPeriod}, rows, view)
}

func adminSettlementList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin settlement list")
	merchant := fs.Uint64(flagMerchant, 0, "商家 id；不填列出全部商家")
	from := fs.String(flagFrom, "", "账期起点（含）；与 --to 一起给出，不填按各商家账期取上一个完整自然周期")
	to := fs.String(flagTo, "", "账期终点（不含）；与 --from 一起给出")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	fromAt, err := parseAdminTime(*from)
	if err != nil {
		return env.usageError(err.Error())
	}
	toAt, err := parseAdminTime(*to)
	if err != nil {
		return env.usageError(err.Error())
	}
	// 只给一侧会让另一半落到零值，账期变成「世界上所有流水」，那是用法错误而不是默认值。
	if fromAt.IsZero() != toAt.IsZero() {
		return env.usageError("settlement list 的 --from 与 --to 要么都给，要么都不给")
	}
	if !fromAt.IsZero() && !toAt.After(fromAt) {
		return env.usageError("settlement list 的 --to 必须晚于 --from")
	}
	bills, err := env.service.SettlementBills(ctx, admin.SettlementQuery{
		MerchantID: *merchant,
		From:       fromAt,
		To:         toAt,
	})
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(bills))
	for _, b := range bills {
		rows = append(rows, []string{
			strconv.FormatUint(b.MerchantID, 10), b.Period, b.From, b.To,
			strconv.FormatInt(b.Trades, 10),
			b.GrossSales, b.Commission, b.UpstreamCost, b.Payout,
		})
	}
	return env.emit(*asJSON,
		[]string{headerMerchantID, flagPeriod, flagFrom, flagTo, "trades", "gross_sales", "commission", "upstream_cost", "payout"},
		rows, bills)
}
