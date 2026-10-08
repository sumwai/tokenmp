// Package user 实现 /api/v1/user/* 用户级业务端点。
//
// 与认证面（internal/auth）的分工：认证面回答「谁登录」，本包回答「这个登录主体
// 能看到什么」。作用域由会话推导，端点不接受账户 id 参数 —— 归属只能来自令牌，
// 否则凭空多出一个越权面。
//
// 信封与 Bearer 解析复用 internal/webapi，账户摘要复用 internal/me 的口径，
// 使页面展示与数据面判定不会各算一套。
package user

import (
	"context"

	"github.com/sumwai/tokenmp/internal/me"
	"github.com/sumwai/tokenmp/internal/store"
)

// 用户级端点的固定路径。取值与 docs/openapi-web.yaml 一致，改动须同步契约。
const (
	// PathPrefix 是用户级端点子树前缀，装配层按它挂载。
	PathPrefix = "/api/v1/user/"
	// AccountPath 是账户概览的固定路径。
	AccountPath = "/api/v1/user/account"
	// KeysPath 是密钥集合的固定路径。
	KeysPath = "/api/v1/user/keys"
)

// Sessions 解析页面会话令牌，由 internal/auth 的会话实现满足。
type Sessions interface {
	// SessionUserID 解析访问令牌并返回登录主体 id；令牌无效时返回错误。
	SessionUserID(ctx context.Context, accessToken string) (uint64, error)
}

// Store 是用户级端点依赖的数据面。
type Store interface {
	// AccountByOwner 按登录主体查所属账户；无归属时返回 sql.ErrNoRows。
	AccountByOwner(ctx context.Context, userID uint64) (*store.Account, error)
	// InsertAPIKey 写入一条客户端密钥；只接受哈希与前缀，明文不落库。
	InsertAPIKey(ctx context.Context, k store.APIKey) (uint64, error)
	// ListAPIKeysByAccount 按账户分页列出密钥，返回当页行与满足条件的总数。
	ListAPIKeysByAccount(ctx context.Context, accountID uint64, enabled *bool, limit, offset int) ([]store.APIKey, int, error)
	// APIKeyByID 按主键查密钥；无匹配时返回 sql.ErrNoRows。
	APIKeyByID(ctx context.Context, id uint64) (*store.APIKey, error)
	// SetAPIKeyEnabled 置位密钥启用标志；吊销即置 false。
	SetAPIKeyEnabled(ctx context.Context, id uint64, enabled bool) error
}

// SummaryReader 是账户摘要的读取入口，由 internal/me 的实现满足。
type SummaryReader interface {
	Summary(ctx context.Context, accountID, apiKeyID uint64, recentLimit int) (*me.Summary, error)
}

// Options 是装配参数；三个字段都必填。
type Options struct {
	Sessions Sessions
	Store    Store
	Summary  SummaryReader
}

// Handler 是用户级端点的 HTTP 入口，按路径分发到各动作。
type Handler struct {
	sessions Sessions
	store    Store
	summary  SummaryReader
}

// NewHandler 构造用户级端点处理器。
func NewHandler(opts Options) *Handler {
	return &Handler{sessions: opts.Sessions, store: opts.Store, summary: opts.Summary}
}
