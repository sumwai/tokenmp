//go:build integration

// 真实 MySQL 的迁移验证。单独用 integration 构建标签圈起来，
// 因为 make check 必须只依赖 go 与 golangci-lint，不能要求一个可用的数据库。
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

// envTestDSN 指向一个可丢弃的库：本测试会先删掉已知表再重建。
const envTestDSN = "TOKENMP_TEST_MYSQL_DSN"

// knownTables 与 migrations/0001_init.sql、0002_billing.sql、0003_account_bucket_unit_rate.sql、
// 0004_billing_usage_api_key.sql 对应，删除顺序无关紧要（全库无外键）。
var knownTables = []string{
	"billing_adjustment",
	"account_quota_event",
	"account_quota",
	"account_purchase",
	"merchant_product",
	"sys_calendar",
	"billing_price_rule",
	"billing_price_component",
	"billing_pricing",
	"billing_usage",
	"account_bucket",
	"account_api_key",
	"account",
	"upstream_model_map",
	"upstream_credential",
	"upstream_channel",
	"merchant",
	"schema_migrations",
}

func TestMigrateCreatesSchemaAndIsIdempotent(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过真实 MySQL 迁移验证", envTestDSN)
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
	// 清理用独立的 context：函数内的 ctx 带超时且被 defer 取消，
	// t.Cleanup 在函数返回后执行，此时它已经失效。
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cleanupCancel()
		dropKnownTables(cleanupCtx, t, s.DB())
	})

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}

	for _, table := range knownTables {
		if !tableExists(ctx, t, s.DB(), table) {
			t.Errorf("迁移后表 %s 不存在", table)
		}
	}

	assertPlatformMerchant(ctx, t, s.DB())
	assertUnitRateColumn(ctx, t, s.DB())
	assertAPIKeyColumn(ctx, t, s.DB())

	// 重复启动的幂等性：第二次迁移不应因表已存在而失败，也不应重复插入种子行。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
	assertMigrationVersionCount(ctx, t, s.DB(), 4)
	assertPlatformMerchantCount(ctx, t, s.DB(), 1)
	// 第二次迁移不应重复加列：列仍存在且可空。
	assertUnitRateColumn(ctx, t, s.DB())
	// 0004 的重复加列 / 加索引同样应当被跳过，重跑后列仍存在。
	assertAPIKeyColumn(ctx, t, s.DB())
}

func dropKnownTables(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range knownTables {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("删除表 %s 失败：%v", table, err)
		}
	}
}

func tableExists(ctx context.Context, t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?",
		table).Scan(&count)
	if err != nil {
		t.Fatalf("查询表 %s 是否存在失败：%v", table, err)
	}
	return count > 0
}

func assertPlatformMerchant(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var kind string
	if err := db.QueryRowContext(ctx, "SELECT kind FROM merchant WHERE id = 1").Scan(&kind); err != nil {
		t.Fatalf("查询平台自营商家失败：%v", err)
	}
	if kind != "platform" {
		t.Errorf("商家 1 的 kind = %q，期望 platform", kind)
	}
}

func assertPlatformMerchantCount(ctx context.Context, t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM merchant WHERE code = 'platform'").Scan(&count); err != nil {
		t.Fatalf("统计平台自营商家失败：%v", err)
	}
	if count != want {
		t.Errorf("平台自营商家行数 = %d，期望 %d", count, want)
	}
}

func assertMigrationVersionCount(ctx context.Context, t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("统计迁移版本失败：%v", err)
	}
	if count != want {
		t.Errorf("schema_migrations 行数 = %d，期望 %d", count, want)
	}
}

// TestMigrateRejectsUnknownChannelType 验证写入口的严格性在有真实驱动时同样成立；
// 读方向的宽容在无数据库的单测里覆盖。
func TestValidateChannelTypeIntegration(t *testing.T) {
	if err := store.ValidateChannelType(store.ChannelTypeFromDB("openai_chat")); err != nil {
		t.Errorf("已知协议不应报错：%v", err)
	}
	if err := store.ValidateChannelType(store.ChannelTypeFromDB("not_a_protocol")); err == nil {
		t.Error("未知协议应当被拒绝")
	}
}

// decimalEqual 按数值比较两个 DECIMAL 文本。数据库会把写入值补到列标度
// （写入 "0.27" 读回 "0.27000000"），字符串相等判断会误报。
func decimalEqual(a, b string) bool {
	ra, okA := new(big.Rat).SetString(a)
	rb, okB := new(big.Rat).SetString(b)
	return okA && okB && ra.Cmp(rb) == 0
}

// assertUnitRateColumn 断言 0003 加上的可空折算率列存在且为 NULL 语义。
func assertUnitRateColumn(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var nullable string
	err := db.QueryRowContext(ctx,
		"SELECT is_nullable FROM information_schema.columns "+
			"WHERE table_schema = DATABASE() AND table_name = 'account_bucket' AND column_name = 'unit_rate'").
		Scan(&nullable)
	if err != nil {
		t.Fatalf("查询 account_bucket.unit_rate 列失败：%v", err)
	}
	if nullable != "YES" {
		t.Errorf("account_bucket.unit_rate 应为可空列，得到 is_nullable=%q", nullable)
	}
}

// assertAPIKeyColumn 断言 0004 加上的 api_key_id 列与 (api_key_id, created_at) 索引存在。
func assertAPIKeyColumn(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var (
		columnType string
		nullable   string
	)
	err := db.QueryRowContext(ctx,
		"SELECT column_type, is_nullable FROM information_schema.columns "+
			"WHERE table_schema = DATABASE() AND table_name = 'billing_usage' AND column_name = 'api_key_id'").
		Scan(&columnType, &nullable)
	if err != nil {
		t.Fatalf("查询 billing_usage.api_key_id 列失败：%v", err)
	}
	if nullable != "NO" {
		t.Errorf("billing_usage.api_key_id 应为非空列，得到 is_nullable=%q", nullable)
	}
	if !strings.HasPrefix(columnType, "bigint") {
		t.Errorf("billing_usage.api_key_id 类型 = %q，期望 bigint 系", columnType)
	}

	var columns int
	err = db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.statistics "+
			"WHERE table_schema = DATABASE() AND table_name = 'billing_usage' AND index_name = 'idx_usage_api_key_time'").
		Scan(&columns)
	if err != nil {
		t.Fatalf("查询 billing_usage.idx_usage_api_key_time 索引失败：%v", err)
	}
	if columns != 2 {
		t.Errorf("idx_usage_api_key_time 列数 = %d，期望 2", columns)
	}
}

// TestBillingStoreRoundTrip 在真实 MySQL 上把 0002 的新表走一遍写读回环。
// 无数据库时不跑；无此验证的 store 方法只能在集成环境里暴露参数顺序、
// DECIMAL / DATETIME 扫描这类只在真实驱动下才暴露的问题。
func TestBillingStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过计费表写读回环", envTestDSN)
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

	now := time.Now().Truncate(time.Second)

	// 定价版本与分量。
	pricingID, err := s.InsertPricing(ctx, store.Pricing{
		MerchantID: 1, Model: "gpt-test", Version: 1, EffectiveAt: now,
	})
	if err != nil {
		t.Fatalf("写入定价失败：%v", err)
	}
	if err := s.InsertPriceComponents(ctx, []store.PriceComponent{
		{
			PricingID:  pricingID,
			Metric:     billing.MetricInputToken,
			UnitSettle: billing.UnitSettleCurrency,
			UnitPrice:  "0.27",
			BasisQty:   "1000000",
		},
		{
			PricingID:  pricingID,
			Metric:     billing.MetricOutputToken,
			UnitSettle: billing.UnitSettleCurrency,
			UnitPrice:  "1.10",
			BasisQty:   "1000000",
			TierFrom:   "1000",
			TierTo:     "5000",
			TierBasis:  "request_input",
		},
	}); err != nil {
		t.Fatalf("写入计价分量失败：%v", err)
	}
	components, err := s.PriceComponents(ctx, pricingID)
	if err != nil {
		t.Fatalf("读取计价分量失败：%v", err)
	}
	if len(components) != 2 {
		t.Fatalf("计价分量行数 = %d，期望 2", len(components))
	}
	if !decimalEqual(components[0].UnitPrice, "0.27") || !decimalEqual(components[1].TierTo, "5000") || components[1].TierBasis != "request_input" {
		t.Errorf("计价分量回读内容不符：%+v", components)
	}

	active, err := s.ActivePricing(ctx, 1, "gpt-test", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("查询生效定价失败：%v", err)
	}
	if active.ID != pricingID || active.Version != 1 {
		t.Errorf("生效定价 = (%d, v%d)，期望 (%d, v1)", active.ID, active.Version, pricingID)
	}

	// 改价 = 置位旧版 + 插新版；置位后旧版不再是 active。
	if err := s.RetireActivePricing(ctx, 1, "gpt-test", now); err != nil {
		t.Fatalf("置位 retired_at 失败：%v", err)
	}
	if _, err := s.ActivePricing(ctx, 1, "gpt-test", now.Add(time.Hour)); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("置位后应查不到 active 定价，得到 %v", err)
	}

	// 规则：增、查、删。
	mask := uint8(1 << 6) // 仅周日
	validFrom := now.Add(-24 * time.Hour)
	ruleID, err := s.InsertPriceRule(ctx, store.PriceRule{
		Scope:       billing.ScopePricing,
		ScopeID:     pricingID,
		Metric:      billing.MetricInputToken,
		Multiplier:  "0.5",
		ValidFrom:   &validFrom,
		TimeFrom:    "22:00:00",
		WeekdayMask: &mask,
		Priority:    200,
	})
	if err != nil {
		t.Fatalf("写入规则失败：%v", err)
	}
	rules, err := s.PriceRulesByScope(ctx, billing.ScopePricing, pricingID)
	if err != nil {
		t.Fatalf("读取规则失败：%v", err)
	}
	if len(rules) != 1 || rules[0].ID != ruleID || rules[0].WeekdayMask == nil || *rules[0].WeekdayMask != mask {
		t.Errorf("规则回读内容不符：%+v", rules)
	}
	if rules[0].TimeFrom != "22:00:00" {
		t.Errorf("规则时段回读不符：%q", rules[0].TimeFrom)
	}
	if rules[0].ValidFrom == nil || !rules[0].ValidFrom.Equal(validFrom) || rules[0].ValidTo != nil {
		t.Errorf("规则有效期回读不符：%+v", rules[0])
	}
	if err := s.DeletePriceRule(ctx, ruleID); err != nil {
		t.Fatalf("删除规则失败：%v", err)
	}
	if rules, err = s.PriceRulesByScope(ctx, billing.ScopePricing, pricingID); err != nil || len(rules) != 0 {
		t.Errorf("删除后规则应为空，得到 %v（err=%v）", rules, err)
	}

	// 日历：批量 upsert 后回读，重复 upsert 覆盖同一日期。
	if err := s.UpsertCalendarDays(ctx, "cn", []store.CalendarDay{
		{Date: "2026-10-01", DayKind: billing.DayKindHoliday},
		{Date: "2026-09-27", DayKind: billing.DayKindMakeupWorkday},
	}); err != nil {
		t.Fatalf("写入日历失败：%v", err)
	}
	if err := s.UpsertCalendarDays(ctx, "cn", []store.CalendarDay{
		{Date: "2026-10-01", DayKind: billing.DayKindWeekend},
	}); err != nil {
		t.Fatalf("覆盖日历失败：%v", err)
	}
	days, err := s.CalendarDays(ctx, "cn", "2026-09-01", "2026-10-31")
	if err != nil {
		t.Fatalf("读取日历失败：%v", err)
	}
	if len(days) != 2 {
		t.Fatalf("日历行数 = %d，期望 2（%+v）", len(days), days)
	}
	if days[0].Date != "2026-09-27" || days[0].DayKind != billing.DayKindMakeupWorkday {
		t.Errorf("日历首行不符：%+v", days[0])
	}
	if days[1].Date != "2026-10-01" || days[1].DayKind != billing.DayKindWeekend {
		t.Errorf("日历覆盖未生效：%+v", days[1])
	}

	// 商品与购买。
	productID, err := s.InsertProduct(ctx, store.Product{
		MerchantID: 1, Name: "10 元 100M", Unit: billing.UnitSettleToken,
		Qty: "100000000", Price: "10", ModelScope: []byte(`["gpt-test"]`), ValidityDays: 30,
	})
	if err != nil {
		t.Fatalf("写入商品失败：%v", err)
	}
	product, err := s.Product(ctx, productID)
	if err != nil {
		t.Fatalf("读取商品失败：%v", err)
	}
	if product.Unit != billing.UnitSettleToken || !decimalEqual(product.Qty, "100000000") || product.ValidityDays != 30 {
		t.Errorf("商品回读内容不符：%+v", product)
	}
	if _, err := s.InsertPurchase(ctx, store.Purchase{
		AccountID: 1, MerchantID: 1, ProductID: productID, Qty: "1", PricePaid: "10", PurchasedAt: now,
	}); err != nil {
		t.Fatalf("写入购买记录失败：%v", err)
	}

	// 限额与重置事件。
	quotaID, err := s.InsertQuota(ctx, store.Quota{
		Scope: billing.ScopeAccount, ScopeID: 1, Metric: billing.MetricRequest,
		WindowKind: billing.WindowKindRolling, Period: billing.Period5h,
		LimitAmount: "1000", Action: billing.ActionReject,
	})
	if err != nil {
		t.Fatalf("写入限额失败：%v", err)
	}
	if _, err := s.InsertQuotaEvent(ctx, store.QuotaEvent{
		QuotaID: quotaID, Event: billing.QuotaEventReset, BaselineAt: now, Reason: "运营重置", Operator: "ops",
	}); err != nil {
		t.Fatalf("写入重置事件失败：%v", err)
	}

	// 调账。
	if _, err := s.InsertAdjustment(ctx, store.Adjustment{
		AccountID: 1, DeltaAmount: "-1.5", Reason: "误扣补偿", Operator: "ops", CreatedAt: now,
	}); err != nil {
		t.Fatalf("写入调账失败：%v", err)
	}

	// 迁移幂等：写完数据后重跑迁移不应失败，也不应重复建表或清数据。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
	assertMigrationVersionCount(ctx, t, s.DB(), 4)
}
