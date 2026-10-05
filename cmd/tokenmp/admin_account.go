package main

import (
	"context"
	"strconv"

	"github.com/sumwai/tokenmp/internal/admin"
)

// 本文件是 `admin account` 与 `admin key` 的命令层。

func adminAccount(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("account 需要动作：create / list / disable / set-multiplier / set-merchant")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionCreate:
		return adminAccountCreate(ctx, rest, env)
	case actionList:
		return adminAccountList(ctx, rest, env)
	case actionDisable:
		return adminAccountDisable(ctx, rest, env)
	case actionSetMultiplier:
		return adminAccountSetMultiplier(ctx, rest, env)
	case actionSetMerchant:
		return adminAccountSetMerchant(ctx, rest, env)
	default:
		return env.usageErrorf("account 未知动作 %q", action)
	}
}

func adminAccountCreate(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin account create")
	code := fs.String(flagCode, "", "账户 code")
	name := fs.String(flagName, "", "账户名")
	merchant := fs.Uint64(flagMerchant, 0, "默认结算商家 id；不填走平台自营")
	multiplier := fs.String(flagMultiplier, "", "账户倍率，默认 1")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	input := admin.AccountInput{Code: *code, Name: *name, PriceMultiplier: *multiplier}
	if *merchant != 0 {
		input.DefaultMerchantID = merchant
	}
	id, err := env.service.CreateAccount(ctx, input)
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已创建账户 id=%d\n", id)
}

func adminAccountList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin account list")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	accounts, err := env.service.ListAccounts(ctx)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(accounts))
	for _, a := range accounts {
		rows = append(rows, []string{
			strconv.FormatUint(a.ID, 10), a.Code, a.Name,
			formatUintPtr(a.DefaultMerchantID), a.PriceMultiplier, a.Status,
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagCode, flagName, "default_merchant", flagMultiplier, headerStatus}, rows, accounts)
}

func adminAccountDisable(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin account disable")
	id := fs.Uint64(flagID, 0, "账户 id")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := env.service.DisableAccount(ctx, *id); err != nil {
		return env.fail(err)
	}
	return env.printf("已停用账户 id=%d\n", *id)
}

func adminAccountSetMultiplier(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin account set-multiplier")
	id := fs.Uint64(flagID, 0, "账户 id")
	multiplier := fs.String(flagMultiplier, "", "账户倍率")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *multiplier == "" {
		return env.usageError("account set-multiplier 需要 --multiplier")
	}
	if err := env.service.SetAccountMultiplier(ctx, *id, *multiplier); err != nil {
		return env.fail(err)
	}
	return env.printf("已置位账户倍率 id=%d multiplier=%s\n", *id, *multiplier)
}

func adminAccountSetMerchant(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin account set-merchant")
	id := fs.Uint64(flagID, 0, "账户 id")
	merchant := fs.Uint64(flagMerchant, 0, "默认结算商家 id")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *merchant == 0 {
		return env.usageError("account set-merchant 需要 --merchant")
	}
	if err := env.service.SetAccountMerchant(ctx, *id, *merchant); err != nil {
		return env.fail(err)
	}
	return env.printf("已置位账户默认商家 id=%d merchant=%d\n", *id, *merchant)
}

func adminKey(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("key 需要动作：issue / list / revoke")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionIssue:
		return adminKeyIssue(ctx, rest, env)
	case actionList:
		return adminKeyList(ctx, rest, env)
	case actionRevoke:
		return adminKeyRevoke(ctx, rest, env)
	default:
		return env.usageErrorf("key 未知动作 %q", action)
	}
}

func adminKeyIssue(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin key issue")
	account := fs.Uint64(flagAccount, 0, "账户 id")
	merchant := fs.Uint64(flagMerchant, 0, "绑定商家 id；不填跟随账户默认")
	name := fs.String(flagName, "", "密钥名")
	expires := fs.String(flagExpires, "", "过期时刻；不填不过期")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	expiresAt, err := parseAdminTime(*expires)
	if err != nil {
		return env.usageError(err.Error())
	}
	input := admin.IssueKeyInput{AccountID: *account, Name: *name}
	if *merchant != 0 {
		input.MerchantID = merchant
	}
	if !expiresAt.IsZero() {
		input.ExpiresAt = &expiresAt
	}
	issued, err := env.service.IssueKey(ctx, input)
	if err != nil {
		return env.fail(err)
	}
	// 明文只在这一次输出：库中只有哈希，之后无法恢复。
	return env.printf(
		"已签发密钥 id=%d account=%d prefix=%s\n明文（仅此一次，之后不可恢复）：%s\n",
		issued.ID, issued.AccountID, issued.Prefix, issued.Plaintext)
}

func adminKeyList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin key list")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	keys, err := env.service.ListKeys(ctx)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, []string{
			strconv.FormatUint(k.ID, 10), strconv.FormatUint(k.AccountID, 10),
			formatUintPtr(k.MerchantID), k.Name, k.KeyPrefix, formatBool(k.Enabled),
			formatTimePtr(k.ExpiresAt), formatTimePtr(k.LastUsedAt),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagAccount, flagMerchant, flagName, "prefix", headerEnabled, "expires_at", "last_used_at"},
		rows, keys)
}

func adminKeyRevoke(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin key revoke")
	id := fs.Uint64(flagID, 0, "密钥 id")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := env.service.RevokeKey(ctx, *id); err != nil {
		return env.fail(err)
	}
	return env.printf("已吊销密钥 id=%d\n", *id)
}
