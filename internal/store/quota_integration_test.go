//go:build integration

// 窗口限额聚合的真实 MySQL 验证：JSON_EXTRACT 求和、窗口下界与 reset 基准截断。
// 与迁移验证同属「需要数据库」的一类，由 make check-integration 显式触发。
package store_test

import (
	"context"
	"os"
	"testing"
	"time"

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
		{billing.MetricInputToken: 100, billing.MetricOutputToken: 5},
		{billing.MetricInputToken: 50},
	} {
		if _, err := s.InsertUsage(ctx, store.UsageRow{
			MerchantID: 1, AccountID: accountID, ChannelID: channelID, Model: "up-model", Usage: usage,
		}); err != nil {
			t.Fatalf("写入用量失败：%v", err)
		}
	}

	// metric 聚合键：只累加 input_token，输出 token 不计入。
	assertUsage(ctx, t, s, quota.UsageQuery{
		Scope: billing.ScopeAccount, ScopeID: accountID, QuotaID: quotaID,
		Metric: billing.MetricInputToken, Since: farPast,
	}, "150")

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

	// api_key / plan 在 billing_usage 上没有维度，聚合应明确报错。
	if _, err := s.Usage(ctx, quota.UsageQuery{
		Scope: billing.ScopeAPIKey, ScopeID: 1, QuotaID: quotaID,
		Metric: billing.MetricInputToken, Since: farPast,
	}); err == nil {
		t.Error("api_key 维度应报错而不是静默聚合")
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
