//go:build integration

// 账户面用量聚合的真实 MySQL 验证：分组维度、谓词收敛，以及「合计 = 明细逐行相加」的自洽。
// 自洽是本端点存在的理由，因此这里逐项对照明细，而不是只断言几个合计数字。
package store_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

func TestAccountUsageStatsIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过用量聚合验证", envTestDSN)
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
		accountID  = 7
		otherAccID = 8
		merchantID = 1
		channelID  = 5
		keyA       = 9
		keyB       = 10
	)
	// 种子时刻用 UTC 构造：驱动按 UTC 写入 DATETIME，读出与 DATE_FORMAT 也读同一取值，
	// 因此「哪一天」的期望值取 UTC 的那一天（数据库会话时区即分组边界口径）。
	now := time.Now().UTC().Truncate(time.Second)

	// 种子行直接按列写入：聚合要验证的是「按天归拢」与「逐行折算后相加」，
	// 两件事都需要指定写入时刻与非零金额，而写入 API 的 created_at 由数据库默认值给出、
	// 金额由结算路径算出。读路径本身仍走被测函数。
	const seedSQL = "INSERT INTO billing_usage " +
		"(merchant_id, account_id, channel_id, api_key_id, model, requested_model, protocol, cross_protocol, `usage`, " +
		"pricing_id, gross_amount, multiplier, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)"
	seed := func(accID, keyID uint64, model string, at time.Time, gross, multiplier string, metrics map[billing.Metric]int) {
		t.Helper()
		payload, err := json.Marshal(metrics)
		if err != nil {
			t.Fatalf("编码用量失败：%v", err)
		}
		if _, err := s.DB().ExecContext(ctx, seedSQL,
			merchantID, accID, channelID, keyID, "up-"+model, model, "openai_chat", false,
			payload, gross, multiplier, at); err != nil {
			t.Fatalf("写种子流水失败：%v", err)
		}
	}

	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, time.UTC)
	today := dayStart.Format("2006-01-02")
	seed(accountID, keyA, "client-model", dayStart, "0.1", "2",
		map[billing.Metric]int{billing.MetricInputToken: 10, billing.MetricOutputToken: 4})
	seed(accountID, keyB, "client-model", dayStart.Add(time.Hour), "0.5", "1.5",
		map[billing.Metric]int{billing.MetricInputToken: 1, billing.MetricCacheReadToken: 3})
	seed(accountID, keyA, "other-model", dayStart.Add(-26*time.Hour), "0.25", "1",
		map[billing.Metric]int{billing.MetricInputToken: 5})
	seed(otherAccID, keyA, "client-model", dayStart, "9", "9",
		map[billing.Metric]int{billing.MetricInputToken: 100})

	// 按天分组：本账户两天各一行；另一账户的流水不进结果。
	byDay, err := s.AccountUsageStats(ctx, store.UsageStatsQuery{AccountID: accountID, GroupBy: "day"})
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	if len(byDay) != 2 {
		t.Fatalf("按天分组数 = %d，期望 2（%+v）", len(byDay), byDay)
	}
	todayRow := byDay[1]
	if todayRow.Key != today {
		t.Fatalf("分组 key = %q，期望当天 %s", todayRow.Key, today)
	}
	if todayRow.Calls != 2 {
		t.Errorf("当天请求数 = %d，期望 2", todayRow.Calls)
	}
	if got := todayRow.Tokens[string(billing.MetricInputToken)]; got != 11 {
		t.Errorf("当天 input_token 合计 = %d，期望 11", got)
	}
	if got := todayRow.Tokens[string(billing.MetricOutputToken)]; got != 4 {
		t.Errorf("当天 output_token 合计 = %d，期望 4", got)
	}
	if got := todayRow.Tokens[string(billing.MetricCacheReadToken)]; got != 3 {
		t.Errorf("当天 cache_read_token 合计 = %d，期望 3", got)
	}
	if _, ok := todayRow.Tokens[string(billing.MetricReasoningToken)]; ok {
		t.Error("分组内从未出现的指标不应进合计集合")
	}
	// 应扣量逐行折算后相加：0.1×2 + 0.5×1.5 = 0.95。
	if amount, err := decimal.NewFromString(todayRow.ChargedAmount); err != nil || !amount.Equal(decimal.RequireFromString("0.95")) {
		t.Errorf("当天应扣量 = %s（%v），期望 0.95", todayRow.ChargedAmount, err)
	}

	// 按模型分组：本账户两个模型各一行。
	byModel, err := s.AccountUsageStats(ctx, store.UsageStatsQuery{AccountID: accountID, GroupBy: "model"})
	if err != nil {
		t.Fatalf("按模型聚合失败：%v", err)
	}
	if len(byModel) != 2 || byModel[0].Key != "client-model" || byModel[1].Key != "other-model" {
		t.Fatalf("按模型分组 = %+v，期望 client-model / other-model 各一行", byModel)
	}
	if byModel[0].Calls != 2 || byModel[0].Tokens[string(billing.MetricInputToken)] != 11 {
		t.Errorf("client-model 行 = %+v，期望 2 次调用 / 11 input", byModel[0])
	}

	// 按密钥分组：key 是 id 的十进制文本，按字典序升序。
	byKey, err := s.AccountUsageStats(ctx, store.UsageStatsQuery{AccountID: accountID, GroupBy: "api_key"})
	if err != nil {
		t.Fatalf("按密钥聚合失败：%v", err)
	}
	if len(byKey) != 2 || byKey[0].Key != "10" || byKey[1].Key != "9" {
		t.Fatalf("按密钥分组 = %+v，期望按 id 升序的两行", byKey)
	}
	if byKey[1].Calls != 2 {
		t.Errorf("密钥 9 的调用数 = %d，期望 2", byKey[1].Calls)
	}

	// 合计 = 明细逐行相加：这是「同一份事实两种读法」的证据，也是契约里的口径承诺。
	rows, total, err := s.ListAccountUsage(ctx, store.AccountUsageFilter{AccountID: accountID, Limit: 100})
	if err != nil {
		t.Fatalf("列出明细失败：%v", err)
	}
	wantCharged := decimal.Zero
	wantInput := 0
	for i := range rows {
		gross, err := decimal.NewFromString(rows[i].GrossAmount)
		if err != nil {
			t.Fatalf("解析 gross_amount 失败：%v", err)
		}
		multiplier, err := decimal.NewFromString(rows[i].Multiplier)
		if err != nil {
			t.Fatalf("解析 multiplier 失败：%v", err)
		}
		wantCharged = wantCharged.Add(gross.Mul(multiplier))
	}

	totalCalls := int64(0)
	gotCharged := decimal.Zero
	for _, item := range byDay {
		totalCalls += item.Calls
		wantInput += int(item.Tokens[string(billing.MetricInputToken)])
		amount, err := decimal.NewFromString(item.ChargedAmount)
		if err != nil {
			t.Fatalf("解析聚合金额失败：%v", err)
		}
		gotCharged = gotCharged.Add(amount)
	}
	if totalCalls != int64(total) {
		t.Errorf("聚合次数 = %d，明细行数 = %d", totalCalls, total)
	}
	if !gotCharged.Equal(wantCharged) {
		t.Errorf("聚合应扣量 = %s，明细逐行相加 = %s", gotCharged, wantCharged)
	}

	// 模型过滤与时间区间：谓词与明细同源，收敛结果一致。
	byModelFilter, err := s.AccountUsageStats(ctx, store.UsageStatsQuery{
		AccountID: accountID, GroupBy: "day", RequestedModel: "client-model",
	})
	if err != nil {
		t.Fatalf("带模型过滤的聚合失败：%v", err)
	}
	if len(byModelFilter) != 1 || byModelFilter[0].Calls != 2 {
		t.Fatalf("模型过滤后 = %+v，期望只剩当天的 2 次", byModelFilter)
	}
	windowed, err := s.AccountUsageStats(ctx, store.UsageStatsQuery{
		AccountID: accountID, GroupBy: "day", Since: dayStart,
	})
	if err != nil {
		t.Fatalf("带区间聚合失败：%v", err)
	}
	if len(windowed) != 1 || windowed[0].Key != today {
		t.Fatalf("区间内分组 = %+v，期望只有当天一行", windowed)
	}
	if _, err := s.AccountUsageStats(ctx, store.UsageStatsQuery{AccountID: accountID, GroupBy: "status"}); err == nil {
		t.Error("未登记的聚合维度应报错")
	}
	if _, err := s.AccountUsageStats(ctx, store.UsageStatsQuery{GroupBy: "day"}); err == nil {
		t.Error("账户为 0 应报错")
	}
}
