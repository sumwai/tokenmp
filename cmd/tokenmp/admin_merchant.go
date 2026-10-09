package main

import (
	"context"
	"strconv"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是 `admin merchant` 与 `admin channel` 的命令层。

func adminMerchant(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("merchant 需要动作：create / list / disable")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionCreate:
		return adminMerchantCreate(ctx, rest, env)
	case actionList:
		return adminMerchantList(ctx, rest, env)
	case actionDisable:
		return adminMerchantDisable(ctx, rest, env)
	case actionSetOwner:
		return adminMerchantSetOwner(ctx, rest, env)
	default:
		return env.usageErrorf("merchant 未知动作 %q", action)
	}
}

func adminMerchantCreate(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin merchant create")
	code := fs.String(flagCode, "", "商家 code")
	name := fs.String(flagName, "", "商家 name")
	kind := fs.String(flagKind, "", "商家类型：platform | partner")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *code == "" || *name == "" || *kind == "" {
		return env.usageError("merchant create 需要 --code --name --kind")
	}
	merchantKind := store.MerchantKind(*kind)
	if err := store.ValidateMerchantKind(merchantKind); err != nil {
		return env.usageError(err.Error())
	}
	id, err := env.service.CreateMerchant(ctx, *code, *name, merchantKind)
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已创建商家 id=%d\n", id)
}

func adminMerchantList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin merchant list")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	merchants, err := env.service.ListMerchants(ctx)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(merchants))
	for _, m := range merchants {
		rows = append(rows, []string{
			strconv.FormatUint(m.ID, 10), m.Code, m.Name, string(m.Kind), m.Status,
			formatOwner(m.OwnerUserID), formatTime(m.CreatedAt),
		})
	}
	return env.emit(*asJSON, []string{flagID, flagCode, flagName, flagKind, headerStatus, flagOwner, headerCreatedAt}, rows, merchants)
}

// formatOwner 渲染商家的归属登录主体；未绑定时留空而不是写 0。
func formatOwner(owner *uint64) string {
	if owner == nil {
		return ""
	}
	return strconv.FormatUint(*owner, 10)
}

func adminMerchantSetOwner(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin merchant set-owner")
	id := fs.Uint64(flagID, 0, "商家 id")
	owner := fs.Uint64(flagOwner, 0, "归属的登录主体 id")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := env.service.SetMerchantOwner(ctx, *id, *owner); err != nil {
		return env.fail(err)
	}
	return env.printf("已绑定商家 id=%d 到登录主体 %d\n", *id, *owner)
}

func adminMerchantDisable(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin merchant disable")
	id := fs.Uint64(flagID, 0, "商家 id")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := env.service.DisableMerchant(ctx, *id); err != nil {
		return env.fail(err)
	}
	return env.printf("已停用商家 id=%d\n", *id)
}

func adminChannel(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("channel 需要动作：create / list / enable / disable")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionCreate:
		return adminChannelCreate(ctx, rest, env)
	case actionList:
		return adminChannelList(ctx, rest, env)
	case actionEnable:
		return adminChannelSetEnabled(ctx, rest, env, true)
	case actionDisable:
		return adminChannelSetEnabled(ctx, rest, env, false)
	default:
		return env.usageErrorf("channel 未知动作 %q", action)
	}
}

func adminChannelCreate(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin channel create")
	merchant := fs.Uint64(flagMerchant, 0, "归属商家 id")
	name := fs.String(flagName, "", "渠道名")
	channelType := fs.String(flagType, "", "协议方言：openai_chat | openai_responses | anthropic_messages | gemini_generate")
	baseURL := fs.String(flagBaseURL, "", "上游根地址")
	credGroup := fs.String(flagCredGroup, "", "凭据分组")
	vendor := fs.String(flagVendor, "", "上游厂商标签")
	priority := fs.Int(flagPriority, 0, "路由优先级，默认 100")
	weight := fs.Int(flagWeight, 0, "加权随机权重，默认 100")
	credentialStyle := fs.String(flagCredentialStyle, "", "凭据注入形态：authorization | x-api-key | x-goog-api-key | query；留空按协议现状")
	config := fs.String(flagConfig, "", "渠道级扩展配置 JSON；上游套餐探针声明写在这里")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	kind := store.ChannelType(*channelType)
	if err := store.ValidateChannelType(kind); err != nil {
		return env.usageError(err.Error())
	}
	style := domain.CredentialHeaderStyle(*credentialStyle)
	if *credentialStyle != "" && !style.Valid() {
		return env.usageErrorf("凭据注入形态 %q 不受支持", *credentialStyle)
	}
	id, err := env.service.CreateChannel(ctx, admin.ChannelInput{
		MerchantID:      *merchant,
		Name:            *name,
		Vendor:          *vendor,
		Type:            kind,
		CredGroup:       *credGroup,
		BaseURL:         *baseURL,
		Priority:        *priority,
		Weight:          *weight,
		Config:          *config,
		CredentialStyle: style,
	})
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已创建渠道 id=%d\n", id)
}

func adminChannelList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin channel list")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	channels, err := env.service.ListChannels(ctx)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(channels))
	for _, c := range channels {
		rows = append(rows, []string{
			strconv.FormatUint(c.ID, 10), strconv.FormatUint(c.MerchantID, 10), c.Name, c.Vendor,
			string(c.Type), c.CredGroup, c.BaseURL,
			strconv.Itoa(c.Priority), strconv.Itoa(c.Weight), formatBool(c.Enabled),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagMerchant, flagName, flagVendor, flagType, headerCredGroup, headerBaseURL, flagPriority, flagWeight, headerEnabled},
		rows, channels)
}

func adminChannelSetEnabled(ctx context.Context, args []string, env *adminEnv, enabled bool) int {
	fs := env.newFlagSet("admin channel")
	id := fs.Uint64(flagID, 0, "渠道 id")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	var err error
	label := "停用"
	if enabled {
		label = "启用"
		err = env.service.EnableChannel(ctx, *id)
	} else {
		err = env.service.DisableChannel(ctx, *id)
	}
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已%s渠道 id=%d\n", label, *id)
}
