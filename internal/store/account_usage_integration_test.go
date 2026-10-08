//go:build integration

// 账户面用量流水与模型目录的真实 MySQL 验证：列清单与谓词组合、可空请求级信息、
// 模型与协议方言的归拢去重，以及按启用状态与商家收敛的可用性。
package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

func TestAccountUsageIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过账户面用量验证", envTestDSN)
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
		accountID  = 42
		otherAccID = 43
		merchantID = 1
		channelID  = 5
		apiKeyID   = 9
	)
	now := time.Now().Truncate(time.Second)

	// 两条流水：一条带请求级信息，一条只有占位信息（历史行的形态）。
	if _, err := s.InsertUsage(ctx, store.UsageRow{
		MerchantID: merchantID, AccountID: accountID, ChannelID: channelID, APIKeyID: apiKeyID,
		Model: "up-model", RequestedModel: "client-model", Protocol: "openai_chat", CrossProtocol: true,
		Usage: map[billing.Metric]int{billing.MetricInputToken: 10, billing.MetricOutputToken: 4},
	}); err != nil {
		t.Fatalf("写流水失败：%v", err)
	}
	if _, err := s.InsertUsage(ctx, store.UsageRow{
		MerchantID: merchantID, AccountID: accountID, ChannelID: channelID,
		Model: "up-model", Usage: map[billing.Metric]int{billing.MetricInputToken: 1},
	}); err != nil {
		t.Fatalf("写流水失败：%v", err)
	}
	if _, err := s.InsertUsage(ctx, store.UsageRow{
		MerchantID: merchantID, AccountID: otherAccID, ChannelID: channelID,
		Model: "up-model", RequestedModel: "client-model", Usage: map[billing.Metric]int{},
	}); err != nil {
		t.Fatalf("写流水失败：%v", err)
	}

	rows, total, err := s.ListAccountUsage(ctx, store.AccountUsageFilter{AccountID: accountID, Limit: 20})
	if err != nil {
		t.Fatalf("列出用量失败：%v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("流水条数 = %d（total %d），期望 2", len(rows), total)
	}
	// 主键倒序：后写的在前；历史行没有请求级信息，读回零值。
	if rows[0].RequestedModel != "" || rows[0].Protocol != "" || rows[0].CrossProtocol {
		t.Errorf("历史行 = %+v，请求级信息应读回零值", rows[0])
	}
	if rows[1].RequestedModel != "client-model" || rows[1].Protocol != "openai_chat" || !rows[1].CrossProtocol {
		t.Errorf("带请求级信息的行 = %+v，与写入不符", rows[1])
	}
	if len(rows[1].Usage) == 0 || rows[1].CreatedAt.IsZero() {
		t.Errorf("用量与写入时刻应读回，得到 %s / %s", rows[1].Usage, rows[1].CreatedAt)
	}

	// 过滤：模型与密钥各收敛一次，区间按闭区间匹配。
	byModel, modelTotal, err := s.ListAccountUsage(ctx, store.AccountUsageFilter{
		AccountID: accountID, RequestedModel: "client-model", Limit: 20,
	})
	if err != nil {
		t.Fatalf("按模型过滤失败：%v", err)
	}
	if modelTotal != 1 || len(byModel) != 1 || byModel[0].RequestedModel != "client-model" {
		t.Errorf("按模型过滤 = %+v（total %d），期望只剩带请求级信息的一条", byModel, modelTotal)
	}
	_, keyTotal, err := s.ListAccountUsage(ctx, store.AccountUsageFilter{
		AccountID: accountID, APIKeyID: apiKeyID, Limit: 20,
	})
	if err != nil {
		t.Fatalf("按密钥过滤失败：%v", err)
	}
	if keyTotal != 1 {
		t.Errorf("按密钥过滤 total = %d，期望 1", keyTotal)
	}
	if _, untilTotal, err := s.ListAccountUsage(ctx, store.AccountUsageFilter{
		AccountID: accountID, Until: now.Add(-time.Hour), Limit: 20,
	}); err != nil {
		t.Fatalf("按时刻过滤失败：%v", err)
	} else if untilTotal != 0 {
		t.Errorf("结束时刻早于全部流水时应为空，得到 %d 条", untilTotal)
	}

	// 分页：一页一条，第二页拿到剩下那条。
	first, _, err := s.ListAccountUsage(ctx, store.AccountUsageFilter{AccountID: accountID, Limit: 1})
	if err != nil {
		t.Fatalf("分页查询失败：%v", err)
	}
	second, _, err := s.ListAccountUsage(ctx, store.AccountUsageFilter{AccountID: accountID, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("分页查询失败：%v", err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
		t.Errorf("分页结果 = %d / %d 条且应互不相同", len(first), len(second))
	}
}

func TestAccountModelsIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过模型目录验证", envTestDSN)
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

	const merchantID = 1
	// 账户默认商家留空：生效商家解析应回落到平台自营。
	accountID, err := s.InsertAccount(ctx, store.Account{
		Code: "acc-models", Name: "模型目录", PriceMultiplier: "1", Status: "active",
	})
	if err != nil {
		t.Fatalf("建账户失败：%v", err)
	}
	// 同模型命中两条渠道（协议不同）用于归拢去重，另有一条渠道被禁用、一条模型映射被禁用。
	chatChannel, err := s.InsertChannel(ctx, store.Channel{
		MerchantID: merchantID, Name: "chat", Vendor: "v", Type: store.ChannelTypeOpenAIChat,
		CredGroup: "g1", BaseURL: "https://example.com/v1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("建渠道失败：%v", err)
	}
	anthropicChannel, err := s.InsertChannel(ctx, store.Channel{
		MerchantID: merchantID, Name: "anthropic", Vendor: "v", Type: store.ChannelTypeAnthropicMessages,
		CredGroup: "g2", BaseURL: "https://example.com", Enabled: true,
	})
	if err != nil {
		t.Fatalf("建渠道失败：%v", err)
	}
	disabledChannel, err := s.InsertChannel(ctx, store.Channel{
		MerchantID: merchantID, Name: "disabled", Vendor: "v", Type: store.ChannelTypeGeminiGenerate,
		CredGroup: "g3", BaseURL: "https://example.com", Enabled: false,
	})
	if err != nil {
		t.Fatalf("建渠道失败：%v", err)
	}
	for _, m := range []store.ModelMap{
		{ChannelID: chatChannel, Model: "shared-model", UpstreamModel: "up-a", PriceMultiplier: "1", Enabled: true},
		{ChannelID: anthropicChannel, Model: "shared-model", UpstreamModel: "up-b", PriceMultiplier: "1", Enabled: true},
		{ChannelID: disabledChannel, Model: "disabled-model", UpstreamModel: "up-c", PriceMultiplier: "1", Enabled: true},
		{ChannelID: chatChannel, Model: "off-model", UpstreamModel: "up-d", PriceMultiplier: "1", Enabled: true},
	} {
		if _, err := s.UpsertModelMap(ctx, m); err != nil {
			t.Fatalf("写模型映射失败：%v", err)
		}
	}
	// 新建一律启用，停用是独立动作：按停用后的状态断言可用性，才真的覆盖过滤条件。
	if err := s.SetChannelEnabled(ctx, disabledChannel, false); err != nil {
		t.Fatalf("停用渠道失败：%v", err)
	}
	// 停用动作按主键定位：upsert 命中既有行时 LastInsertId 为 0，行 id 从列表取。
	maps, err := s.ListModelMaps(ctx)
	if err != nil {
		t.Fatalf("列出模型映射失败：%v", err)
	}
	for _, m := range maps {
		if m.Model == "off-model" {
			if err := s.SetModelMapEnabled(ctx, m.ID, false); err != nil {
				t.Fatalf("停用模型映射失败：%v", err)
			}
		}
	}

	models, err := s.ListAccountModels(ctx, accountID)
	if err != nil {
		t.Fatalf("列出模型失败：%v", err)
	}
	if len(models) != 1 {
		t.Fatalf("模型数 = %d（%+v），期望只有 shared-model", len(models), models)
	}
	if models[0].Name != "shared-model" {
		t.Errorf("模型名 = %q，期望 shared-model", models[0].Name)
	}
	// 两条渠道协议不同且已按字典序排列；禁用渠道与禁用映射都不出现。
	want := []string{"anthropic_messages", "openai_chat"}
	if len(models[0].Protocols) != len(want) {
		t.Fatalf("协议数 = %v，期望 %v", models[0].Protocols, want)
	}
	for i, protocol := range want {
		if models[0].Protocols[i] != protocol {
			t.Errorf("第 %d 个协议 = %q，期望 %q", i, models[0].Protocols[i], protocol)
		}
	}

	// 另一商家的账户默认商家不同时看不到平台的模型。
	otherMerchant, err := s.InsertMerchant(ctx, store.Merchant{Code: "other-merchant", Name: "另一商家", Kind: store.MerchantKindPartner, Status: "active"})
	if err != nil {
		t.Fatalf("建商家失败：%v", err)
	}
	otherAccount, err := s.InsertAccount(ctx, store.Account{
		Code: "acc-other", Name: "另一账户", DefaultMerchantID: &otherMerchant,
		PriceMultiplier: "1", Status: "active",
	})
	if err != nil {
		t.Fatalf("建账户失败：%v", err)
	}
	otherModels, err := s.ListAccountModels(ctx, otherAccount)
	if err != nil {
		t.Fatalf("列出模型失败：%v", err)
	}
	if len(otherModels) != 0 {
		t.Errorf("另一商家下不应看到平台模型，得到 %+v", otherModels)
	}
}
