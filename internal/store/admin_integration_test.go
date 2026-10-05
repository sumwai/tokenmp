//go:build integration

// 管理面全链路的真实 MySQL 验证：建商家 → 渠道 → 凭据 → 模型映射 → 定价 →
// 开户 → 发 key → 充值 → 购买。与迁移验证同属「需要数据库」的一类，
// 由 make check-integration 显式触发，不加入 make check。
package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

func TestAdminFullChainIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过管理面全链路验证", envTestDSN)
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

	svc := admin.New(s)
	now := time.Now().Truncate(time.Second)

	merchantID, err := svc.CreateMerchant(ctx, "partner-int", "集成入驻", store.MerchantKindPartner)
	if err != nil {
		t.Fatalf("建商家失败：%v", err)
	}
	merchants, err := svc.ListMerchants(ctx)
	if err != nil || len(merchants) != 2 {
		t.Fatalf("商家列表应含平台自营与入驻两条，得到 %d 条（err=%v）", len(merchants), err)
	}

	channelID, err := svc.CreateChannel(ctx, admin.ChannelInput{
		MerchantID: merchantID, Name: "ch-int", Type: store.ChannelTypeOpenAIChat,
		CredGroup: "group-int", BaseURL: "https://up.example.com",
	})
	if err != nil {
		t.Fatalf("建渠道失败：%v", err)
	}

	//nolint:gosec // G101：集成测试用的假上游凭据，不是真实凭据。
	if _, err := svc.AddCredential(ctx, admin.CredentialInput{
		MerchantID: merchantID, Group: "group-int", Name: "primary", APIKey: "sk-upstream-int",
	}); err != nil {
		t.Fatalf("写凭据失败：%v", err)
	}
	credentials, err := svc.ListCredentials(ctx)
	if err != nil || len(credentials) != 1 {
		t.Fatalf("凭据列表应有一条，得到 %d（err=%v）", len(credentials), err)
	}
	if credentials[0].Prefix == "sk-upstream-int" {
		t.Errorf("凭据列表不应回显明文：%q", credentials[0].Prefix)
	}
	// 轮换读取路径按分组拿到启用凭据，且 secret 里含 api_key。
	group, err := s.CredentialsByGroup(ctx, "group-int", merchantID)
	if err != nil || len(group) != 1 {
		t.Fatalf("按分组读凭据应有一条，得到 %d（err=%v）", len(group), err)
	}

	if _, err := svc.SetModelMap(ctx, admin.ModelMapInput{
		ChannelID: channelID, Model: "alias", UpstreamModel: "up-model", PriceMultiplier: "1.2",
	}); err != nil {
		t.Fatalf("写模型映射失败：%v", err)
	}
	candidates, err := s.RouteCandidates(ctx, store.ChannelTypeOpenAIChat, "alias", merchantID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("按商家选路应命中一条候选，得到 %d（err=%v）", len(candidates), err)
	}
	if candidates[0].UpstreamModel != "up-model" {
		t.Errorf("候选上游模型 = %q，期望 up-model", candidates[0].UpstreamModel)
	}

	pricing, err := svc.PublishPricing(ctx, admin.PublishPricingInput{
		MerchantID: merchantID, Model: "up-model", EffectiveAt: now,
		Components: []store.PriceComponent{{
			Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
			UnitPrice: "0.27", BasisQty: "1000000",
		}},
	})
	if err != nil {
		t.Fatalf("发布定价失败：%v", err)
	}
	if pricing.Version != 1 {
		t.Errorf("首个定价版本 = %d，期望 1", pricing.Version)
	}
	// 改价：新版本生效，旧版本被置位。
	repriced, err := svc.PublishPricing(ctx, admin.PublishPricingInput{
		MerchantID: merchantID, Model: "up-model", EffectiveAt: now.Add(time.Hour),
		Components: []store.PriceComponent{{
			Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
			UnitPrice: "0.20", BasisQty: "1000000",
		}},
	})
	if err != nil {
		t.Fatalf("改价失败：%v", err)
	}
	if repriced.Version != 2 {
		t.Errorf("改价版本 = %d，期望 2", repriced.Version)
	}
	active, err := s.ActivePricing(ctx, merchantID, "up-model", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("读生效定价失败：%v", err)
	}
	if active.Version != 2 {
		t.Errorf("生效版本 = %d，期望 2", active.Version)
	}

	accountID, err := svc.CreateAccount(ctx, admin.AccountInput{
		Code: "acct-int", Name: "集成账户", DefaultMerchantID: &merchantID,
	})
	if err != nil {
		t.Fatalf("开户失败：%v", err)
	}

	issued, err := svc.IssueKey(ctx, admin.IssueKeyInput{AccountID: accountID, Name: "default"})
	if err != nil {
		t.Fatalf("发 key 失败：%v", err)
	}
	sum := sha256.Sum256([]byte(issued.Plaintext))
	auth, err := s.LookupAPIKey(ctx, hex.EncodeToString(sum[:]), now)
	if err != nil {
		t.Fatalf("签发的 key 应能用于鉴权：%v", err)
	}
	if auth.AccountID != accountID || auth.MerchantID != merchantID {
		t.Errorf("鉴权归属不符：account=%d merchant=%d", auth.AccountID, auth.MerchantID)
	}

	if _, err := svc.CreditBucket(ctx, admin.CreditBucketInput{
		AccountID: accountID, MerchantID: merchantID, Unit: billing.UnitSettleCurrency,
		Amount: "100", Fallback: billing.FallbackReject, Source: billing.SourceRecharge,
	}); err != nil {
		t.Fatalf("充值失败：%v", err)
	}

	productID, err := svc.CreateProduct(ctx, admin.ProductInput{
		MerchantID: merchantID, Name: "100 万 token 包", Unit: billing.UnitSettleToken,
		Qty: "1000000", Price: "10", ValidityDays: 30,
	})
	if err != nil {
		t.Fatalf("上架商品失败：%v", err)
	}
	buy, err := svc.Buy(ctx, admin.BuyInput{AccountID: accountID, ProductID: productID, Qty: "2"})
	if err != nil {
		t.Fatalf("购买失败：%v", err)
	}
	if buy.Total != "2000000" || buy.PricePaid != "20" || buy.BucketID == 0 {
		t.Errorf("购买派生不符：%+v", buy)
	}

	buckets, err := svc.ListBuckets(ctx, accountID)
	if err != nil {
		t.Fatalf("列账本失败：%v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("账户应有充值 + 购买两份账本，得到 %d", len(buckets))
	}
	// 购买生成的账本写入折算率 price / qty = 10 / 1000000；充值账本为 NULL。
	var purchaseBucket, grantBucket *store.BucketRow
	for i := range buckets {
		switch buckets[i].Source {
		case billing.SourcePurchase:
			purchaseBucket = &buckets[i]
		case billing.SourceRecharge:
			grantBucket = &buckets[i]
		}
	}
	if purchaseBucket == nil || grantBucket == nil {
		t.Fatalf("应分别存在购买与充值账本：%+v", buckets)
	}
	if purchaseBucket.UnitRate == nil || !decimalEqual(*purchaseBucket.UnitRate, "0.00001") {
		t.Errorf("购买账本折算率 = %v，期望 0.00001", purchaseBucket.UnitRate)
	}
	if grantBucket.UnitRate != nil {
		t.Errorf("充值账本不应带折算率，得到 %q", *grantBucket.UnitRate)
	}

	// 窗口限额：写定义、按日窗口列已用量、重置后已用量归零、删除。
	if _, err := s.InsertUsage(ctx, store.UsageRow{
		MerchantID: merchantID, AccountID: accountID, ChannelID: channelID, Model: "up-model",
		Usage: map[billing.Metric]int{billing.MetricRequest: 3},
	}); err != nil {
		t.Fatalf("写入用量失败：%v", err)
	}
	quotaID, err := svc.CreateQuota(ctx, admin.QuotaInput{
		Scope: billing.ScopeAccount, ScopeID: accountID, Metric: billing.MetricRequest,
		WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay,
		LimitAmount: "2", Action: billing.ActionReject,
	})
	if err != nil {
		t.Fatalf("写入限额失败：%v", err)
	}
	views, err := svc.ListQuotas(ctx, billing.ScopeAccount, accountID)
	if err != nil || len(views) != 1 {
		t.Fatalf("限额列表应有一条，得到 %d（err=%v）", len(views), err)
	}
	if views[0].Used == nil || !decimalEqual(*views[0].Used, "3") {
		t.Fatalf("当前窗口已用量 = %v，期望 3", views[0].Used)
	}
	// 重置基准推到远期：聚合下界随之前移，已用量归零。远离现在是为了避开
	// 数据库 CURRENT_TIMESTAMP 与测试进程时钟之间可能的时区偏移。
	if _, err := svc.ResetQuota(ctx, admin.ResetQuotaInput{
		QuotaID: quotaID, BaselineAt: now.AddDate(10, 0, 0), Reason: "集成测试重置", Operator: "ops",
	}); err != nil {
		t.Fatalf("重置限额失败：%v", err)
	}
	views, err = svc.ListQuotas(ctx, billing.ScopeAccount, accountID)
	if err != nil {
		t.Fatalf("重置后列限额失败：%v", err)
	}
	if views[0].Used == nil || !decimalEqual(*views[0].Used, "0") {
		t.Errorf("重置后已用量 = %v，期望 0", views[0].Used)
	}
	if err := svc.DeleteQuota(ctx, quotaID); err != nil {
		t.Fatalf("删除限额失败：%v", err)
	}
	if remaining, err := svc.ListQuotas(ctx, billing.ScopeAccount, accountID); err != nil || len(remaining) != 0 {
		t.Errorf("删除后限额列表应为空，得到 %d 条（err=%v）", len(remaining), err)
	}
}
