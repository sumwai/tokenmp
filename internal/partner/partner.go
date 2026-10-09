// Package partner 实现 /api/v1/partner/* 商家域端点：上游账号（渠道与凭据）的自助
// 登记、启停与列表，以及本商家名下渠道的用量聚合。
//
// 与用户级端点（internal/user）的分工：两者都以会话为作用域来源，但这里的归属多一跳
// —— 登录主体 → 名下商家（merchant.owner_user_id），端点因此不接受任何商家参数，
// 越权面只剩「会话」一个入口。非本商家的资源一律 404，不以 403 区分存在性。
//
// 写路径直接落在存储层：登记动作的校验、默认值与配置派生都在本包，与管理面
// （internal/admin）取同一组列默认值与同一份凭据 secret 口径，页面登记出来的行与
// CLI 登记出来的行同形；读与启停也不复用管理面 —— 管理面的读是全表、写只按 id，
// 没有商家作用域，用在这里等于把越权面开到页面上。
//
// 凭据明文只在创建响应出现一次，列表只给出前缀；其余商家的任何标识都不进响应。
package partner

import (
	"context"

	"github.com/sumwai/tokenmp/internal/store"
)

// 商家域端点的固定路径。取值与 docs/openapi-web.yaml 一致，改动须同步契约。
const (
	// PathPrefix 是商家域子树的根路径。
	PathPrefix = "/api/v1/partner"
	// ChannelsPath 是上游渠道集合的固定路径。
	ChannelsPath = PathPrefix + "/channels"
	// CredentialsPath 是上游凭据集合的固定路径。
	CredentialsPath = PathPrefix + "/credentials"
	// UsageStatsPath 是名下用量聚合的固定路径。
	UsageStatsPath = PathPrefix + "/usage/stats"
)

// Sessions 解析页面会话令牌，由 internal/auth 的会话实现满足。
type Sessions interface {
	// SessionSubject 解析访问令牌并返回登录主体 id 与身份集合（叠加后的全部身份）；
	// 令牌无效、会话已撤销或已过期时返回错误。
	SessionSubject(ctx context.Context, accessToken string) (userID uint64, roles []string, err error)
}

// Store 是商家域依赖的数据面：每一处读写都按商家收敛，作用域只能来自会话。
type Store interface {
	// MerchantByOwner 按登录主体查名下商家；无绑定时返回 sql.ErrNoRows。
	MerchantByOwner(ctx context.Context, userID uint64) (*store.Merchant, error)
	// ChannelsByMerchant 按商家分页列出渠道，返回当页行与满足条件的总数。
	ChannelsByMerchant(ctx context.Context, merchantID uint64, enabled *bool, limit, offset int) ([]store.Channel, int, error)
	// CredentialsByMerchant 按商家分页列出凭据，返回当页行与满足条件的总数。
	CredentialsByMerchant(ctx context.Context, merchantID uint64, limit, offset int) ([]store.CredentialRow, int, error)
	// InsertChannel 写一条渠道；同名冲突返回满足 errors.Is(err, store.ErrConflict) 的错误。
	InsertChannel(ctx context.Context, c store.Channel) (uint64, error)
	// InsertCredential 写一条凭据；secret 是已序列化的 JSON。
	InsertCredential(ctx context.Context, c store.CredentialRow) (uint64, error)
	// SetChannelEnabledForMerchant 置位本商家某条渠道的启用标志；未命中本商家的行返回 false。
	SetChannelEnabledForMerchant(ctx context.Context, id, merchantID uint64, enabled bool) (bool, error)
	// SetCredentialEnabledForMerchant 置位本商家某条凭据的启用标志；未命中本商家的行返回 false。
	SetCredentialEnabledForMerchant(ctx context.Context, id, merchantID uint64, enabled bool) (bool, error)
	// MerchantUsageStats 按维度聚合本商家名下的用量。
	MerchantUsageStats(ctx context.Context, q store.MerchantUsageStatsQuery) ([]store.UsageStatsItem, error)
}

// Options 是装配参数；两个字段都必填。
type Options struct {
	Sessions Sessions
	Store    Store
}

// Handler 是商家域端点的 HTTP 入口，按路径分发到各动作。
type Handler struct {
	sessions Sessions
	store    Store
}

// NewHandler 构造商家域端点处理器。
func NewHandler(opts Options) *Handler {
	return &Handler{sessions: opts.Sessions, store: opts.Store}
}
