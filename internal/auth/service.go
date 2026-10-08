package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是页面认证的业务动作：挑战签发、注册登录、会话读写与刷新轮换。
//
// 每个方法的错误只返回 auth 包的哨兵（或 store.ErrConflict、sql.ErrNoRows 的既有语义），
// HTTP 状态与业务码的映射集中在 handler，业务层不感知信封。

// rateLimitPerMinute 是认证端点每来源每分钟的调用上限。
//
// 按代价标定：challenge 每次要 RSA keygen，signin/signup 每次要 bcrypt 比对，
// 两者都是毫秒级 CPU；30 次对真人绰绰有余，对脚本足以把 keygen 成本压到无害。
const rateLimitPerMinute = 30

// Service 承载页面认证业务；经 Store 访问数据库，经 challengeSet 持有内存密钥。
type Service struct {
	store          Store
	opts           Options
	challenges     *challengeSet
	limiter        *rateLimiter
	logger         *slog.Logger
	mailer         Mailer
	oauthProviders []OAuthProvider
	oauthStates    *stateSet
	oauthClient    *http.Client
}

// New 构造页面认证服务。logger 为 nil 时丢弃日志。
func New(st Store, opts Options) *Service {
	opts = opts.normalize()
	// 频率限制按认证端点的实际代价标定：challenge 每次要 RSA keygen，
	// signin/signup 每次要 bcrypt 比对，两者都是毫秒级 CPU。
	// 一分钟 30 次对真人绰绰有余，对脚本足以把 keygen 成本压到无害。
	return &Service{
		store:          st,
		opts:           opts,
		challenges:     newChallengeSet(opts.ChallengeTTL, opts.Now),
		limiter:        newRateLimiter(time.Minute, rateLimitPerMinute, opts.Now),
		logger:         opts.Logger,
		mailer:         opts.Mailer,
		oauthProviders: opts.OAuthProviders,
		oauthStates:    newStateSet(oauthStateTTL, opts.Now),
		oauthClient:    &http.Client{Timeout: oauthHTTPTimeout},
	}
}

// sessionTokens 是登录与注册的响应数据，字段与契约 SessionTokens 对齐。
type sessionTokens struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    int         `json:"expires_in"`
	User         sessionUser `json:"user"`
}

// accessTokenData 是刷新端点的响应数据，字段与契约 AccessTokenData 对齐。
type accessTokenData struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// sessionUser 是会话端点与登录响应共用的身份数据，字段与契约 SessionUser 对齐。
//
// 不含商家标识：商家是结算内部概念，账户的结算归属由 account.default_merchant_id
// 决定，不对登录主体暴露。页面作用域由账户归属（account.owner_user_id）推导。
type sessionUser struct {
	ID         uint64   `json:"id"`
	Username   string   `json:"username"`
	Role       string   `json:"role"`
	Identities []string `json:"identities"`
}

// Challenge 签发一次性加密公钥。
func (s *Service) Challenge(_ context.Context, addr, fingerprint string) (*publicKeyData, error) {
	if !s.limiter.allow("challenge:" + addr) {
		return nil, ErrRateLimited
	}
	if err := validateFingerprint(fingerprint); err != nil {
		return nil, err
	}
	return s.challenges.put(fingerprint)
}

// Signin 校验账号与密码并签发会话。
//
// 三类失败（账号不存在、密码错误、账号停用）折叠为 ErrInvalidCredentials，
// 由 handler 映射为同一个 401 与同一句文案。
func (s *Service) Signin(ctx context.Context, addr, login, passwordCipher, fingerprint string) (*sessionTokens, error) {
	if !s.limiter.allow("signin:" + addr) {
		return nil, ErrRateLimited
	}
	plain, err := s.decryptPassword(fingerprint, passwordCipher)
	if err != nil {
		return nil, err
	}
	defer clear(plain)

	user, err := s.store.WebUserByLogin(ctx, strings.TrimSpace(login))
	if err != nil || !userActive(user) || user.PasswordHash == "" {
		return nil, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), plain); err != nil {
		return nil, ErrInvalidCredentials
	}
	return s.issueSession(ctx, user)
}

// Signup 创建账号并在同一事务内开户；注册入口关闭时返回 ErrSignupDisabled。
//
// 邮箱与用户名的唯一键冲突统一折叠为 store.ErrConflict：两者对前端是同一种
// 「这个标识已被占用」，区分来源只会泄漏注册探测信息。账号与账户在存储层
// 同一事务内写入，失败时不留下能登录却没有账户可操作的账号。
func (s *Service) Signup(ctx context.Context, addr, email, username, passwordCipher, fingerprint string) (*sessionTokens, error) {
	if !s.limiter.allow("signup:" + addr) {
		return nil, ErrRateLimited
	}
	if s.opts.SignupDisabled {
		return nil, ErrSignupDisabled
	}
	email = strings.TrimSpace(email)
	username = strings.TrimSpace(username)
	if err := validateIdentity(email, username); err != nil {
		return nil, err
	}
	plain, err := s.decryptPassword(fingerprint, passwordCipher)
	if err != nil {
		return nil, err
	}
	defer clear(plain)

	hash, err := bcrypt.GenerateFromPassword(plain, bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	id, _, err := s.store.WebInsertUserWithAccount(ctx, store.WebUser{
		Email:        email,
		Username:     username,
		PasswordHash: string(hash),
		Role:         store.RoleMember,
		Status:       statusActive,
	}, store.Account{
		Name:            username,
		PriceMultiplier: defaultAccountMultiplier,
		Status:          statusActive,
	})
	if err != nil {
		return nil, err
	}
	return s.issueSession(ctx, &store.WebUser{
		ID:           id,
		Email:        email,
		Username:     username,
		PasswordHash: string(hash),
		Role:         store.RoleMember,
		Status:       statusActive,
	})
}

// SessionUserID 解析页面会话令牌并返回登录主体 id；令牌无效、会话已撤销或过期
// 一律返回 ErrUnauthorized。
//
// 供同属页面面的业务包按会话推导作用域：页面端点不接受账户 id 参数，归属只能
// 来自令牌。它只回 id，不回身份细节——业务包不需要、也不该拿到会话全貌。
func (s *Service) SessionUserID(ctx context.Context, accessToken string) (uint64, error) {
	row, err := s.store.WebSessionByAccess(ctx, hashToken(accessToken))
	if err != nil || !sessionUsable(row, s.opts.Now()) {
		return 0, ErrUnauthorized
	}
	return row.User.ID, nil
}

// SessionByAccess 按访问令牌读当前身份；未登录、已撤销或已过期一律 ErrUnauthorized。
func (s *Service) SessionByAccess(ctx context.Context, accessToken string) (*sessionUser, error) {
	row, err := s.store.WebSessionByAccess(ctx, hashToken(accessToken))
	if err != nil || !sessionUsable(row, s.opts.Now()) {
		return nil, ErrUnauthorized
	}
	identities, err := s.identities(ctx, &row.User)
	if err != nil {
		return nil, err
	}
	return &sessionUser{
		ID:         row.User.ID,
		Username:   row.User.Username,
		Role:       row.User.Role,
		Identities: identities,
	}, nil
}

// Refresh 用刷新令牌换发访问令牌，轮换在一次 CAS 更新里完成。
//
// 重放检测：请求的令牌若命中 prev_refresh_hash，说明它已被轮换过 ——
// 只有失窃副本才会拿旧值回来，因此撤销整个会话而不是只拒绝本次请求。
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*accessTokenData, error) {
	if !s.limiter.allow("refresh:" + hashToken(refreshToken)) {
		return nil, ErrRateLimited
	}
	expect := hashToken(refreshToken)
	row, err := s.store.WebSessionByRefresh(ctx, expect)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s.rejectReplayed(ctx, expect)
		}
		return nil, err
	}
	now := s.opts.Now()
	if row.Session.RevokedAt != nil || now.After(row.Session.RefreshExpiresAt) {
		return nil, ErrUnauthorized
	}

	access, err := newTokenPair()
	if err != nil {
		return nil, err
	}
	refresh, err := newTokenPair()
	if err != nil {
		return nil, err
	}
	ok, err := s.store.WebRotateSession(ctx, row.Session.ID, expect, access.hash, refresh.hash,
		now.Add(s.opts.AccessTTL), now.Add(s.opts.RefreshTTL))
	if err != nil {
		return nil, err
	}
	if !ok {
		// CAS 未命中：并发刷新赢了另一边，本次请求的令牌已作废。
		return nil, ErrUnauthorized
	}
	return &accessTokenData{AccessToken: access.plain, ExpiresIn: int(s.opts.AccessTTL / time.Second)}, nil
}

// Signout 撤销当前会话；行存在即视为成功，重复登出是幂等操作。
func (s *Service) Signout(ctx context.Context, accessToken string) error {
	row, err := s.store.WebSessionByAccess(ctx, hashToken(accessToken))
	if err != nil {
		return ErrUnauthorized
	}
	return s.store.WebRevokeSession(ctx, row.Session.ID)
}

// rejectReplayed 判定刷新令牌是否命中已轮换的旧值；命中则撤销会话并记安全事件。
func (s *Service) rejectReplayed(ctx context.Context, expect string) (*accessTokenData, error) {
	prev, err := s.store.WebSessionByPrevRefresh(ctx, expect)
	if err != nil {
		return nil, ErrUnauthorized
	}
	if err := s.store.WebRevokeSession(ctx, prev.Session.ID); err != nil {
		return nil, err
	}
	if s.logger != nil {
		s.logger.Warn("页面刷新令牌重放，会话已撤销",
			"session_id", prev.Session.ID, "user_id", prev.Session.UserID)
	}
	return nil, ErrUnauthorized
}

// decryptPassword 取一次性私钥并解出密码明文；私钥取出即销毁。
func (s *Service) decryptPassword(fingerprint, passwordCipher string) ([]byte, error) {
	if err := validateFingerprint(fingerprint); err != nil {
		return nil, err
	}
	privateKey, err := s.challenges.take(fingerprint)
	if err != nil {
		return nil, err
	}
	return decryptWith(privateKey, passwordCipher)
}

// issueSession 签发新会话并返回登录响应数据。
func (s *Service) issueSession(ctx context.Context, user *store.WebUser) (*sessionTokens, error) {
	access, err := newTokenPair()
	if err != nil {
		return nil, err
	}
	refresh, err := newTokenPair()
	if err != nil {
		return nil, err
	}
	now := s.opts.Now()
	if _, insertErr := s.store.WebInsertSession(ctx, store.WebSession{
		UserID:           user.ID,
		AccessHash:       access.hash,
		RefreshHash:      refresh.hash,
		AccessExpiresAt:  now.Add(s.opts.AccessTTL),
		RefreshExpiresAt: now.Add(s.opts.RefreshTTL),
	}); insertErr != nil {
		return nil, insertErr
	}
	identities, err := s.identities(ctx, user)
	if err != nil {
		return nil, err
	}
	return &sessionTokens{
		AccessToken:  access.plain,
		RefreshToken: refresh.plain,
		ExpiresIn:    int(s.opts.AccessTTL / time.Second),
		User: sessionUser{
			ID:         user.ID,
			Username:   user.Username,
			Role:       user.Role,
			Identities: identities,
		},
	}, nil
}

// identities 汇总账号可用的登录方式：设过密码含 "password"，其余来自绑定表。
//
// 顺序固定为 password 在前、提供方按字典序，保证同一账号的返回稳定可比对。
func (s *Service) identities(ctx context.Context, user *store.WebUser) ([]string, error) {
	providers, err := s.store.WebIdentityProviders(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(providers)+1)
	if user.PasswordHash != "" {
		out = append(out, "password")
	}
	out = append(out, providers...)
	return out, nil
}

// sessionUsable 判定会话是否可用于访问令牌：未撤销、未过期、账号仍启用。
func sessionUsable(row *store.WebSessionWithUser, now time.Time) bool {
	return row != nil &&
		row.Session.RevokedAt == nil &&
		now.Before(row.Session.AccessExpiresAt) &&
		userActive(&row.User)
}

// userActive 判定账号是否处于可登录状态。
func userActive(user *store.WebUser) bool {
	return user != nil && user.Status == statusActive
}

// validateFingerprint 校验指纹格式；与契约的长度约束一致。
func validateFingerprint(fingerprint string) error {
	fingerprint = strings.TrimSpace(fingerprint)
	if len(fingerprint) < 8 || len(fingerprint) > 128 {
		return ErrInvalidParams
	}
	return nil
}

// validateIdentity 校验邮箱与用户名。
//
// 邮箱用标准库解析器做基础形态校验，再要求包含域名点号：只挡明显笔误，
// 不替下游服务商验证可达性 —— 那是发信环节的职责。
func validateIdentity(email, username string) error {
	if err := validateEmail(email); err != nil {
		return err
	}
	if len(username) < 2 || len(username) > 64 || strings.TrimSpace(username) != username {
		return ErrInvalidParams
	}
	for _, r := range username {
		if r <= ' ' || r == 0x7f {
			return ErrInvalidParams
		}
	}
	return nil
}
