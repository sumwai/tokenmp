package gateway

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sumwai/tokenmp/internal/credential"
	"github.com/sumwai/tokenmp/internal/oauth"
)

// 本文件把 OAuth 协议客户端与凭据表写回拼成 credential.OAuthRefresher：
// 续期是「协议交互 + 写回」两步，前者在 internal/oauth，后者在存储层，
// 装配层负责把两者接起来，因此放在这里而不放进任何一侧的能力包。

// credentialSecretWriter 是续期写回所需的存储能力。
type credentialSecretWriter interface {
	UpdateCredentialSecret(ctx context.Context, id uint64, secret []byte) error
}

// oauthCredentialRefresher 用刷新令牌换新访问令牌并写回凭据行。
type oauthCredentialRefresher struct {
	client *oauth.Client
	store  credentialSecretWriter
	logger *slog.Logger
}

// newOAuthCredentialRefresher 构造续期器；缺少存储能力时返回 nil，由调用方按「不续期」处理。
func newOAuthCredentialRefresher(store credentialSecretWriter, logger *slog.Logger) *oauthCredentialRefresher {
	if store == nil {
		return nil
	}
	return &oauthCredentialRefresher{client: oauth.New(oauth.Options{}), store: store, logger: logger}
}

// Refresh 实现 credential.OAuthRefresher。
//
// 成功时把新令牌写回；写回失败不阻断本次请求——新令牌已经拿到，下一次取用会再续期。
// invalid_grant 时把凭据标记为已过期（清空访问令牌、把过期时刻置为 epoch）并原样返回错误，
// 使该凭据在管理面可见且不会再被当作有效凭据注入。
func (f *oauthCredentialRefresher) Refresh(ctx context.Context, in credential.OAuthRefreshInput) (credential.OAuthRefreshOutput, error) {
	token, err := f.client.Refresh(ctx, in.Profile, in.Refresh)
	if err != nil {
		if errors.Is(err, oauth.ErrInvalidGrant) {
			f.markExpired(ctx, in)
		}
		return credential.OAuthRefreshOutput{}, err
	}
	secret, err := credential.BuildOAuthSecret(token.Access, token.Refresh, token.Expires, in.Account)
	if err != nil {
		return credential.OAuthRefreshOutput{}, err
	}
	if err := f.store.UpdateCredentialSecret(ctx, in.CredentialID, secret); err != nil {
		f.warn("OAuth 凭据续期写回失败", in, err)
	}
	return credential.OAuthRefreshOutput{
		Access:  token.Access,
		Refresh: token.Refresh,
		Expires: token.Expires,
	}, nil
}

// markExpired 把凭据写成「刷新令牌已失效」的形态：清空访问令牌、过期时刻置为 epoch。
//
// 保留 refresh 与 account 字段：前者是排障线索，后者是人工辨识凭据的依据。
func (f *oauthCredentialRefresher) markExpired(ctx context.Context, in credential.OAuthRefreshInput) {
	secret, err := credential.BuildOAuthSecret("", in.Refresh, time.Time{}, in.Account)
	if err != nil {
		f.warn("标记 OAuth 凭据过期失败", in, err)
		return
	}
	if err := f.store.UpdateCredentialSecret(ctx, in.CredentialID, secret); err != nil {
		f.warn("标记 OAuth 凭据过期失败", in, err)
	}
}

// warn 记一条不含任何令牌的告警。
func (f *oauthCredentialRefresher) warn(message string, in credential.OAuthRefreshInput, err error) {
	if f.logger == nil {
		return
	}
	f.logger.Warn(message, "credential", in.Name, "error", err.Error())
}
