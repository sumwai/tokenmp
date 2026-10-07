// Package auth 实现页面账号体系的注册、登录、会话签发与销毁。
//
// 与 internal/access 的分工：access 是数据面的模型 API 密钥鉴权，语义为
// 「账户 + 商家 + 密钥」并附带 402 额度预检与 429 限额判定；本包是页面会话，
// 两套凭据、两个错误码空间、两条互不相交的路径前缀（/api/v1/ 与 /v1/）。
// 本包不得被数据面复用，反向亦然。
//
// 分层与 internal/me 一致：本包承载业务动作（校验、默认值、令牌签发），
// 数据访问经 Store 接口注入，装配层（internal/gateway）只负责挂载路由。
// 令牌只在生成瞬间存在明文，落库与比对一律 SHA-256 十六进制。
package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 页面契约的固定路径。字段与语义以 docs/openapi-web.yaml 为准，此处只声明常量。
const (
	PathChallenge = "/api/v1/auth/challenge"
	PathSignin    = "/api/v1/auth/signin"
	PathSignup    = "/api/v1/auth/signup"
	PathRefresh   = "/api/v1/auth/refresh"
	PathSession   = "/api/v1/auth/session"
	PathSignout   = "/api/v1/auth/signout"
	PathOTP       = "/api/v1/auth/otp"
	PathReset     = "/api/v1/auth/reset"
	PathPassword  = "/api/v1/auth/password" //nolint:gosec // G101：这是 URL 路径，不是凭据
	PathErase     = "/api/v1/auth/erase"
	PathProviders = "/api/v1/auth/providers"
	// PathOAuthPrefix 是第三方登录子树：{provider} 与 {provider}/exchange 两段。
	PathOAuthPrefix = "/api/v1/auth/oauth/"
	// PathPrefix 是整个页面认证面的子树前缀，装配层按它挂载。
	PathPrefix = "/api/v1/auth/"
)

// 业务错误 → 页面信封 code 的映射依据。handler 按 errors.Is 分支。
var (
	// ErrInvalidCredentials 统一承载「账号不存在 / 密码错误 / 账号停用」：
	// 拆开返回会把「这个账号是否存在」暴露给未认证调用方。
	ErrInvalidCredentials = errors.New("auth: 凭据错误")
	// ErrChallengeExpired 表示一次性公钥已失效（过期或已使用），客户端应重取后重试。
	ErrChallengeExpired = errors.New("auth: 挑战密钥已失效")
	// ErrSignupDisabled 表示注册入口在服务端配置中关闭。
	ErrSignupDisabled = errors.New("auth: 注册入口已关闭")
	// ErrUnauthorized 表示会话缺失、失效或已过期。
	ErrUnauthorized = errors.New("auth: 未登录")
	// ErrInvalidParams 表示请求参数缺失或格式非法。
	ErrInvalidParams = errors.New("auth: 参数非法")
	// ErrRateLimited 表示按来源的频率限制已触发。
	ErrRateLimited = errors.New("auth: 触发频率限制")
)

// Store 是页面认证依赖的数据访问面。
//
// 只列本包真正用到的动作；行类型由 internal/store 定义，本包不重复声明。
type Store interface {
	WebUserByLogin(ctx context.Context, login string) (*store.WebUser, error)
	WebUserByEmail(ctx context.Context, email string) (*store.WebUser, error)
	WebInsertUser(ctx context.Context, u store.WebUser) (uint64, error)
	WebInsertSession(ctx context.Context, sess store.WebSession) (uint64, error)
	WebSessionByAccess(ctx context.Context, hash string) (*store.WebSessionWithUser, error)
	WebSessionByRefresh(ctx context.Context, hash string) (*store.WebSessionWithUser, error)
	WebSessionByPrevRefresh(ctx context.Context, hash string) (*store.WebSessionWithUser, error)
	WebRotateSession(ctx context.Context, id uint64, expectRefresh, accessHash, refreshHash string,
		accessExpiresAt, refreshExpiresAt time.Time) (bool, error)
	WebRevokeSession(ctx context.Context, id uint64) error
	WebRevokeUserSessions(ctx context.Context, userID uint64) error
	WebRevokeOtherSessions(ctx context.Context, userID, keepSessionID uint64) error
	WebIdentityProviders(ctx context.Context, userID uint64) ([]string, error)
	WebUserByID(ctx context.Context, id uint64) (*store.WebUser, error)
	WebIdentityByProvider(ctx context.Context, provider, subject string) (*store.WebIdentity, error)
	WebInsertIdentity(ctx context.Context, id store.WebIdentity) error
	WebInsertOTP(ctx context.Context, email, purpose, codeHash string, expiresAt time.Time) error
	WebConsumeOTP(ctx context.Context, email, purpose, codeHash string, now time.Time) (bool, error)
	WebUpdatePassword(ctx context.Context, userID uint64, passwordHash string) error
	WebEraseUser(ctx context.Context, userID uint64) error
}

// Options 是装配期配置；零值字段回落到默认值。
type Options struct {
	// SignupDisabled 关闭注册入口；零值为启用 —— 开关拼错或漏传时失败方向是
	// 「入口可开」而不是「注册静默禁用」，配置侧的显式关闭才生效。
	SignupDisabled bool
	// TrustProxy 为真时按 X-Forwarded-For 首段取来源地址用于限频，
	// 仅在确有反代的部署打开：直连暴露时该头可被客户端伪造。
	TrustProxy bool
	// AccessTTL、RefreshTTL 是访问令牌与刷新令牌的寿命；非正取默认值。
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	// ChallengeTTL 是一次性公钥的寿命；非正取默认值。
	ChallengeTTL time.Duration
	// Now 取当前时刻；为 nil 时取系统时钟。
	Now func() time.Time
	// Logger 记录安全事件（刷新令牌重放等）；nil 时不记录。
	Logger *slog.Logger
	// Mailer 是验证码投递端口；nil 表示邮件通道未配置，
	// 发送入口一律回 500（不因账号是否存在而分化）。
	Mailer Mailer
	// OAuthProviders 是已配置的第三方登录提供方；空列表时 providers 返回空。
	OAuthProviders []OAuthProvider
}

const (
	defaultAccessTTL    = 2 * time.Hour
	defaultRefreshTTL   = 30 * 24 * time.Hour
	defaultChallengeTTL = 5 * time.Minute
)

// statusActive 是账号可登录状态，与 0006 迁移的 status 列取值一致。
const statusActive = "active"

// roleMember 是注册与第三方建号的默认角色；管理角色经其它通道授予。
const roleMember = "member"

// normalize 把零值选项折算成默认值，返回生效配置。
func (o Options) normalize() Options {
	if o.AccessTTL <= 0 {
		o.AccessTTL = defaultAccessTTL
	}
	if o.RefreshTTL <= 0 {
		o.RefreshTTL = defaultRefreshTTL
	}
	if o.ChallengeTTL <= 0 {
		o.ChallengeTTL = defaultChallengeTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}
