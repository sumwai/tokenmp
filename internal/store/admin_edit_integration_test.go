//go:build integration

// 管理面修改动作的真实 MySQL 验证：按主键读取的三个查询、三个更新语句，
// 以及引用校验与目标缺失两类标记在真实驱动下的表现。
//
// 与迁移验证、管理面全链路同属「需要数据库」的一类，由 make check-integration 显式触发。
package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// TestAdminEditActionsIntegration 覆盖修改动作与服务层的引用校验。
func TestAdminEditActionsIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过管理面修改动作验证", envTestDSN)
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

	// 按主键读不到时给 sql.ErrNoRows：服务层据此翻成 404，页面才分得清「改错了字段」
	// 与「这一行已经不在」。
	if _, err := s.MerchantByID(ctx, 999999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("商家读不到时应给 sql.ErrNoRows：%v", err)
	}
	if _, err := s.ChannelByID(ctx, 999999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("渠道读不到时应给 sql.ErrNoRows：%v", err)
	}
	if _, err := s.CredentialByID(ctx, 999999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("凭据读不到时应给 sql.ErrNoRows：%v", err)
	}
	if _, err := s.ModelMapByID(ctx, 999999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("模型映射读不到时应给 sql.ErrNoRows：%v", err)
	}

	svc := admin.New(s)
	merchantID, err := svc.CreateMerchant(ctx, "edit-int", "待修改商家", store.MerchantKindPartner)
	if err != nil {
		t.Fatalf("建商家失败：%v", err)
	}
	channelID, err := svc.CreateChannel(ctx, admin.ChannelInput{
		MerchantID: merchantID, Name: "edit-channel", Type: store.ChannelTypeOpenAIChat,
		CredGroup: "group-edit", BaseURL: "https://before.example.com",
	})
	if err != nil {
		t.Fatalf("建渠道失败：%v", err)
	}

	// 修改商家：编码、名称、类型三列一起写。
	if err := svc.UpdateMerchant(ctx, merchantID, "edit-int-2", "改过的商家", store.MerchantKindPlatform); err != nil {
		t.Fatalf("修改商家失败：%v", err)
	}
	merchant, err := s.MerchantByID(ctx, merchantID)
	if err != nil {
		t.Fatalf("回读商家失败：%v", err)
	}
	if merchant.Code != "edit-int-2" || merchant.Name != "改过的商家" || merchant.Kind != store.MerchantKindPlatform {
		t.Fatalf("商家未按请求更新：%+v", merchant)
	}
	// 状态不受修改动作影响：这一行仍是新建时的 active。
	if merchant.Status != store.StatusActive {
		t.Errorf("修改商家不应改动状态，得到 %q", merchant.Status)
	}

	// 修改渠道：包含 config 与凭据注入形态，两者并进同一个 JSON 对象。
	if err := svc.UpdateChannel(ctx, channelID, admin.ChannelInput{
		MerchantID: merchantID, Name: "edit-channel", Vendor: "anthropic",
		Type: store.ChannelTypeAnthropicMessages, CredGroup: "group-edit",
		BaseURL: "https://after.example.com", Priority: 7, Weight: 8,
		Config:          `{"headers":{"X-Env":"next"}}`,
		CredentialStyle: domain.CredentialHeaderQuery,
	}); err != nil {
		t.Fatalf("修改渠道失败：%v", err)
	}
	channel, err := s.ChannelByID(ctx, channelID)
	if err != nil {
		t.Fatalf("回读渠道失败：%v", err)
	}
	if channel.BaseURL != "https://after.example.com" || channel.Vendor != "anthropic" ||
		channel.Type != store.ChannelTypeAnthropicMessages || channel.Priority != 7 || channel.Weight != 8 {
		t.Fatalf("渠道未按请求更新：%+v", channel)
	}
	// config 落在 JSON 列里，由 MySQL 重新序列化（键序与空白都会变），因此按结构比对。
	var config struct {
		Headers  map[string]string `json:"headers"`
		StyleKey string            `json:"credential_style"`
	}
	if err := json.Unmarshal(channel.Config, &config); err != nil {
		t.Fatalf("解析渠道 config 失败：%v（%s）", err, channel.Config)
	}
	if config.Headers["X-Env"] != "next" || config.StyleKey != "query" {
		t.Fatalf("渠道 config = %s", channel.Config)
	}
	// 启用位不受修改动作影响。
	if !channel.Enabled {
		t.Error("修改渠道不应改动启用位")
	}

	// 修改模型映射：按主键改行，模型名本身与转发取值都能改。
	mapID, err := svc.SetModelMap(ctx, admin.ModelMapInput{
		ChannelID: channelID, Model: "alias-before", UpstreamModel: "up-before",
	})
	if err != nil {
		t.Fatalf("写模型映射失败：%v", err)
	}
	if err := svc.UpdateModelMap(ctx, mapID, admin.ModelMapInput{
		ChannelID: channelID, Model: "alias-after", UpstreamModel: "up-after",
		PriceMultiplier: "1.5", RequestOverrides: []byte(`{"temperature":0}`),
	}); err != nil {
		t.Fatalf("修改模型映射失败：%v", err)
	}
	modelMap, err := s.ModelMapByID(ctx, mapID)
	if err != nil {
		t.Fatalf("回读模型映射失败：%v", err)
	}
	// 倍率是 DECIMAL 列，读回来带列的小数位（1.5 读出 1.5000），按数值比对。
	gotMultiplier, err := decimal.NewFromString(modelMap.PriceMultiplier)
	if err != nil {
		t.Fatalf("解析 price_multiplier 失败：%v", err)
	}
	if modelMap.Model != "alias-after" || modelMap.UpstreamModel != "up-after" ||
		!gotMultiplier.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("模型映射未按请求更新：%+v", modelMap)
	}
	// overrides 同样落在 JSON 列里，由 MySQL 重新序列化，按结构比对。
	var overrides map[string]any
	if err := json.Unmarshal(modelMap.RequestOverrides, &overrides); err != nil {
		t.Fatalf("解析 request_overrides 失败：%v（%s）", err, modelMap.RequestOverrides)
	}
	if len(overrides) != 1 || overrides["temperature"] != float64(0) {
		t.Fatalf("request_overrides = %s", modelMap.RequestOverrides)
	}
	// 启用位不受修改动作影响，修改也不新增行。
	if !modelMap.Enabled {
		t.Error("修改模型映射不应改动启用位")
	}
	allMaps, err := s.ListModelMaps(ctx)
	if err != nil {
		t.Fatalf("列模型映射失败：%v", err)
	}
	if len(allMaps) != 1 {
		t.Fatalf("按主键修改不应新增行，得到 %d 行", len(allMaps))
	}

	// 修改凭据：不带 api-key 时 secret 原样保留。
	credentialID, err := svc.AddCredential(ctx, admin.CredentialInput{
		MerchantID: merchantID, Group: "group-edit", Name: "primary", APIKey: "sk-before",
	})
	if err != nil {
		t.Fatalf("写凭据失败：%v", err)
	}
	before, err := s.CredentialByID(ctx, credentialID)
	if err != nil {
		t.Fatalf("回读凭据失败：%v", err)
	}
	if err := svc.UpdateCredential(ctx, credentialID, admin.CredentialInput{
		MerchantID: merchantID, Group: "group-edit-2", Name: "renamed",
	}); err != nil {
		t.Fatalf("修改凭据失败：%v", err)
	}
	kept, err := s.CredentialByID(ctx, credentialID)
	if err != nil {
		t.Fatalf("回读凭据失败：%v", err)
	}
	if kept.CredGroup != "group-edit-2" || kept.Name != "renamed" {
		t.Fatalf("凭据未按请求更新：%+v", kept)
	}
	if string(kept.Secret) != string(before.Secret) {
		t.Fatalf("不带 api-key 的修改改动了明文：%s -> %s", before.Secret, kept.Secret)
	}

	// 带上 api-key 时在原行上轮换：行 id 不变。
	if err := svc.UpdateCredential(ctx, credentialID, admin.CredentialInput{
		MerchantID: merchantID, Group: "group-edit-2", Name: "renamed", APIKey: "sk-after",
	}); err != nil {
		t.Fatalf("轮换凭据失败：%v", err)
	}
	rotated, err := s.CredentialByID(ctx, credentialID)
	if err != nil {
		t.Fatalf("回读凭据失败：%v", err)
	}
	if rotated.ID != credentialID {
		t.Fatalf("轮换改动了主键：%d -> %d", credentialID, rotated.ID)
	}
	if string(rotated.Secret) == string(before.Secret) {
		t.Fatal("轮换未覆盖明文")
	}

	// 引用校验：不存在的商家、渠道、主体都属参数错误，且不落任何行。
	if _, err := svc.CreateChannel(ctx, admin.ChannelInput{
		MerchantID: 999999, Name: "c", Type: store.ChannelTypeOpenAIChat,
		CredGroup: "g", BaseURL: "https://u",
	}); !errors.Is(err, admin.ErrInvalidInput) {
		t.Errorf("商家不存在应报参数错误：%v", err)
	}
	if _, err := svc.SetModelMap(ctx, admin.ModelMapInput{
		ChannelID: 999999, Model: "m", UpstreamModel: "u",
	}); !errors.Is(err, admin.ErrInvalidInput) {
		t.Errorf("渠道不存在应报参数错误：%v", err)
	}
	if err := svc.SetMerchantOwner(ctx, merchantID, 999999); !errors.Is(err, admin.ErrInvalidInput) {
		t.Errorf("登录主体不存在应报参数错误：%v", err)
	}
	merchant, err = s.MerchantByID(ctx, merchantID)
	if err != nil {
		t.Fatalf("回读商家失败：%v", err)
	}
	if merchant.OwnerUserID != nil {
		t.Errorf("绑定失败不应留下归属：%v", *merchant.OwnerUserID)
	}

	// 目标缺失：修改与启停都报 ErrNotFound。
	if err := svc.UpdateChannel(ctx, 999999, admin.ChannelInput{
		MerchantID: merchantID, Name: "c", Type: store.ChannelTypeOpenAIChat,
		CredGroup: "g", BaseURL: "https://u",
	}); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("渠道不存在应报目标缺失：%v", err)
	}
	if err := svc.DisableMerchant(ctx, 999999); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("商家不存在应报目标缺失：%v", err)
	}
	if err := svc.EnableCredential(ctx, 999999); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("凭据不存在应报目标缺失：%v", err)
	}
	if err := svc.UpdateModelMap(ctx, 999999, admin.ModelMapInput{
		ChannelID: channelID, Model: "m", UpstreamModel: "u",
	}); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("模型映射不存在应报目标缺失：%v", err)
	}
}
