// Package admin 是管理面子命令的业务层：商家、渠道、凭据、模型映射、账户、密钥、
// 账本、商品、购买、定价、规则、日历、流水与调账。
//
// 分层口径与 internal/settlement 一致：本包承载业务动作（校验、默认值、派生计算），
// 数据访问经 Store 接口注入；cmd 只做参数解析与输出渲染，不拼 SQL、不做业务分支。
//
// 枚举校验复用既有白名单：渠道协议用 store.ValidateChannelType，结算单位 / 账本处置 /
// 账本来路 / 计费指标 / 规则范围 / 日期性质用 internal/billing 的 Validate*。
// 商家类型在 store 侧收敛（merchant.kind 没有对应的 billing 枚举），由 store 校验。
package admin

import (
	"context"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

// Store 是管理面依赖的数据访问面。
//
// 只列管理面真正用到的动作；读路径返回的是库表事实行，派生与脱敏在本包完成。
type Store interface {
	// 商家。
	InsertMerchant(ctx context.Context, m store.Merchant) (uint64, error)
	ListMerchants(ctx context.Context) ([]store.Merchant, error)
	SetMerchantStatus(ctx context.Context, id uint64, status string) error

	// 渠道。
	InsertChannel(ctx context.Context, c store.Channel) (uint64, error)
	ListChannels(ctx context.Context) ([]store.Channel, error)
	SetChannelEnabled(ctx context.Context, id uint64, enabled bool) error

	// 上游凭据。
	InsertCredential(ctx context.Context, c store.CredentialRow) (uint64, error)
	ListCredentials(ctx context.Context) ([]store.CredentialRow, error)
	SetCredentialEnabled(ctx context.Context, id uint64, enabled bool) error

	// 渠道模型映射。
	UpsertModelMap(ctx context.Context, m store.ModelMap) (uint64, error)
	ListModelMaps(ctx context.Context) ([]store.ModelMap, error)
	SetModelMapEnabled(ctx context.Context, id uint64, enabled bool) error

	// 账户。
	InsertAccount(ctx context.Context, a store.Account) (uint64, error)
	ListAccounts(ctx context.Context) ([]store.Account, error)
	SetAccountStatus(ctx context.Context, id uint64, status string) error
	SetAccountMultiplier(ctx context.Context, id uint64, multiplier string) error
	SetAccountMerchant(ctx context.Context, id, merchantID uint64) error

	// 客户端密钥。
	InsertAPIKey(ctx context.Context, k store.APIKey) (uint64, error)
	ListAPIKeys(ctx context.Context) ([]store.APIKey, error)
	SetAPIKeyEnabled(ctx context.Context, id uint64, enabled bool) error

	// 账本。
	InsertBucket(ctx context.Context, b store.BucketRow) (uint64, error)
	ListBuckets(ctx context.Context, accountID uint64) ([]store.BucketRow, error)

	// 商品与购买。
	InsertProduct(ctx context.Context, p store.Product) (uint64, error)
	ListProducts(ctx context.Context) ([]store.Product, error)
	Product(ctx context.Context, id uint64) (*store.Product, error)
	CreatePurchaseAndBucket(ctx context.Context, p store.Purchase, b store.BucketRow) (uint64, uint64, error)
	ListPurchases(ctx context.Context, accountID uint64) ([]store.Purchase, error)

	// 定价。
	PublishPricing(ctx context.Context, merchantID uint64, model string, effectiveAt time.Time, components []store.PriceComponent) (*store.Pricing, error)
	ListPricing(ctx context.Context, merchantID uint64, model string) ([]store.Pricing, error)

	// 条件倍率规则。
	InsertPriceRule(ctx context.Context, r store.PriceRule) (uint64, error)
	PriceRulesByScope(ctx context.Context, scope billing.Scope, scopeID uint64) ([]store.PriceRule, error)
	DeletePriceRule(ctx context.Context, id uint64) error

	// 日历。
	UpsertCalendarDays(ctx context.Context, calendar string, days []store.CalendarDay) error
	ListCalendarDays(ctx context.Context, calendar string) ([]store.CalendarDay, error)

	// 用量流水。
	ListUsage(ctx context.Context, accountID uint64, since time.Time) ([]store.UsageListRow, error)

	// 调账。
	InsertAdjustment(ctx context.Context, a store.Adjustment) (uint64, error)
	ListAdjustments(ctx context.Context, accountID uint64) ([]store.Adjustment, error)
}

// Service 是管理面业务层。
type Service struct {
	store Store
	now   func() time.Time
}

// Option 调整 Service 的可注入依赖。
type Option func(*Service)

// WithClock 注入取当前时刻的函数，供测试固定时间。
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// New 构造管理面服务；未注入时钟时取系统时钟。
func New(st Store, opts ...Option) *Service {
	s := &Service{store: st, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}
