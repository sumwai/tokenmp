package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件是 `admin bucket`、`admin product` 与 `admin purchase` 的命令层。

func adminBucket(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("bucket 需要动作：credit / list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionCredit:
		return adminBucketCredit(ctx, rest, env)
	case actionList:
		return adminBucketList(ctx, rest, env)
	default:
		return env.usageErrorf("bucket 未知动作 %q", action)
	}
}

func adminBucketCredit(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin bucket credit")
	account := fs.Uint64(flagAccount, 0, "账户 id")
	merchant := fs.Uint64(flagMerchant, 0, "归属商家 id")
	unit := fs.String(flagUnit, "", "结算单位：currency | token | credit")
	amount := fs.String(flagAmount, "", "发放数量")
	fallback := fs.String(flagFallback, "", "扣尽处置：charge_balance | reject")
	source := fs.String(flagSource, "", "账本来路：purchase | grant | recharge")
	expires := fs.String(flagExpires, "", "过期时刻；不填不过期")
	priority := fs.Int(flagPriority, 0, "扣减优先级，默认 100")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	settleUnit := billing.UnitSettle(*unit)
	if err := billing.ValidateUnitSettle(settleUnit); err != nil {
		return env.usageError(err.Error())
	}
	if err := billing.ValidateFallback(billing.Fallback(*fallback)); err != nil {
		return env.usageError(err.Error())
	}
	if err := billing.ValidateSource(billing.Source(*source)); err != nil {
		return env.usageError(err.Error())
	}
	expiresAt, err := parseAdminTime(*expires)
	if err != nil {
		return env.usageError(err.Error())
	}
	input := admin.CreditBucketInput{
		AccountID:  *account,
		MerchantID: *merchant,
		Unit:       settleUnit,
		Amount:     *amount,
		Fallback:   billing.Fallback(*fallback),
		Source:     billing.Source(*source),
		Priority:   *priority,
	}
	if !expiresAt.IsZero() {
		input.ExpiresAt = &expiresAt
	}
	id, err := env.service.CreditBucket(ctx, input)
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已发放账本 id=%d\n", id)
}

func adminBucketList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin bucket list")
	account := fs.Uint64(flagAccount, 0, "账户 id；不填列出全部")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	buckets, err := env.service.ListBuckets(ctx, *account)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(buckets))
	for _, b := range buckets {
		rows = append(rows, []string{
			strconv.FormatUint(b.ID, 10), strconv.FormatUint(b.AccountID, 10),
			strconv.FormatUint(b.MerchantID, 10), string(b.Unit), b.Total, b.Remaining,
			formatTimePtr(b.ExpiresAt), string(b.Fallback), string(b.Source), strconv.Itoa(b.Priority),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagAccount, flagMerchant, flagUnit, "total", "remaining", "expires_at", flagFallback, flagSource, flagPriority},
		rows, buckets)
}

func adminProduct(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("product 需要动作：create / list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionCreate:
		return adminProductCreate(ctx, rest, env)
	case actionList:
		return adminProductList(ctx, rest, env)
	default:
		return env.usageErrorf("product 未知动作 %q", action)
	}
}

func adminProductCreate(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin product create")
	merchant := fs.Uint64(flagMerchant, 0, "归属商家 id")
	name := fs.String(flagName, "", "商品名")
	unit := fs.String(flagUnit, "", "结算单位：currency | token | credit")
	qty := fs.String(flagQty, "", "每份数量")
	price := fs.String(flagPrice, "", "每份售价")
	modelScope := fs.String(flagModelScope, "", "可用模型范围 JSON 数组")
	validity := fs.Int(flagValidity, 0, "有效天数，0 表示不过期")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	settleUnit := billing.UnitSettle(*unit)
	if err := billing.ValidateUnitSettle(settleUnit); err != nil {
		return env.usageError(err.Error())
	}
	var scope json.RawMessage
	if strings.TrimSpace(*modelScope) != "" {
		scope = json.RawMessage(*modelScope)
	}
	id, err := env.service.CreateProduct(ctx, admin.ProductInput{
		MerchantID:   *merchant,
		Name:         *name,
		Unit:         settleUnit,
		Qty:          *qty,
		Price:        *price,
		ModelScope:   scope,
		ValidityDays: *validity,
	})
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已上架商品 id=%d\n", id)
}

func adminProductList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin product list")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	products, err := env.service.ListProducts(ctx)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(products))
	for _, p := range products {
		rows = append(rows, []string{
			strconv.FormatUint(p.ID, 10), strconv.FormatUint(p.MerchantID, 10), p.Name,
			string(p.Unit), p.Qty, p.Price, string(p.ModelScope), strconv.Itoa(p.ValidityDays),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagMerchant, flagName, flagUnit, flagQty, flagPrice, "model_scope", "validity_days"},
		rows, products)
}

func adminPurchase(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("purchase 需要动作：buy / list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionBuy:
		return adminPurchaseBuy(ctx, rest, env)
	case actionList:
		return adminPurchaseList(ctx, rest, env)
	default:
		return env.usageErrorf("purchase 未知动作 %q", action)
	}
}

func adminPurchaseBuy(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin purchase buy")
	account := fs.Uint64(flagAccount, 0, "账户 id")
	product := fs.Uint64(flagProduct, 0, "商品 id")
	qty := fs.String(flagQty, "", "购买份数")
	fallback := fs.String(flagFallback, "", "扣尽处置，默认 charge_balance")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	input := admin.BuyInput{AccountID: *account, ProductID: *product, Qty: *qty}
	if *fallback != "" {
		if err := billing.ValidateFallback(billing.Fallback(*fallback)); err != nil {
			return env.usageError(err.Error())
		}
		input.Fallback = billing.Fallback(*fallback)
	}
	result, err := env.service.Buy(ctx, input)
	if err != nil {
		return env.fail(err)
	}
	return env.printf(
		"已购买 purchase=%d bucket=%d 数量=%s 实付=%s 单位=%s 折算率=%s 到期=%s\n",
		result.PurchaseID, result.BucketID, result.Total, result.PricePaid,
		string(result.Unit), result.UnitRate, formatTimePtr(result.ExpiresAt))
}

func adminPurchaseList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin purchase list")
	account := fs.Uint64(flagAccount, 0, "账户 id；不填列出全部")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	purchases, err := env.service.ListPurchases(ctx, *account)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(purchases))
	for _, p := range purchases {
		rows = append(rows, []string{
			strconv.FormatUint(p.ID, 10), strconv.FormatUint(p.AccountID, 10),
			strconv.FormatUint(p.MerchantID, 10), strconv.FormatUint(p.ProductID, 10),
			p.Qty, p.PricePaid, formatTime(p.PurchasedAt),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagAccount, flagMerchant, flagProduct, flagQty, "price_paid", "purchased_at"},
		rows, purchases)
}
