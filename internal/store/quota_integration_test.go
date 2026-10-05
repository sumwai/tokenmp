//go:build integration

// 窗口限额聚合的真实 MySQL 验证：JSON_EXTRACT 求和、窗口下界与 reset 基准截断。
// 与迁移验证同属「需要数据库」的一类，由 make check-integration 显式触发。
package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/quota"
	"github.com/sumwai/tokenmp/internal/store"
)

// TestQuotaUsageIntegration 覆盖按 usage JSON 键求和、scope 维度映射、窗口下界与
// reset 基准截断。
func TestQuotaUsageIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过窗口限额聚合验证", envTestDSN)
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

	const (
		accountID = 1
		channelID = 10
	)
	now := time.Now()
	// 用远离现在的边界值而不是 now 前后数秒：created_at 由数据库 CURRENT_TIMESTAMP
	// 生成，与测试进程时钟之间可能有时区偏移，只有足够大的裕量才能稳定断言包含 / 排除。
	farPast := now.AddDate(-100, 0, 0)
	farFuture := now.AddDate(100, 0, 0)

	quotaID, err := s.InsertQuota(ctx, store.Quota{
		Scope: billing.ScopeAccount, ScopeID: accountID, Metric: billing.MetricInputToken,
		WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay,
		LimitAmount: "1000", Action: billing.ActionReject,
	})
	if err != nil {
		t.Fatalf("写入限额失败：%v", err)
	}

	limits, err := s.Quotas(ctx, billing.ScopeAccount, accountID)
	if err != nil {
		t.Fatalf("读取限额失败：%v", err)
	}
	if len(limits) != 1 {
		t.Fatalf("限额行数 = %d，期望 1", len(limits))
	}
	if limits[0].ID != quotaID || limits[0].Metric != billing.MetricInputToken ||
		!decimalEqual(limits[0].LimitAmount.String(), "1000") {
		t.Errorf("限额回读内容不符：%+v", limits[0])
	}
	if other, err := s.Quotas(ctx, billing.ScopeAccount, accountID+1); err != nil || len(other) != 0 {
		t.Errorf("按其它账户过滤应返回空，得到 %d 条（err=%v）", len(other), err)
	}

	for _, usage := range []map[billing.Metric]int{
		{billing.MetricInputToken: 100, billing.MetricOutputToken: 5, billing.MetricRequest: 1},
		{billing.MetricInputToken: 50, billing.MetricRequest: 1},
	} {
		if _, err := s.InsertUsage(ctx, store.UsageRow{
			MerchantID: 1, AccountID: accountID, ChannelID: channelID, Model: "up-model", Usage: usage,
		}); err != nil {
			t.Fatalf("写入用量失败：%v", err)
		}
	}

	// api_key 维度的流水：key 7 用 100，key 8 用 50，另有一条 api_key_id=0 的
	// 「无 key 维度」行。后者的用量不得计入任何 key 限额，聚合 7 / 8 时被排除。
	const (
		keyAQuotaID = 7
		keyBID      = 8
		keylessUse  = 999
	)
	keyQuotaID, err := s.InsertQuota(ctx, store.Quota{
		Scope: billing.ScopeAPIKey, ScopeID: keyAQuotaID, Metric: billing.MetricInputToken,
		WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay,
		LimitAmount: "1000", Action: billing.ActionReject,
	})
	if err != nil {
		t.Fatalf("写入 api_key 限额失败：%v", err)
	}
	keyAccountID := uint64(accountID + 1)
	// 用另一条渠道：channel 维度的既有断言按 channel_id 聚合，混入新行会让那条断言偏离。
	keyChannelID := uint64(channelID + 1)
	for _, row := range []store.UsageRow{
		{MerchantID: 1, AccountID: keyAccountID, ChannelID: keyChannelID, APIKeyID: keyAQuotaID, Model: "up-model",
			Usage: map[billing.Metric]int{billing.MetricInputToken: 100}},
		{MerchantID: 1, AccountID: keyAccountID, ChannelID: keyChannelID, APIKeyID: keyBID, Model: "up-model",
			Usage: map[billing.Metric]int{billing.MetricInputToken: 50}},
		{MerchantID: 1, AccountID: keyAccountID, ChannelID: keyChannelID, APIKeyID: 0, Model: "up-model",
			Usage: map[billing.Metric]int{billing.MetricInputToken: keylessUse}},
	} {
		if _, err := s.InsertUsage(ctx, row); err != nil {
			t.Fatalf("写入 api_key 维度用量失败：%v", err)
		}
	}

	// metric 聚合键：只累加 input_token，输出 token 不计入。
	assertUsage(ctx, t, s, quota.UsageQuery{
		Scope: billing.ScopeAccount, ScopeID: accountID, QuotaID: quotaID,
		Metric: billing.MetricInputToken, Since: farPast,
	}, "150")

	// request 聚合键：一次请求一行流水、每行写 request=1，窗口内聚合值即行数。
	assertUsage(ctx, t, s, quota.UsageQuery{
		Scope: billing.ScopeAccount, ScopeID: accountID, QuotaID: quotaID,
		Metric: billing.MetricRequest, Since: farPast,
	}, "2")

	// scope 维度映射：channel 维度按 channel_id 命中同一批流水。
	assertUsage(ctx, t, s, quota.UsageQuery{
		Scope: billing.ScopeChannel, ScopeID: channelID, QuotaID: quotaID,
		Metric: billing.MetricInputToken, Since: farPast,
	}, "150")

	// 窗口下界：起点晚于全部流水时聚合为 0。
	assertUsage(ctx, t, s, quota.UsageQuery{
		Scope: billing.ScopeAccount, ScopeID: accountID, QuotaID: quotaID,
		Metric: billing.MetricInputToken, Since: farFuture,
	}, "0")

	// metric 不存在于任一流水时，JSON_EXTRACT 返回 NULL，求和为 0。
	assertUsage(ctx, t, s, quota.UsageQuery{
		Scope: billing.ScopeAccount, ScopeID: accountID, QuotaID: quotaID,
		Metric: billing.MetricReasoningToken, Since: farPast,
	}, "0")

	// api_key 维度聚合：只累加该 key 的流水，api_key_id=0 的行不计入。
	assertUsage(ctx, t, s, quota.UsageQuery{
		Scope: billing.ScopeAPIKey, ScopeID: keyAQuotaID, QuotaID: keyQuotaID,
		Metric: billing.MetricInputToken, Since: farPast,
	}, "100")
	assertUsage(ctx, t, s, quota.UsageQuery{
		Scope: billing.ScopeAPIKey, ScopeID: keyBID, QuotaID: keyQuotaID,
		Metric: billing.MetricInputToken, Since: farPast,
	}, "50")

	// reset 基准截断：基准晚于全部流水时聚合为 0，即便窗口起点很早。
	if _, err := s.InsertQuotaEvent(ctx, store.QuotaEvent{
		QuotaID: quotaID, Event: billing.QuotaEventReset, BaselineAt: farFuture,
		Reason: "集成测试重置", Operator: "ops",
	}); err != nil {
		t.Fatalf("写入重置事件失败：%v", err)
	}
	assertUsage(ctx, t, s, quota.UsageQuery{
		Scope: billing.ScopeAccount, ScopeID: accountID, QuotaID: quotaID,
		Metric: billing.MetricInputToken, Since: farPast,
	}, "0")

	// plan 在 billing_usage 上没有维度，聚合应明确报错而不是静默聚合。
	if _, err := s.Usage(ctx, quota.UsageQuery{
		Scope: billing.ScopePlan, ScopeID: 1, QuotaID: quotaID,
		Metric: billing.MetricInputToken, Since: farPast,
	}); err == nil {
		t.Error("plan 维度应报错而不是静默聚合")
	}

	// request 限额判定链路：窗口内聚合值等于行数，达到 limit 即超限（>= 而非 >）。
	// 用 total 口径避开自然日边界，只验证「行数 → 已用量 → 超限」。下一条流水
	// （第 4 次请求）落库后就会被拦。
	requestQuotaID, err := s.InsertQuota(ctx, store.Quota{
		Scope: billing.ScopeAccount, ScopeID: accountID, Metric: billing.MetricRequest,
		WindowKind: billing.WindowKindCalendar, Period: billing.PeriodTotal,
		LimitAmount: "3", Action: billing.ActionReject,
	})
	if err != nil {
		t.Fatalf("写入 request 限额失败：%v", err)
	}
	requestLimit := quota.Limit{
		ID: requestQuotaID, Scope: billing.ScopeAccount, ScopeID: accountID,
		Metric: billing.MetricRequest, WindowKind: billing.WindowKindCalendar,
		Period: billing.PeriodTotal, LimitAmount: decimal.NewFromInt(3), Action: billing.ActionReject,
	}
	// 已落库 2 行 → 已用量 2，尚未达到 3。
	used, ok, err := quota.Used(ctx, s, requestLimit, time.Now())
	if err != nil || !ok {
		t.Fatalf("聚合 request 用量失败：ok=%v err=%v", ok, err)
	}
	if !decimalEqual(used.String(), "2") || requestLimit.Exceeded(used) {
		t.Fatalf("request 已用量 = %s，期望 2 且未超限", used)
	}
	// 第 3 行落库后已用量达到 3，判定超限；下一次（第 4 次）请求因此被拦。
	if _, err := s.InsertUsage(ctx, store.UsageRow{
		MerchantID: 1, AccountID: accountID, ChannelID: channelID, Model: "up-model",
		Usage: map[billing.Metric]int{billing.MetricRequest: 1},
	}); err != nil {
		t.Fatalf("写入 request 用量失败：%v", err)
	}
	used, ok, err = quota.Used(ctx, s, requestLimit, time.Now())
	if err != nil || !ok {
		t.Fatalf("聚合 request 用量失败：ok=%v err=%v", ok, err)
	}
	if !decimalEqual(used.String(), "3") || !requestLimit.Exceeded(used) {
		t.Fatalf("request 已用量 = %s，期望 3 且已超限", used)
	}
}

func assertUsage(ctx context.Context, t *testing.T, s *store.Store, q quota.UsageQuery, want string) {
	t.Helper()
	used, err := s.Usage(ctx, q)
	if err != nil {
		t.Fatalf("聚合用量失败：%v", err)
	}
	if !decimalEqual(used.String(), want) {
		t.Errorf("已用量 = %s，期望 %s（scope=%s scope_id=%d metric=%s）",
			used, want, q.Scope, q.ScopeID, q.Metric)
	}
}
