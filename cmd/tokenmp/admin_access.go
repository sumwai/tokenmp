package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/sumwai/tokenmp/internal/admin"
)

// 本文件是 `admin credential` 与 `admin model-map` 的命令层。

func adminCredential(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("credential 需要动作：add / list / disable")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionAdd:
		return adminCredentialAdd(ctx, rest, env)
	case actionList:
		return adminCredentialList(ctx, rest, env)
	case actionDisable:
		return adminCredentialDisable(ctx, rest, env)
	case actionOAuthLogin:
		return adminCredentialOAuthLogin(ctx, rest, env)
	default:
		return env.usageErrorf("credential 未知动作 %q", action)
	}
}

func adminCredentialAdd(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin credential add")
	merchant := fs.Uint64(flagMerchant, 0, "归属商家 id")
	group := fs.String(flagGroup, "", "凭据分组")
	name := fs.String(flagName, "", "凭据名（同组内区分轮换）")
	apiKey := fs.String(flagAPIKey, "", "凭据明文；留空时从标准输入读")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	key := strings.TrimSpace(*apiKey)
	if key == "" {
		// 从标准输入读：命令行参数会进 shell 历史与进程列表，明文密钥经 stdin 传递更稳。
		raw, err := io.ReadAll(env.stdin)
		if err != nil {
			return env.fail(err)
		}
		key = strings.TrimSpace(string(raw))
	}
	if key == "" {
		return env.usageError("credential add 需要 --api-key 或从标准输入提供明文")
	}
	id, err := env.service.AddCredential(ctx, admin.CredentialInput{
		MerchantID: *merchant,
		Group:      *group,
		Name:       *name,
		APIKey:     key,
	})
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已写入凭据 id=%d\n", id)
}

func adminCredentialList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin credential list")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	views, err := env.service.ListCredentials(ctx)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(views))
	for _, v := range views {
		rows = append(rows, []string{
			strconv.FormatUint(v.ID, 10), strconv.FormatUint(v.MerchantID, 10),
			v.CredGroup, v.Name, v.Prefix, formatBool(v.Enabled),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagMerchant, headerCredGroup, flagName, "prefix", headerEnabled}, rows, views)
}

func adminCredentialDisable(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin credential disable")
	id := fs.Uint64(flagID, 0, "凭据 id")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := env.service.DisableCredential(ctx, *id); err != nil {
		return env.fail(err)
	}
	return env.printf("已停用凭据 id=%d\n", *id)
}

// adminCredentialOAuthLogin 执行一次 OAuth 登录并写入凭据。
//
// 用法兼容两种参数顺序：`oauth-login <group> --merchant <id>`（组名在前）与
// `oauth-login --merchant <id> <group>`（flags 在前）。
// 输出只含 group 与 account 标识，不回显任何令牌。
func adminCredentialOAuthLogin(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin credential oauth-login")
	merchant := fs.Uint64(flagMerchant, 0, "归属商家 id")
	name := fs.String(flagName, "", "凭据名（默认取 account）")
	account := fs.String(flagAccount, "", "账户标识（端点未返回时使用）")
	code := fs.String(flagCode, "", "授权码；留空时从标准输入读")

	group := ""
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		group = args[0]
		rest = args[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if group == "" && fs.NArg() > 0 {
		group = fs.Arg(0)
	}
	if strings.TrimSpace(group) == "" {
		return env.usageError("credential oauth-login 需要凭据分组")
	}

	hooks := admin.OAuthLoginHooks{
		OnDeviceCode: func(verificationURI, userCode string) {
			_, _ = fmt.Fprintf(env.stdout, "请在浏览器打开 %s 并输入用户码 %s\n", verificationURI, userCode)
		},
		OnAuthorizeURL: func(authorizeURL string) {
			_, _ = fmt.Fprintf(env.stdout, "请在浏览器打开授权地址：%s\n", authorizeURL)
		},
		ReadCode: func() (string, error) {
			_, _ = io.WriteString(env.stdout, "粘贴回调地址里的 code 后回车：")
			line, err := bufio.NewReader(env.stdin).ReadString('\n')
			return line, err
		},
	}
	result, err := env.service.OAuthLogin(ctx, admin.OAuthLoginInput{
		MerchantID: *merchant,
		Group:      group,
		Name:       *name,
		Account:    *account,
		Code:       *code,
	}, hooks)
	if err != nil {
		return env.fail(err)
	}
	return env.printf("OAuth 登录完成：id=%d group=%s account=%s flow=%s\n",
		result.ID, result.Group, result.Account, result.Flow)
}

func adminModelMap(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("model-map 需要动作：set / list / disable")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionSet:
		return adminModelMapSet(ctx, rest, env)
	case actionList:
		return adminModelMapList(ctx, rest, env)
	case actionDisable:
		return adminModelMapDisable(ctx, rest, env)
	default:
		return env.usageErrorf("model-map 未知动作 %q", action)
	}
}

func adminModelMapSet(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin model-map set")
	channel := fs.Uint64(flagChannel, 0, "渠道 id")
	model := fs.String(flagModel, "", "客户端模型名")
	upstreamModel := fs.String(flagUpstreamModel, "", "上游模型名")
	multiplier := fs.String(flagMultiplier, "", "渠道倍率，默认 1")
	overrides := fs.String(flagOverrides, "", "请求覆盖 JSON 对象")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	var rawOverrides json.RawMessage
	if strings.TrimSpace(*overrides) != "" {
		rawOverrides = json.RawMessage(*overrides)
	}
	id, err := env.service.SetModelMap(ctx, admin.ModelMapInput{
		ChannelID:        *channel,
		Model:            *model,
		UpstreamModel:    *upstreamModel,
		PriceMultiplier:  *multiplier,
		RequestOverrides: rawOverrides,
	})
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已写入模型映射 id=%d\n", id)
}

func adminModelMapList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin model-map list")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	maps, err := env.service.ListModelMaps(ctx)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(maps))
	for _, m := range maps {
		rows = append(rows, []string{
			strconv.FormatUint(m.ID, 10), strconv.FormatUint(m.ChannelID, 10), m.Model,
			m.UpstreamModel, m.PriceMultiplier, string(m.RequestOverrides), formatBool(m.Enabled),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagChannel, flagModel, "upstream_model", flagMultiplier, "overrides", headerEnabled},
		rows, maps)
}

func adminModelMapDisable(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin model-map disable")
	id := fs.Uint64(flagID, 0, "模型映射 id")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := env.service.DisableModelMap(ctx, *id); err != nil {
		return env.fail(err)
	}
	return env.printf("已停用模型映射 id=%d\n", *id)
}
