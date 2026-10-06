//go:build integration

// 上游套餐与配额的真实 MySQL 验证：迁移 0005 的表、套餐与限额行写读回环、
// 探针目标读取与采集结果写回。与其它集成验证同属「需要数据库」的一类。
package store_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/plan"
	"github.com/sumwai/tokenmp/internal/store"
)

// jsonEqual 按 JSON 语义比较两段文本：MySQL 的 JSON 列会重新排版（插空格、
// 调整换行），字节相等判断会把正确的回读误报为不一致。
func jsonEqual(a, b []byte) bool {
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &right); err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func TestUpstreamPlanIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过上游套餐验证", envTestDSN)
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
	for _, table := range []string{"upstream_plan", "upstream_plan_quota"} {
		if !tableExists(ctx, t, s.DB(), table) {
			t.Fatalf("迁移 0005 后表 %s 不存在", table)
		}
	}
	// 0005 幂等：重复迁移不应因表已存在而中断，也不应重复登记版本。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
	assertMigrationVersionCount(ctx, t, s.DB(), 5)

	const merchantID = 7
	validFrom := time.Now().Truncate(time.Second).Add(-time.Hour)
	planID, err := s.InsertUpstreamPlan(ctx, plan.UpstreamPlan{
		MerchantID: merchantID,
		CredGroup:  "group-plan",
		Name:       "集成套餐",
		Multiplier: decimal.RequireFromString("1.5"),
		ValidFrom:  &validFrom,
	}, []plan.Quota{
		{
			Metric: billing.MetricInputToken, WindowKind: billing.WindowKindRolling,
			Period: billing.Period5h, LimitAmount: decimal.RequireFromString("1000"),
		},
		{
			Metric: billing.MetricRequest, WindowKind: billing.WindowKindCalendar,
			Period: billing.PeriodDay, LimitAmount: decimal.RequireFromString("50000"),
		},
	})
	if err != nil {
		t.Fatalf("写入套餐失败：%v", err)
	}

	plans, err := s.Plans(ctx, merchantID)
	if err != nil {
		t.Fatalf("读取套餐失败：%v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("套餐行数 = %d，期望 1", len(plans))
	}
	got := plans[0]
	if got.ID != planID || got.CredGroup != "group-plan" || got.Name != "集成套餐" {
		t.Errorf("套餐内容不符：%+v", got)
	}
	if !got.Multiplier.Equal(decimal.RequireFromString("1.5")) {
		t.Errorf("套餐倍率 = %s，期望 1.5", got.Multiplier.String())
	}
	if got.ValidFrom == nil || !got.ValidFrom.Equal(validFrom) {
		t.Errorf("有效期起点 = %v，期望 %s", got.ValidFrom, validFrom)
	}
	if len(got.Quotas) != 2 {
		t.Fatalf("限额行数 = %d，期望 2", len(got.Quotas))
	}
	if !got.Quotas[0].LimitAmount.Equal(decimal.RequireFromString("1000")) || !got.Quotas[0].LastUsed.IsZero() {
		t.Errorf("首条限额不符：%+v", got.Quotas[0])
	}
	if !got.Quotas[0].LastCheckedAt.IsZero() {
		t.Errorf("未采集时限额行采集时刻应为零值：%+v", got.Quotas[0])
	}

	byGroup, err := s.PlansByCredGroup(ctx, merchantID, "group-plan")
	if err != nil {
		t.Fatalf("按分组读取套餐失败：%v", err)
	}
	if byGroup.ID != planID || len(byGroup.Quotas) != 2 {
		t.Errorf("按分组回读不符：%+v", byGroup)
	}
	if _, err := s.PlansByCredGroup(ctx, merchantID, "missing"); err == nil {
		t.Error("不存在的分组应当报错")
	}

	// 探针目标：只有配置了 config 的启用渠道会被读到。
	config := []byte(`{"probe":{"url":"https://up/usage","metrics":[{"metric":"request","period":"day","used_path":"$.used"}]}}`)
	channelID, err := s.InsertChannel(ctx, store.Channel{
		MerchantID: merchantID, Name: "with-probe", Type: store.ChannelTypeOpenAIChat,
		CredGroup: "group-plan", BaseURL: "https://up", Config: config,
	})
	if err != nil {
		t.Fatalf("写入渠道失败：%v", err)
	}
	if _, err := s.InsertChannel(ctx, store.Channel{
		MerchantID: merchantID, Name: "without-probe", Type: store.ChannelTypeOpenAIChat,
		CredGroup: "group-other", BaseURL: "https://up",
	}); err != nil {
		t.Fatalf("写入无探针渠道失败：%v", err)
	}
	targets, err := s.ProbeTargets(ctx)
	if err != nil {
		t.Fatalf("读取探针目标失败：%v", err)
	}
	if len(targets) != 1 || targets[0].ChannelID != channelID || targets[0].CredGroup != "group-plan" {
		t.Fatalf("探针目标不符：%+v", targets)
	}
	if !jsonEqual(targets[0].Config, config) {
		t.Errorf("探针配置原文不符：%s", targets[0].Config)
	}

	// 采集结果写回：快照与各限额已用量在同一事务里更新。
	checkedAt := time.Now().Truncate(time.Second)
	snapshot := []byte(`{"used":321}`)
	used := []plan.QuotaUsed{
		{QuotaID: got.Quotas[0].ID, Used: decimal.RequireFromString("321")},
		{QuotaID: got.Quotas[1].ID, Used: decimal.RequireFromString("7")},
	}
	if err := s.SaveProbeResult(ctx, planID, snapshot, checkedAt, used); err != nil {
		t.Fatalf("保存采集结果失败：%v", err)
	}
	after, err := s.PlansByCredGroup(ctx, merchantID, "group-plan")
	if err != nil {
		t.Fatalf("回读套餐失败：%v", err)
	}
	if after.LastCheckedAt.IsZero() || !after.LastCheckedAt.Equal(checkedAt) {
		t.Errorf("套餐采集时刻 = %s，期望 %s", after.LastCheckedAt, checkedAt)
	}
	if !jsonEqual(after.LastSnapshot, snapshot) {
		t.Errorf("快照 = %s，期望 %s", after.LastSnapshot, snapshot)
	}
	if !after.Quotas[0].LastUsed.Equal(decimal.RequireFromString("321")) ||
		!after.Quotas[1].LastUsed.Equal(decimal.RequireFromString("7")) {
		t.Errorf("限额已用量不符：%+v", after.Quotas)
	}
	if !after.Quotas[0].LastCheckedAt.Equal(checkedAt) {
		t.Errorf("限额行采集时刻 = %s，期望 %s", after.Quotas[0].LastCheckedAt, checkedAt)
	}
}
