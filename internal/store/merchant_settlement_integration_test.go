//go:build integration

// 商家分佣对账的真实 MySQL 验证：settle_info 的读写往返，以及账期聚合的区间语义
// （左闭右开、按商家隔离）。与迁移验证同属「需要数据库」的一类，由
// make check-integration 显式触发。
package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 账期边界用固定的历史时刻而不是 now 前后：聚合谓词是纯时间比较，
// 固定取值让断言与测试进程时钟无关。
var (
	settleWindowFrom = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	settleWindowTo   = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
)

func TestMerchantSettlementIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过分佣对账验证", envTestDSN)
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

	merchantID, err := s.InsertMerchant(ctx, store.Merchant{
		Code: "settle-int", Name: "集成分账商家", Kind: store.MerchantKindPartner,
	})
	if err != nil {
		t.Fatalf("写商家失败：%v", err)
	}
	otherID, err := s.InsertMerchant(ctx, store.Merchant{
		Code: "settle-int-other", Name: "另一个商家", Kind: store.MerchantKindPartner,
	})
	if err != nil {
		t.Fatalf("写商家失败：%v", err)
	}

	// 新建商家的 settle_info 是 NULL，读回应为空（由上层按默认口径解释）。
	raw, err := s.MerchantSettleInfo(ctx, merchantID)
	if err != nil {
		t.Fatalf("读口径失败：%v", err)
	}
	if len(raw) != 0 {
		t.Errorf("新建商家的口径应为 NULL，得到 %q", raw)
	}

	want := `{"commission_rate":"0.1000","period":"month"}`
	if err := s.SetMerchantSettleInfo(ctx, merchantID, []byte(want)); err != nil {
		t.Fatalf("写口径失败：%v", err)
	}
	raw, err = s.MerchantSettleInfo(ctx, merchantID)
	if err != nil {
		t.Fatalf("回读口径失败：%v", err)
	}
	if !jsonEqual(raw, []byte(want)) {
		t.Errorf("口径回读 = %s，期望语义等价于 %s", raw, want)
	}

	if _, err := s.MerchantSettleInfo(ctx, 1<<40); err == nil {
		t.Error("不存在的商家应当报错")
	}

	// 卖出：期内两笔（含起点那一刻，左闭），期外与另一商家各一笔。
	seedPurchase(ctx, t, s, merchantID, "60", settleWindowFrom)
	seedPurchase(ctx, t, s, merchantID, "40", settleWindowFrom.AddDate(0, 0, 15))
	seedPurchase(ctx, t, s, merchantID, "1000", settleWindowFrom.AddDate(0, 0, -1))
	seedPurchase(ctx, t, s, otherID, "999", settleWindowFrom.AddDate(0, 0, 15))
	// 终点那一刻（右开）不计入本期。
	seedPurchase(ctx, t, s, merchantID, "500", settleWindowTo)

	// 上游成本：期内两条流水（含起点），期外一条，另一商家一条。
	seedUsage(ctx, t, s, merchantID, "12.5", settleWindowFrom)
	seedUsage(ctx, t, s, merchantID, "7.5", settleWindowFrom.AddDate(0, 0, 20))
	seedUsage(ctx, t, s, merchantID, "1000", settleWindowFrom.AddDate(0, 0, -2))
	seedUsage(ctx, t, s, otherID, "50", settleWindowFrom.AddDate(0, 0, 20))

	facts, err := s.MerchantSettlementFacts(ctx, merchantID, settleWindowFrom, settleWindowTo)
	if err != nil {
		t.Fatalf("聚合对账事实失败：%v", err)
	}
	if facts.MerchantID != merchantID || facts.Trades != 2 {
		t.Errorf("笔数与归属不符：%+v", facts)
	}
	if !decimalEqual(facts.GrossSales, "100") {
		t.Errorf("卖出总额 = %s，期望 100", facts.GrossSales)
	}
	if !decimalEqual(facts.UpstreamCost, "20") {
		t.Errorf("上游成本 = %s，期望 20", facts.UpstreamCost)
	}

	// 按商家隔离：另一个商家只统计自己的两笔。
	other, err := s.MerchantSettlementFacts(ctx, otherID, settleWindowFrom, settleWindowTo)
	if err != nil {
		t.Fatalf("聚合另一个商家失败：%v", err)
	}
	if other.Trades != 1 || !decimalEqual(other.GrossSales, "999") || !decimalEqual(other.UpstreamCost, "50") {
		t.Errorf("另一个商家的对账事实不符：%+v", other)
	}

	// 空账期：没有任何流水的区间返回零而不是错误。
	empty, err := s.MerchantSettlementFacts(ctx, merchantID,
		settleWindowFrom.AddDate(-1, 0, 0), settleWindowFrom.AddDate(-1, 0, 0).AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("聚合空账期失败：%v", err)
	}
	if empty.Trades != 0 || !decimalEqual(empty.GrossSales, "0") || !decimalEqual(empty.UpstreamCost, "0") {
		t.Errorf("空账期应为零值事实：%+v", empty)
	}

	if _, err := s.MerchantSettlementFacts(ctx, 0, settleWindowFrom, settleWindowTo); err == nil {
		t.Error("merchant_id 为 0 应当被拒绝")
	}
}

// seedPurchase 直插一条购买记录：价格与购买时刻都要精确控制，走业务入口会引入
// 商品与账本的前置条件，与本用例要验的聚合谓词无关。
func seedPurchase(ctx context.Context, t *testing.T, s *store.Store, merchantID uint64, price string, at time.Time) {
	t.Helper()
	_, err := s.DB().ExecContext(ctx,
		"INSERT INTO account_purchase (account_id, merchant_id, product_id, qty, price_paid, purchased_at) VALUES (?, ?, ?, ?, ?, ?)",
		1, merchantID, 1, "1", price, at)
	if err != nil {
		t.Fatalf("写购买记录失败：%v", err)
	}
}

// seedUsage 直插一条用量流水：InsertUsage 把结算列写死为未结算形态（gross_amount=0），
// 只落用量不落金额，而本用例要验的正是金额聚合。
func seedUsage(ctx context.Context, t *testing.T, s *store.Store, merchantID uint64, gross string, at time.Time) {
	t.Helper()
	_, err := s.DB().ExecContext(ctx,
		"INSERT INTO billing_usage (merchant_id, account_id, channel_id, model, `usage`, gross_amount, multiplier, created_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		merchantID, 1, 1, "up-model", `{"request": 1}`, gross, "1", at)
	if err != nil {
		t.Fatalf("写用量流水失败：%v", err)
	}
}
