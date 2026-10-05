package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是账本、商品与购买的管理动作。

// defaultBucketPriority 是账本扣减顺序的默认优先级，与列的 DEFAULT 100 一致。
const defaultBucketPriority = 100

// CreditBucketInput 是发放账本的输入。
type CreditBucketInput struct {
	AccountID  uint64
	MerchantID uint64
	Unit       billing.UnitSettle
	Amount     string
	Fallback   billing.Fallback
	Source     billing.Source
	ExpiresAt  *time.Time
	Priority   int
}

// CreditBucket 发放一笔账本存量（运营发放 / 充值）。
//
// amount 同时作为 total 与 remaining 的初值：发放即到账，不存在「总额与可用额不同」
// 的中间态。fallback 与 source 必填，调用方不从别处推断。
func (s *Service) CreditBucket(ctx context.Context, in CreditBucketInput) (uint64, error) {
	if err := requireID("账本 account", in.AccountID); err != nil {
		return 0, err
	}
	if err := requireID("账本 merchant", in.MerchantID); err != nil {
		return 0, err
	}
	if err := billing.ValidateUnitSettle(in.Unit); err != nil {
		return 0, err
	}
	if err := requirePositiveDecimal("账本 amount", in.Amount); err != nil {
		return 0, err
	}
	if err := billing.ValidateFallback(in.Fallback); err != nil {
		return 0, err
	}
	if err := billing.ValidateSource(in.Source); err != nil {
		return 0, err
	}
	if in.Priority == 0 {
		in.Priority = defaultBucketPriority
	}
	return s.store.InsertBucket(ctx, store.BucketRow{
		AccountID:  in.AccountID,
		MerchantID: in.MerchantID,
		Unit:       in.Unit,
		Total:      in.Amount,
		Remaining:  in.Amount,
		ExpiresAt:  in.ExpiresAt,
		Fallback:   in.Fallback,
		Source:     in.Source,
		Priority:   in.Priority,
	})
}

// ListBuckets 列出账本；accountID 为 0 时列出全部。
func (s *Service) ListBuckets(ctx context.Context, accountID uint64) ([]store.BucketRow, error) {
	return s.store.ListBuckets(ctx, accountID)
}

// ProductInput 是上架商品档位的输入。
type ProductInput struct {
	MerchantID   uint64
	Name         string
	Unit         billing.UnitSettle
	Qty          string
	Price        string
	ModelScope   json.RawMessage
	ValidityDays int
}

// CreateProduct 上架一个商品档位。
func (s *Service) CreateProduct(ctx context.Context, in ProductInput) (uint64, error) {
	if err := requireID("商品 merchant", in.MerchantID); err != nil {
		return 0, err
	}
	if err := requireString("商品 name", in.Name); err != nil {
		return 0, err
	}
	if err := billing.ValidateUnitSettle(in.Unit); err != nil {
		return 0, err
	}
	if err := requirePositiveDecimal("商品 qty", in.Qty); err != nil {
		return 0, err
	}
	if err := requirePositiveDecimal("商品 price", in.Price); err != nil {
		return 0, err
	}
	if in.ValidityDays < 0 {
		return 0, fmt.Errorf("admin: 商品 validity 不能为负数，得到 %d", in.ValidityDays)
	}
	if len(in.ModelScope) > 0 {
		var scope []string
		if err := json.Unmarshal(in.ModelScope, &scope); err != nil {
			return 0, fmt.Errorf("admin: 商品 model-scope 必须是字符串数组的 JSON")
		}
	}
	return s.store.InsertProduct(ctx, store.Product{
		MerchantID:   in.MerchantID,
		Name:         in.Name,
		Unit:         in.Unit,
		Qty:          in.Qty,
		Price:        in.Price,
		ModelScope:   in.ModelScope,
		ValidityDays: in.ValidityDays,
	})
}

// ListProducts 列出全部商品档位。
func (s *Service) ListProducts(ctx context.Context) ([]store.Product, error) {
	return s.store.ListProducts(ctx)
}

// BuyInput 是购买商品的输入。
type BuyInput struct {
	AccountID uint64
	ProductID uint64
	Qty       string
	Fallback  billing.Fallback
}

// BuyResult 是一次购买的产物。
//
// 刻意没有 unit_rate 字段：account_bucket.unit_rate 依赖 issue #32 落地，
// 本 issue 不建列、不写该值，购买锁定的折算率因此暂缺，输出只给总量与有效期。
type BuyResult struct {
	PurchaseID uint64             `json:"purchase_id"`
	BucketID   uint64             `json:"bucket_id"`
	AccountID  uint64             `json:"account_id"`
	MerchantID uint64             `json:"merchant_id"`
	ProductID  uint64             `json:"product_id"`
	Qty        string             `json:"qty"`
	PricePaid  string             `json:"price_paid"`
	Unit       billing.UnitSettle `json:"unit"`
	Total      string             `json:"total"`
	ExpiresAt  *time.Time         `json:"expires_at"`
}

// Buy 按商品档位生成购买记录与派生的账本。
//
// 派生口径：total = 商品每份数量 × 购买份数，price_paid = 商品单价 × 购买份数，
// 有效期 = 购买时刻 + 商品有效天数（0 表示不过期）。
//
// 折算率（unit_rate）锁定依赖 issue #32 的 account_bucket 新列；在 #32 落地前
// 购买生成的账本不带折算率，跨单位折算按 token 存量直接扣减。
func (s *Service) Buy(ctx context.Context, in BuyInput) (*BuyResult, error) {
	if err := requireID("购买 account", in.AccountID); err != nil {
		return nil, err
	}
	if err := requireID("购买 product", in.ProductID); err != nil {
		return nil, err
	}
	if err := requirePositiveDecimal("购买 qty", in.Qty); err != nil {
		return nil, err
	}
	fallback := in.Fallback
	if fallback == "" {
		fallback = billing.FallbackChargeBalance
	}
	if err := billing.ValidateFallback(fallback); err != nil {
		return nil, err
	}

	product, err := s.store.Product(ctx, in.ProductID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("admin: 商品 %d 不存在", in.ProductID)
		}
		return nil, err
	}
	qty, err := parseDecimal(in.Qty)
	if err != nil {
		return nil, fmt.Errorf("admin: 购买 qty 不是合法数字 %q", in.Qty)
	}
	productQty, err := parseDecimal(product.Qty)
	if err != nil {
		return nil, fmt.Errorf("admin: 商品 %d 的数量无法解析: %w", product.ID, err)
	}
	productPrice, err := parseDecimal(product.Price)
	if err != nil {
		return nil, fmt.Errorf("admin: 商品 %d 的价格无法解析: %w", product.ID, err)
	}
	total := productQty.Mul(qty)
	pricePaid := productPrice.Mul(qty)

	now := s.now()
	var expiresAt *time.Time
	if product.ValidityDays > 0 {
		expires := now.AddDate(0, 0, product.ValidityDays)
		expiresAt = &expires
	}

	purchaseID, bucketID, err := s.store.CreatePurchaseAndBucket(ctx,
		store.Purchase{
			AccountID:   in.AccountID,
			MerchantID:  product.MerchantID,
			ProductID:   product.ID,
			Qty:         in.Qty,
			PricePaid:   pricePaid.String(),
			PurchasedAt: now,
		},
		store.BucketRow{
			AccountID:  in.AccountID,
			MerchantID: product.MerchantID,
			Unit:       product.Unit,
			Total:      total.String(),
			Remaining:  total.String(),
			ExpiresAt:  expiresAt,
			Fallback:   fallback,
			Source:     billing.SourcePurchase,
			Priority:   defaultBucketPriority,
		})
	if err != nil {
		return nil, err
	}
	return &BuyResult{
		PurchaseID: purchaseID,
		BucketID:   bucketID,
		AccountID:  in.AccountID,
		MerchantID: product.MerchantID,
		ProductID:  product.ID,
		Qty:        in.Qty,
		PricePaid:  pricePaid.String(),
		Unit:       product.Unit,
		Total:      total.String(),
		ExpiresAt:  expiresAt,
	}, nil
}

// ListPurchases 列出购买记录；accountID 为 0 时列出全部。
func (s *Service) ListPurchases(ctx context.Context, accountID uint64) ([]store.Purchase, error) {
	return s.store.ListPurchases(ctx, accountID)
}

// requirePositiveDecimal 校验字符串是正数。
func requirePositiveDecimal(field, value string) error {
	if err := requireDecimal(field, value); err != nil {
		return err
	}
	parsed, err := parseDecimal(value)
	if err != nil {
		return fmt.Errorf("admin: %s 不是合法数字 %q", field, value)
	}
	if !parsed.GreaterThan(decimal.Zero) {
		return fmt.Errorf("admin: %s 必须为正数，得到 %s: %w", field, value, errNotPositive)
	}
	return nil
}
