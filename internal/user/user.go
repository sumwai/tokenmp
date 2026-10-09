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
	"time"

	"github.com/sumwai/tokenmp/internal/me"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
)

// 用户级端点的固定路径。取值与 docs/openapi-web.yaml 一致，改动须同步契约。
const (
	// PathPrefix 是用户级端点子树前缀，装配层按它挂载。
	PathPrefix = "/api/v1/user/"
	// ConsolePath 是控制台清单的固定路径。
	ConsolePath = "/api/v1/user/console"
	// AccountPath 是账户概览的固定路径。
	AccountPath = "/api/v1/user/account"
	// KeysPath 是密钥集合的固定路径。
	KeysPath = "/api/v1/user/keys"
	// UsagePath 是用量流水的固定路径。
	UsagePath = "/api/v1/user/usage"
	// ModelsPath 是模型目录的固定路径。
	ModelsPath = "/api/v1/user/models"
	// RequestsPath 是请求记录集合的固定路径。
	RequestsPath = "/api/v1/user/requests"
	// ProductsPath 是商品目录的固定路径。
	ProductsPath = "/api/v1/user/products"
	// OrdersPath 是订单集合的固定路径：GET 列出、POST 下单。
	OrdersPath = "/api/v1/user/orders"
)

// itemsKey 是列表数据的字段名。
//
// 契约把每个列表端点的 data 声明为 {items: [...]}，取值写成一个常量，
// 六个列表端点不会各自拼一个字面量后慢慢漂开。
const itemsKey = "items"

// Sessions 解析页面会话令牌，由 internal/auth 的会话实现满足。
type Sessions interface {
	// SessionSubject 解析访问令牌并返回登录主体 id 与身份集合；令牌无效、会话已撤销
	// 或已过期时返回错误。
	//
	// 回身份集合是控制台清单端点的需要：身份是叠加的，清单按各身份取能力并集。
	// 作用域推导只需要 id，两者共用一次会话读取，同一请求不会读两遍会话。
	SessionSubject(ctx context.Context, accessToken string) (userID uint64, roles []string, err error)
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
	// ListAccountUsage 按账户分页列出用量流水，返回当页行与满足条件的总数。
	ListAccountUsage(ctx context.Context, f store.AccountUsageFilter) ([]store.AccountUsageRow, int, error)
	// AccountUsageStats 按维度聚合账户的用量合计。
	AccountUsageStats(ctx context.Context, q store.UsageStatsQuery) ([]store.UsageStatsItem, error)
	// ListAccountModels 列出账户可调用的模型及其协议方言。
	ListAccountModels(ctx context.Context, accountID uint64) ([]store.AccountModel, error)
	// ListRequestLogs 按账户分页列出请求记录，返回当页行与满足条件的总数。
	ListRequestLogs(ctx context.Context, f store.RequestLogFilter) ([]store.RequestLogRow, int, error)
	// RequestLogByRequestID 读一条属于某账户的请求记录；无匹配时返回 sql.ErrNoRows。
	RequestLogByRequestID(ctx context.Context, accountID uint64, requestID string) (*store.RequestLogRow, error)
	// RequestAttempts 按尝试序号读一条请求的全部尝试。
	RequestAttempts(ctx context.Context, requestID string) ([]store.RequestAttempt, error)
	// RequestStats 按维度聚合账户的请求计数。
	RequestStats(ctx context.Context, q store.RequestStatsQuery) ([]store.RequestStatsItem, error)
	// AccountBuckets 读账户的全部账本行，供额度预检使用；与数据面 402 预检共用一份口径。
	AccountBuckets(ctx context.Context, accountID uint64) ([]settlement.Bucket, error)
	// ListProducts 列出可购买的商品档位。
	ListProducts(ctx context.Context) ([]store.Product, error)
	// Product 按 id 查商品档位；无匹配时返回 sql.ErrNoRows。
	Product(ctx context.Context, id uint64) (*store.Product, error)
	// CreateOrder 幂等地下单：写购买记录与派生账本，重复提交返回首次创建的订单。
	CreateOrder(ctx context.Context, p store.Purchase, b store.BucketRow) (store.OrderWrite, error)
	// ListOrdersByAccount 按账户分页列出订单，返回当页行与满足条件的总数。
	ListOrdersByAccount(ctx context.Context, accountID uint64, limit, offset int) ([]store.OrderRow, int, error)
}

// SummaryReader 是账户摘要的读取入口，由 internal/me 的实现满足。
type SummaryReader interface {
	Summary(ctx context.Context, accountID, apiKeyID uint64, recentLimit int) (*me.Summary, error)
}

// Options 是装配参数；前三个字段必填。
type Options struct {
	Sessions Sessions
	Store    Store
	Summary  SummaryReader
	// Now 是取当前时刻的函数，供额度预检与购买派生的有效期使用；nil 即系统时钟。
	Now func() time.Time
}

// Handler 是用户级端点的 HTTP 入口，按路径分发到各动作。
type Handler struct {
	sessions Sessions
	store    Store
	summary  SummaryReader
	now      func() time.Time
}

// NewHandler 构造用户级端点处理器。
func NewHandler(opts Options) *Handler {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Handler{sessions: opts.Sessions, store: opts.Store, summary: opts.Summary, now: now}
}
