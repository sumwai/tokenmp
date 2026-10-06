package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/sumwai/tokenmp/internal/credential"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/oauth"
	"github.com/sumwai/tokenmp/internal/route"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是 `admin credential oauth-login` 的业务层：按渠道声明的 OAuth 画像走一次
// 设备码或授权码登录，把换到的令牌写进凭据表。
//
// I/O 不在本层：用户码 / 授权 URL 的展示与回调 code 的读取经 hooks 交回命令层，
// 使业务层可以在测试与剧本里直接驱动，不必伪造终端。

// OAuthLoginInput 是一次 OAuth 登录的输入。
type OAuthLoginInput struct {
	MerchantID uint64
	// Group 是凭据分组，登录结果写入该分组。
	Group string
	// Name 是写入凭据行的名字；为空时取 account。
	Name string
	// Account 是账户标识的回退取值；端点应答里带了 account 时优先用端点的。
	Account string
	// Code 是授权码流程的回调码；为空时经 hooks.ReadCode 读取。
	Code string
}

// OAuthLoginHooks 是登录流程的外部交互点。
type OAuthLoginHooks struct {
	// OnDeviceCode 在设备码流程里展示验证地址与用户码。
	OnDeviceCode func(verificationURI, userCode string)
	// OnAuthorizeURL 在授权码流程里展示授权地址。
	OnAuthorizeURL func(authorizeURL string)
	// ReadCode 读取用户粘贴的回调码。
	ReadCode func() (string, error)
}

// OAuthLoginResult 是一次成功登录的结果：只回显分组与账户标识，不含任何令牌。
type OAuthLoginResult struct {
	ID      uint64
	Group   string
	Account string
	// Flow 是本次实际走的流程：device 或 code。
	Flow string
}

// 流程名，写进结果与日志。
const (
	oauthFlowDevice = "device"
	oauthFlowCode   = "code"
)

// OAuthLogin 执行一次 OAuth 登录并写入凭据行。
//
// 画像完全来自渠道 config：有 device_url 走设备码，否则要求 authorize_url 走授权码；
// 两者都没有时报错而不是猜一种流程。
func (s *Service) OAuthLogin(ctx context.Context, in OAuthLoginInput, hooks OAuthLoginHooks) (*OAuthLoginResult, error) {
	if err := requireID("凭据 merchant", in.MerchantID); err != nil {
		return nil, err
	}
	if err := requireString("凭据 group", in.Group); err != nil {
		return nil, err
	}
	config, err := s.store.ChannelConfigByCredGroup(ctx, in.MerchantID, in.Group)
	if err != nil {
		return nil, fmt.Errorf("admin: 读取凭据分组 %q 对应渠道的 config 失败: %w", in.Group, err)
	}
	profile := route.ParseOAuthProfile(config)
	if !profile.Configured() {
		return nil, fmt.Errorf("admin: 凭据分组 %q 对应的渠道未声明可用的 oauth 画像（需要 token_url 与 client_id）", in.Group)
	}

	token, flow, err := s.runOAuthFlow(ctx, profile, in, hooks)
	if err != nil {
		return nil, err
	}
	account := firstNonEmptyString(token.Account, in.Account, in.Name)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = account
	}
	secret, err := credential.BuildOAuthSecret(token.Access, token.Refresh, token.Expires, account)
	if err != nil {
		return nil, fmt.Errorf("admin: 编码 OAuth 凭据失败: %w", err)
	}
	id, err := s.store.InsertCredential(ctx, store.CredentialRow{
		MerchantID: in.MerchantID,
		CredGroup:  in.Group,
		Name:       name,
		Secret:     secret,
	})
	if err != nil {
		return nil, err
	}
	return &OAuthLoginResult{ID: id, Group: in.Group, Account: account, Flow: flow}, nil
}

// runOAuthFlow 按画像选择并执行一种 OAuth 流程。
func (s *Service) runOAuthFlow(
	ctx context.Context,
	profile domain.OAuthProfile,
	in OAuthLoginInput,
	hooks OAuthLoginHooks,
) (oauth.Token, string, error) {
	switch {
	case profile.SupportsDeviceCode():
		device, err := s.oauth.StartDeviceCode(ctx, profile)
		if err != nil {
			return oauth.Token{}, "", fmt.Errorf("admin: 发起设备码授权失败: %w", err)
		}
		if hooks.OnDeviceCode != nil {
			hooks.OnDeviceCode(device.VerificationURI, device.UserCode)
		}
		token, err := s.oauth.PollDeviceCode(ctx, profile, device)
		if err != nil {
			return oauth.Token{}, "", fmt.Errorf("admin: 设备码授权未完成: %w", err)
		}
		return token, oauthFlowDevice, nil
	case profile.SupportsAuthorizationCode():
		authorizeURL, err := s.oauth.AuthorizeURL(profile, "")
		if err != nil {
			return oauth.Token{}, "", fmt.Errorf("admin: 拼装授权地址失败: %w", err)
		}
		if hooks.OnAuthorizeURL != nil {
			hooks.OnAuthorizeURL(authorizeURL)
		}
		code := strings.TrimSpace(in.Code)
		if code == "" {
			if hooks.ReadCode == nil {
				return oauth.Token{}, "", fmt.Errorf("admin: 授权码流程需要提供回调 code")
			}
			code, err = hooks.ReadCode()
			if err != nil {
				return oauth.Token{}, "", fmt.Errorf("admin: 读取授权码失败: %w", err)
			}
			code = strings.TrimSpace(code)
		}
		if code == "" {
			return oauth.Token{}, "", fmt.Errorf("admin: 授权码不能为空")
		}
		token, err := s.oauth.ExchangeCode(ctx, profile, code)
		if err != nil {
			return oauth.Token{}, "", fmt.Errorf("admin: 授权码交换失败: %w", err)
		}
		return token, oauthFlowCode, nil
	default:
		return oauth.Token{}, "", fmt.Errorf("admin: 渠道未声明 device_url 也未声明 authorize_url，无法选择 OAuth 流程")
	}
}

// firstNonEmptyString 返回首个非空字符串。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
