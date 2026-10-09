//go:build integration

// 幂等下单与订单列表的真实 MySQL 验证：0010 迁移的幂等键列与唯一索引、
// 重复提交只产生一笔订单、订单读取的档位口径与偏移分页。
package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

func TestOrderIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过订单集成验证", envTestDSN)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("关闭连接失败：%v", err)
		}
	})

	dropKnownTables(ctx, t, s.DB())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cleanupCancel()
		dropKnownTables(cleanupCtx, t, s.DB())
	})

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	const accountID = uint64(42)
	productID, err := s.InsertProduct(ctx, store.Product{
		MerchantID: 1, Name: "10 元 100M token", Unit: billing.UnitSettleToken,
		Qty: "100000000", Price: "10", ValidityDays: 30,
	})
	if err != nil {
		t.Fatalf("上架商品失败：%v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	key := "order-key-0001"
	first, err := s.CreateOrder(ctx, store.Purchase{
		AccountID: accountID, MerchantID: 1, ProductID: productID,
		Qty: "2", PricePaid: "20", PurchasedAt: now, IdempotencyKey: key,
	}, bucketRow(accountID, "200000000"))
	if err != nil {
		t.Fatalf("首次下单失败：%v", err)
	}
	if first.Replayed {
		t.Error("首次下单不应报幂等命中")
	}
	if first.Purchase.ID == 0 || first.BucketID == 0 {
		t.Fatalf("首次下单应返回订单与账本 id：%+v", first)
	}

	// 重复提交同一幂等键、且带着不同的份数：应返回首次订单，不新增行、不重复发存量。
	replay, err := s.CreateOrder(ctx, store.Purchase{
		AccountID: accountID, MerchantID: 1, ProductID: productID,
		Qty: "9", PricePaid: "90", PurchasedAt: now, IdempotencyKey: key,
	}, bucketRow(accountID, "900000000"))
	if err != nil {
		t.Fatalf("重复下单失败：%v", err)
	}
	if !replay.Replayed {
		t.Error("同键重复提交应报幂等命中")
	}
	if replay.Purchase.ID != first.Purchase.ID {
		t.Errorf("幂等命中应返回首次订单 id %d，得到 %d", first.Purchase.ID, replay.Purchase.ID)
	}
	// 存储层的购买行按列标度读回（"2.00000000"），比较按数值解析；
	// 契约面的字符串形态由 ListOrdersByAccount 断言。
	if !decimalEqual(replay.Purchase.Qty, "2") || !decimalEqual(replay.Purchase.PricePaid, "20") {
		t.Errorf("幂等命中应返回首次订单口径，得到 qty=%q price=%q", replay.Purchase.Qty, replay.Purchase.PricePaid)
	}
	if replay.BucketID != 0 {
		t.Errorf("幂等命中不应再发存量，得到 bucket id %d", replay.BucketID)
	}
	if got := countRows(ctx, t, s.DB(), "account_purchase"); got != 1 {
		t.Errorf("account_purchase 行数 = %d，期望 1", got)
	}
	if got := countRows(ctx, t, s.DB(), "account_bucket"); got != 1 {
		t.Errorf("account_bucket 行数 = %d，期望 1", got)
	}

	// 幂等键只在账户内唯一：另一个账户用同一个键仍能下单。
	other, err := s.CreateOrder(ctx, store.Purchase{
		AccountID: 43, MerchantID: 1, ProductID: productID,
		Qty: "1", PricePaid: "10", PurchasedAt: now, IdempotencyKey: key,
	}, bucketRow(43, "100000000"))
	if err != nil {
		t.Fatalf("另一账户同键下单失败：%v", err)
	}
	if other.Replayed {
		t.Error("不同账户的同名幂等键不应互相顶掉")
	}

	// 订单读取带上档位口径，且金额、数量与折算率保持十进制字符串。
	orders, total, err := s.ListOrdersByAccount(ctx, accountID, 10, 0)
	if err != nil {
		t.Fatalf("订单列表失败：%v", err)
	}
	if total != 1 || len(orders) != 1 {
		t.Fatalf("订单数与总数 = %d / %d，期望 1 / 1", len(orders), total)
	}
	got := orders[0]
	if got.ProductName != "10 元 100M token" || got.Unit != billing.UnitSettleToken {
		t.Errorf("订单档位口径 = %q / %q", got.ProductName, got.Unit)
	}
	// 契约面的十进制字符串不带列标度：尾零只由展示层按结算单位补。
	if got.Qty != "2" || got.PricePaid != "20" {
		t.Errorf("订单口径 = qty %q / price %q，期望 2 / 20", got.Qty, got.PricePaid)
	}
	if got.Total != "200000000" {
		t.Errorf("派生存量 = %q，期望 200000000", got.Total)
	}
	if !decimalEqual(got.UnitRate, "0.0000001") {
		t.Errorf("折算率 = %q，期望 0.0000001", got.UnitRate)
	}
	// 另一个账户的订单不出现。
	if other.Purchase.AccountID != 43 {
		t.Fatalf("另一账户订单归属错误：%d", other.Purchase.AccountID)
	}

	// 分页：请求第 2 页（偏移 1）时当页为空但总数仍是 1。
	page2, total2, err := s.ListOrdersByAccount(ctx, accountID, 10, 1)
	if err != nil {
		t.Fatalf("第二页查询失败：%v", err)
	}
	if len(page2) != 0 || total2 != 1 {
		t.Errorf("第二页 = %d 条 / 总数 %d，期望 0 / 1", len(page2), total2)
	}
}

// bucketRow 造一条派生账本行，口径与 internal/admin 的购买一致。
func bucketRow(accountID uint64, amount string) store.BucketRow {
	rate := "0.0000001"
	return store.BucketRow{
		AccountID: accountID, MerchantID: 1, Unit: billing.UnitSettleToken,
		Total: amount, Remaining: amount, Fallback: billing.FallbackChargeBalance,
		Source: billing.SourcePurchase, Priority: store.DefaultBucketPriority, UnitRate: &rate,
	}
}

// countRows 执行一条返回单列计数的查询。
