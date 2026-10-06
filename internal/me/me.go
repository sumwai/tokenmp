// Package me 实现用户自助查询端点：账户摘要、可用包存量、限额窗口与最近流水。
//
// 独立成包而不是塞进 gateway：汇总口径（可用包的过滤与分组、限额窗口的复用、
// 流水的裁剪与折算）是纯规则，单独成包后可用内存替身直接单测；gateway 只负责把
// 数据面同一鉴权中间件与这里的 HTTP 处理器接到 mux 上。
//
// 本包只读：不改表、不写流水，也不返回任何凭据。每次请求直查数据库，不做缓存 ——
// 余额与限额是「查完立刻用来做决定」的数据，缓存带来的一致性解释成本高于一次主键查询。
// 后续如需缓存，入口在 Service.Summary：按 accountID 缓存整份快照，失效时刻取
// 最短的包到期时间与限额重置时间，两者都在本包的结构里可取到。
package me

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/quota"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
)

// Store 是自助查询依赖的数据面。
//
// 限额定义与窗口聚合复用 quota.Repo：与鉴权链上的判定是同一份实现，
// 已用量的口径不会在展示与判定之间漂移。
type Store interface {
	// Account 按 id 读账户，用于回账户 id 与 code。
	Account(ctx context.Context, id uint64) (*store.Account, error)
	// AccountBuckets 读账户的全部账本行（不加锁），与额度预检共用。
	AccountBuckets(ctx context.Context, accountID uint64) ([]settlement.Bucket, error)
	// RecentUsage 按 id 倒序读账户最近若干条流水。
	RecentUsage(ctx context.Context, accountID uint64, limit int) ([]store.UsageListRow, error)
	// quota.Repo 提供限额定义读取与窗口用量聚合。
	quota.Repo
}

// Service 组装一份账户摘要。
type Service struct {
	store Store
	now   func() time.Time
}

// New 构造自助查询服务；now 为 nil 时取系统时钟。
func New(st Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: st, now: now}
}

// Summary 是一次自助查询的结果。
//
// 字段只增不改语义：加入新维度时追加字段，不改既有字段的含义与类型。
type Summary struct {
	Account AccountView  `json:"account"`
	Buckets []BucketView `json:"buckets"`
	Quotas  []QuotaView  `json:"quotas"`
	Recent  []RecentView `json:"recent"`
}

// AccountView 是账户本身对外可见的部分：主键与业务编码。
//
// 刻意不含默认商家、倍率与状态：它们属运营口径，接入方不需要也不应据此判断行为。
type AccountView struct {
	ID   uint64 `json:"id"`
	Code string `json:"code"`
}

// BucketView 是一条可用存量包。
//
// 同 unit 的多个包各自成条、按 unit 归拢在一起，而不是合并成一条合计：
// 每条包的 fallback 与到期时间不同，合并会掩盖「哪部分何时到期」，
// 而到期时间正是自助查询要看的信息。
type BucketView struct {
	Unit      billing.UnitSettle `json:"unit"`
	Remaining string             `json:"remaining"`
	Fallback  billing.Fallback   `json:"fallback"`
	ExpiresAt *time.Time         `json:"expires_at"`
}

// QuotaView 是一条限额行在当前窗口的展示口径。
//
// ResetsAt 为 nil 表示该窗口不会自然重置（total），而不是「重置时间未知」。
type QuotaView struct {
	Scope      billing.Scope      `json:"scope"`
	Metric     billing.Metric     `json:"metric"`
	WindowKind billing.WindowKind `json:"window_kind"`
	Period     billing.Period     `json:"period"`
	Used       string             `json:"used"`
	Limit      string             `json:"limit"`
	Action     billing.Action     `json:"action"`
	ResetsAt   *time.Time         `json:"resets_at"`
}

// RecentView 是一条流水摘要。
type RecentView struct {
	Model         string    `json:"model"`
	CreatedAt     time.Time `json:"created_at"`
	ChargedAmount string    `json:"charged_amount"`
}

// Summary 汇出账户的可用包、限额窗口与最近流水。
//
// recentLimit 由 HTTP 层按查询参数封顶后传入；0 表示不需要流水。
func (s *Service) Summary(ctx context.Context, accountID, apiKeyID uint64, recentLimit int) (*Summary, error) {
	if accountID == 0 {
		return nil, errors.New("me: account_id 不能为 0")
	}
	now := s.now()
	account, err := s.store.Account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	buckets, err := s.store.AccountBuckets(ctx, accountID)
	if err != nil {
		return nil, err
	}
	quotas, err := s.quotaViews(ctx, accountID, apiKeyID, now)
	if err != nil {
		return nil, err
	}
	recent, err := s.recentViews(ctx, accountID, recentLimit)
	if err != nil {
		return nil, err
	}
	return &Summary{
		Account: AccountView{ID: account.ID, Code: account.Code},
		Buckets: availableBuckets(buckets, now),
		Quotas:  quotas,
		Recent:  recent,
	}, nil
}

// availableBuckets 过滤出未过期且仍有余量的包，并按 unit 归拢。
//
// 组装顺序沿用账本的扣减序（不过期在前、到期早的在前），只在最后按 unit 做一次
// 稳定排序，使同单位的包相邻且组内仍是扣减序。与扣减序一致，客户端读到的
// 第一条就是下一次结算会先扣的那条。
func availableBuckets(buckets []settlement.Bucket, now time.Time) []BucketView {
	views := make([]BucketView, 0, len(buckets))
	for _, bucket := range buckets {
		if !bucket.Remaining.IsPositive() {
			continue
		}
		// ExpiresAt 为 nil 表示不过期；等于 now 视为已过期，与扣减时「到期时刻之后不再可用」一致。
		if bucket.ExpiresAt != nil && !bucket.ExpiresAt.After(now) {
			continue
		}
		views = append(views, BucketView{
			Unit:      bucket.Unit,
			Remaining: bucket.Remaining.String(),
			Fallback:  bucket.Fallback,
			ExpiresAt: bucket.ExpiresAt,
		})
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].Unit < views[j].Unit })
	return views
}

// scopeRef 是一个限额维度与其实体 id。
type scopeRef struct {
	scope billing.Scope
	id    uint64
}

// quotaViews 列出账户维度与本次密钥维度的限额，并附当前窗口的已用量与重置时间。
//
// 与判定路径同口径：窗口起点用 quota.WindowStart（经 quota.Used），
// 已用量用 quota.Repo 的聚合查询，重置时间用 quota.ResetAt。这里不重算窗口边界。
//
// 聚合失败直接报错回 500，不跳过该行：转发路径上的宽容是为「限额执行不该成为
// 转发单点」，而本端点的全部价值就是这些数值，静默漏显示会误导对余量的判断。
// 窗口组合不可判定的历史脏行仍跳过：那不是查询失败，是这条限额本就不生效。
func (s *Service) quotaViews(ctx context.Context, accountID, apiKeyID uint64, now time.Time) ([]QuotaView, error) {
	refs := []scopeRef{{scope: billing.ScopeAccount, id: accountID}}
	if apiKeyID != 0 {
		refs = append(refs, scopeRef{scope: billing.ScopeAPIKey, id: apiKeyID})
	}
	views := make([]QuotaView, 0)
	for _, ref := range refs {
		limits, err := s.store.Quotas(ctx, ref.scope, ref.id)
		if err != nil {
			return nil, err
		}
		for _, limit := range limits {
			used, ok, err := quota.Used(ctx, s.store, limit, now)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			view := QuotaView{
				Scope:      ref.scope,
				Metric:     limit.Metric,
				WindowKind: limit.WindowKind,
				Period:     limit.Period,
				Used:       used.String(),
				Limit:      limit.LimitAmount.String(),
				Action:     limit.Action,
			}
			if resetsAt, ok := quota.ResetAt(limit.WindowKind, limit.Period, now); ok {
				view.ResetsAt = &resetsAt
			}
			views = append(views, view)
		}
	}
	return views, nil
}

// recentViews 读最近若干条流水并折算出付费金额。
//
// 付费金额 = gross_amount × multiplier：两者都是流水里的既有事实，相乘即本次
// 实际应扣量（折算到其它单位的部分由 settlement 明细记载，不在这里展开）。
// 行序保持存储层的「最近在前」，客户端按序读到的第一条就是最后一次调用。
func (s *Service) recentViews(ctx context.Context, accountID uint64, limit int) ([]RecentView, error) {
	rows, err := s.store.RecentUsage(ctx, accountID, limit)
	if err != nil {
		return nil, err
	}
	views := make([]RecentView, 0, len(rows))
	for _, row := range rows {
		gross, err := decimal.NewFromString(row.GrossAmount)
		if err != nil {
			return nil, fmt.Errorf("me: 无法解析流水 gross_amount %q: %w", row.GrossAmount, err)
		}
		multiplier, err := decimal.NewFromString(row.Multiplier)
		if err != nil {
			return nil, fmt.Errorf("me: 无法解析流水 multiplier %q: %w", row.Multiplier, err)
		}
		views = append(views, RecentView{
			Model:         row.Model,
			CreatedAt:     row.CreatedAt,
			ChargedAmount: gross.Mul(multiplier).String(),
		})
	}
	return views, nil
}
