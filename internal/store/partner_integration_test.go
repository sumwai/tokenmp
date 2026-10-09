//go:build integration

// 商家域数据面的真实 MySQL 验证：归属绑定、按商家收敛的读写，以及本商家名下的
// 用量聚合。与其余集成验证同属「需要数据库」的一类，由 make check-integration
// 显式触发，不加入 make check。
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

// TestPartnerDomainIntegration 覆盖商家域的作用域红线：非本商家的资源在存储层就
// 读不到、改不动，页面层因此只需要把「没命中」翻成 404。
func TestPartnerDomainIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过商家域验证", envTestDSN)
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
	ownerID := insertWebUser(t, ctx, s, "partner-owner@example.com", store.RolePartner)

	merchantID, err := svc.CreateMerchant(ctx, "partner-a", "商家甲", store.MerchantKindPartner)
	if err != nil {
		t.Fatalf("建商家失败：%v", err)
	}
	otherMerchantID, err := svc.CreateMerchant(ctx, "partner-b", "商家乙", store.MerchantKindPartner)
	if err != nil {
		t.Fatalf("建第二个商家失败：%v", err)
	}
	if err := svc.SetMerchantOwner(ctx, merchantID, ownerID); err != nil {
		t.Fatalf("绑定归属失败：%v", err)
	}

	// 归属绑定：按登录主体读回商家，未绑定的主体读不到。
	bound, err := s.MerchantByOwner(ctx, ownerID)
	if err != nil {
		t.Fatalf("按归属读商家失败：%v", err)
	}
	if bound.ID != merchantID || bound.OwnerUserID == nil || *bound.OwnerUserID != ownerID {
		t.Fatalf("归属读回不符：%+v", bound)
	}
	if _, err := s.MerchantByOwner(ctx, ownerID+1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("未绑定的主体应回 sql.ErrNoRows，得到 %v", err)
	}

	// 渠道与凭据：两个商家各一条，读路径必须互不可见。
	channelID := createChannel(t, ctx, svc, merchantID, "ch-a", "group-a")
	otherChannelID := createChannel(t, ctx, svc, otherMerchantID, "ch-b", "group-b")
	credentialID := createCredential(t, ctx, svc, merchantID, "group-a", "primary-a")
	createCredential(t, ctx, svc, otherMerchantID, "group-b", "primary-b")

	channels, total, err := s.ChannelsByMerchant(ctx, merchantID, nil, 20, 0)
	if err != nil {
		t.Fatalf("列出本商家渠道失败：%v", err)
	}
	if total != 1 || len(channels) != 1 || channels[0].ID != channelID {
		t.Fatalf("渠道列表应只含本商家的一条，得到 total=%d %+v", total, channels)
	}
	credentials, credTotal, err := s.CredentialsByMerchant(ctx, merchantID, 20, 0)
	if err != nil {
		t.Fatalf("列出本商家凭据失败：%v", err)
	}
	if credTotal != 1 || len(credentials) != 1 || credentials[0].ID != credentialID {
		t.Fatalf("凭据列表应只含本商家的一条，得到 total=%d %+v", credTotal, credentials)
	}

	// 越权启停：作用域写在 UPDATE 的谓词里，非本商家的行改不动。
	if hit, err := s.SetChannelEnabledForMerchant(ctx, otherChannelID, merchantID, false); err != nil || hit {
		t.Fatalf("停用他人渠道应未命中（hit=%v err=%v）", hit, err)
	}
	if hit, err := s.SetChannelEnabledForMerchant(ctx, channelID, merchantID, false); err != nil || !hit {
		t.Fatalf("停用本商家渠道应命中（hit=%v err=%v）", hit, err)
	}
	// 重复停用：取值没变时 MySQL 的 RowsAffected 也是 0，命中判定不能据此变成不存在。
	if hit, err := s.SetChannelEnabledForMerchant(ctx, channelID, merchantID, false); err != nil || !hit {
		t.Fatalf("重复停用应仍命中（hit=%v err=%v）", hit, err)
	}
	if hit, err := s.SetCredentialEnabledForMerchant(ctx, credentialID, otherMerchantID, false); err != nil || hit {
		t.Fatalf("停用他人凭据应未命中（hit=%v err=%v）", hit, err)
	}
	if hit, err := s.SetCredentialEnabledForMerchant(ctx, credentialID, merchantID, false); err != nil || !hit {
		t.Fatalf("停用本商家凭据应命中（hit=%v err=%v）", hit, err)
	}

	// 用量聚合：两个商家各写一条流水，商家甲只看得到自己那条。
	accountID := createAccount(t, ctx, svc, "acct-a", merchantID)
	insertUsage(t, ctx, s, merchantID, accountID, channelID, 10, 4)
	insertUsage(t, ctx, s, otherMerchantID, accountID, otherChannelID, 100, 40)

	items, err := s.MerchantUsageStats(ctx, store.MerchantUsageStatsQuery{
		MerchantID: merchantID,
		GroupBy:    "model",
	})
	if err != nil {
		t.Fatalf("聚合本商家用量失败：%v", err)
	}
	if len(items) != 1 || items[0].Calls != 1 {
		t.Fatalf("聚合应只含本商家的一条流水，得到 %+v", items)
	}
	if items[0].Tokens["input_token"] != 10 || items[0].Tokens["output_token"] != 4 {
		t.Fatalf("token 合计不符：%+v", items[0].Tokens)
	}

	// 与账户面的同一区间合计自洽：两条入口用的是同一份聚合实现。
	accountItems, err := s.AccountUsageStats(ctx, store.UsageStatsQuery{AccountID: accountID, GroupBy: "model"})
	if err != nil {
		t.Fatalf("聚合账户用量失败：%v", err)
	}
	if len(accountItems) != 1 || accountItems[0].Calls != 2 {
		t.Fatalf("账户面应看到两个商家的流水，得到 %+v", accountItems)
	}
	if accountItems[0].Tokens["input_token"] != 110 {
		t.Fatalf("账户面合计应含两条流水，得到 %+v", accountItems[0].Tokens)
	}
}

// insertWebUser 写一个登录主体，返回它的 id；归属列没有外键，但用例用真实主体。
func insertWebUser(t *testing.T, ctx context.Context, s *store.Store, email, role string) uint64 {
	t.Helper()
	userID, _, err := s.WebInsertUserWithAccount(ctx,
		store.WebUser{Email: email, Username: email, PasswordHash: "hash", Role: role, Status: store.StatusActive},
		store.Account{Code: "acct-" + email, Name: email, PriceMultiplier: "1", Status: store.StatusActive})
	if err != nil {
		t.Fatalf("写登录主体失败：%v", err)
	}
	return userID
}

// createChannel 建一条渠道，返回它的 id。
func createChannel(t *testing.T, ctx context.Context, svc *admin.Service, merchantID uint64, name, group string) uint64 {
	t.Helper()
	id, err := svc.CreateChannel(ctx, admin.ChannelInput{
		MerchantID: merchantID, Name: name, Type: store.ChannelTypeOpenAIChat,
		CredGroup: group, BaseURL: "https://up.example.com",
	})
	if err != nil {
		t.Fatalf("建渠道失败：%v", err)
	}
	return id
}

// createCredential 写一份凭据，返回它的 id。
func createCredential(t *testing.T, ctx context.Context, svc *admin.Service, merchantID uint64, group, name string) uint64 {
	t.Helper()
	id, err := svc.AddCredential(ctx, admin.CredentialInput{
		MerchantID: merchantID, Group: group, Name: name, APIKey: "sk-" + name,
	})
	if err != nil {
		t.Fatalf("写凭据失败：%v", err)
	}
	return id
}

// createAccount 建一个归属某商家的账户，返回它的 id。
func createAccount(t *testing.T, ctx context.Context, svc *admin.Service, code string, merchantID uint64) uint64 {
	t.Helper()
	merchant := merchantID
	id, err := svc.CreateAccount(ctx, admin.AccountInput{Code: code, Name: code, DefaultMerchantID: &merchant})
	if err != nil {
		t.Fatalf("建账户失败：%v", err)
	}
	return id
}

// insertUsage 写一条用量流水。
func insertUsage(t *testing.T, ctx context.Context, s *store.Store, merchantID, accountID, channelID uint64, input, output int) {
	t.Helper()
	if _, err := s.InsertUsage(ctx, store.UsageRow{
		MerchantID: merchantID, AccountID: accountID, ChannelID: channelID,
		Model: "up-model", RequestedModel: "up-model",
		Usage: map[billing.Metric]int{billing.MetricInputToken: input, billing.MetricOutputToken: output},
	}); err != nil {
		t.Fatalf("写流水失败：%v", err)
	}
}
