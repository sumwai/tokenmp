package admin

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖配置动作的两类新防线：引用与动作目标必须先存在，以及修改动作的入参口径。
//
// 两类「不存在」的标记不同：请求体里的引用不存在属参数错误（ErrInvalidInput），
// 动作目标不存在属目标缺失（ErrNotFound）。调用方靠这个区分回 400 还是 404。

// notFound 返回一个「按主键读不到」的假实现，赋给某个 ByID 钩子。
func notFound[T any]() func(context.Context, uint64) (*T, error) {
	return func(context.Context, uint64) (*T, error) {
		return nil, sql.ErrNoRows
	}
}

// TestCreateChannelRejectsMissingMerchant 守护渠道归属必须指向存在的商家。
func TestCreateChannelRejectsMissingMerchant(t *testing.T) {
	f := &fakeStore{}
	f.merchantByID = notFound[store.Merchant]()
	_, err := newService(f).CreateChannel(context.Background(), ChannelInput{
		MerchantID: 99, Name: "c", Type: store.ChannelType("openai_chat"),
		CredGroup: "g", BaseURL: "https://u",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("应报参数错误：%v", err)
	}
	if f.called("InsertChannel") {
		t.Error("归属不存在时不应触达存储层")
	}
}

// TestAddCredentialRejectsMissingMerchant 守护凭据归属必须指向存在的商家。
func TestAddCredentialRejectsMissingMerchant(t *testing.T) {
	f := &fakeStore{}
	f.merchantByID = notFound[store.Merchant]()
	_, err := newService(f).AddCredential(context.Background(), CredentialInput{
		MerchantID: 99, Group: "g", Name: "n", APIKey: "sk-x",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("应报参数错误：%v", err)
	}
	if f.called("InsertCredential") {
		t.Error("归属不存在时不应触达存储层")
	}
}

// TestSetModelMapRejectsMissingChannel 守护模型映射的渠道引用必须存在。
func TestSetModelMapRejectsMissingChannel(t *testing.T) {
	f := &fakeStore{}
	f.channelByID = notFound[store.Channel]()
	_, err := newService(f).SetModelMap(context.Background(), ModelMapInput{
		ChannelID: 99, Model: "m", UpstreamModel: "u",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("应报参数错误：%v", err)
	}
	if f.called("UpsertModelMap") {
		t.Error("渠道不存在时不应触达存储层")
	}
}

// TestSetMerchantOwnerRejectsMissingUser 守护归属绑定的目标主体必须存在。
//
// web_user.id 是自增的，绑到一个还不存在的 id 之后，那个 id 被后来注册的人拿到时，
// 商家连同它在商家域的全部数据会归到对方名下。
func TestSetMerchantOwnerRejectsMissingUser(t *testing.T) {
	f := &fakeStore{}
	f.webUserByID = notFound[store.WebUser]()
	err := newService(f).SetMerchantOwner(context.Background(), 3, 99)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("应报参数错误：%v", err)
	}
	if f.called("SetMerchantOwner") {
		t.Error("主体不存在时不应触达存储层")
	}
}

// TestSetMerchantOwnerRejectsMissingMerchant 守护商家不存在时回目标缺失而不是参数错误。
func TestSetMerchantOwnerRejectsMissingMerchant(t *testing.T) {
	f := &fakeStore{}
	f.merchantByID = notFound[store.Merchant]()
	err := newService(f).SetMerchantOwner(context.Background(), 3, 9)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("应报目标缺失：%v", err)
	}
	if errors.Is(err, ErrInvalidInput) {
		t.Fatalf("不应同时是参数错误：%v", err)
	}
}

// TestActionsOnMissingRowAreNotFound 守护启停动作与修改动作在主键写错时回目标缺失。
func TestActionsOnMissingRowAreNotFound(t *testing.T) {
	cases := []struct {
		name string
		call func(*Service) error
		hook func(*fakeStore)
	}{
		{
			name: "停用渠道",
			call: func(s *Service) error { return s.DisableChannel(context.Background(), 9) },
			hook: func(f *fakeStore) { f.channelByID = notFound[store.Channel]() },
		},
		{
			name: "停用凭据",
			call: func(s *Service) error { return s.DisableCredential(context.Background(), 9) },
			hook: func(f *fakeStore) { f.credentialByID = notFound[store.CredentialRow]() },
		},
		{
			name: "停用模型映射",
			call: func(s *Service) error { return s.DisableModelMap(context.Background(), 9) },
			hook: func(f *fakeStore) { f.modelMapByID = notFound[store.ModelMap]() },
		},
		{
			name: "停用商家",
			call: func(s *Service) error { return s.DisableMerchant(context.Background(), 9) },
			hook: func(f *fakeStore) { f.merchantByID = notFound[store.Merchant]() },
		},
		{
			name: "修改渠道",
			call: func(s *Service) error {
				return s.UpdateChannel(context.Background(), 9, ChannelInput{
					MerchantID: 1, Name: "c", Type: store.ChannelType("openai_chat"),
					CredGroup: "g", BaseURL: "https://u",
				})
			},
			hook: func(f *fakeStore) { f.channelByID = notFound[store.Channel]() },
		},
		{
			name: "修改凭据",
			call: func(s *Service) error {
				return s.UpdateCredential(context.Background(), 9, CredentialInput{
					MerchantID: 1, Group: "g", Name: "n",
				})
			},
			hook: func(f *fakeStore) { f.credentialByID = notFound[store.CredentialRow]() },
		},
		{
			name: "修改商家",
			call: func(s *Service) error {
				return s.UpdateMerchant(context.Background(), 9, "c", "n", store.MerchantKindPartner)
			},
			hook: func(f *fakeStore) { f.merchantByID = notFound[store.Merchant]() },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			tc.hook(f)
			if err := tc.call(newService(f)); !errors.Is(err, ErrNotFound) {
				t.Fatalf("应报目标缺失：%v", err)
			}
		})
	}
}

// TestEnableMerchant 守护商家停用之后能原地恢复。
func TestEnableMerchant(t *testing.T) {
	f := &fakeStore{}
	var gotStatus string
	f.setMerchantStatus = func(_ context.Context, id uint64, status string) error {
		gotStatus = status
		return nil
	}
	if err := newService(f).EnableMerchant(context.Background(), 4); err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if gotStatus != store.StatusActive {
		t.Fatalf("置位状态 = %q，期望 %q", gotStatus, store.StatusActive)
	}
}

// TestUpdateMerchantPassesFields 守护修改商家把三个字段原样传给存储层。
func TestUpdateMerchantPassesFields(t *testing.T) {
	f := &fakeStore{}
	var gotID uint64
	var gotCode, gotName string
	var gotKind store.MerchantKind
	f.updateMerchant = func(_ context.Context, id uint64, code, name string, kind store.MerchantKind) error {
		gotID, gotCode, gotName, gotKind = id, code, name, kind
		return nil
	}
	err := newService(f).UpdateMerchant(context.Background(), 4, "acme-2", "改名", store.MerchantKindPartner)
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if gotID != 4 || gotCode != "acme-2" || gotName != "改名" || gotKind != store.MerchantKindPartner {
		t.Fatalf("入参 = %d/%s/%s/%s", gotID, gotCode, gotName, gotKind)
	}
}

// TestUpdateChannelUsesSameDefaultsAsCreate 守护修改与新建共用同一套默认值与 config 口径。
func TestUpdateChannelUsesSameDefaultsAsCreate(t *testing.T) {
	f := &fakeStore{}
	var got store.Channel
	f.updateChannel = func(_ context.Context, c store.Channel) error {
		got = c
		return nil
	}
	err := newService(f).UpdateChannel(context.Background(), 7, ChannelInput{
		MerchantID: 2, Name: "改名", Type: store.ChannelType("openai_chat"),
		CredGroup: "g", BaseURL: "https://u", CredentialStyle: domain.CredentialHeaderQuery,
	})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if got.ID != 7 {
		t.Fatalf("主键 = %d，期望 7", got.ID)
	}
	if got.Priority != defaultChannelPriority || got.Weight != defaultChannelWeight {
		t.Fatalf("默认值 = %d/%d，期望 %d/%d",
			got.Priority, got.Weight, defaultChannelPriority, defaultChannelWeight)
	}
	if string(got.Config) != `{"credential_style":"query"}` {
		t.Fatalf("config = %s，期望带凭据注入形态", got.Config)
	}
}

// TestUpdateCredentialKeepsSecretWhenKeyEmpty 守护不带 api-key 的修改不动 secret。
func TestUpdateCredentialKeepsSecretWhenKeyEmpty(t *testing.T) {
	f := &fakeStore{}
	var got store.CredentialRow
	f.updateCredential = func(_ context.Context, c store.CredentialRow) error {
		got = c
		return nil
	}
	err := newService(f).UpdateCredential(context.Background(), 5, CredentialInput{
		MerchantID: 2, Group: "g2", Name: "改名",
	})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if got.ID != 5 || got.CredGroup != "g2" || got.Name != "改名" {
		t.Fatalf("入参 = %+v", got)
	}
	if len(got.Secret) != 0 {
		t.Fatalf("未给 api-key 时不应写 secret：%s", got.Secret)
	}
}

// TestUpdateCredentialRotatesSecret 守护带 api-key 的修改覆盖 secret，且明文以 api_key 承载。
func TestUpdateCredentialRotatesSecret(t *testing.T) {
	f := &fakeStore{}
	var got store.CredentialRow
	f.updateCredential = func(_ context.Context, c store.CredentialRow) error {
		got = c
		return nil
	}
	err := newService(f).UpdateCredential(context.Background(), 5, CredentialInput{
		MerchantID: 2, Group: "g", Name: "n", APIKey: "sk-new",
	})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if len(got.Secret) == 0 {
		t.Fatal("带 api-key 的修改应写入 secret")
	}
	if !strings.Contains(string(got.Secret), "sk-new") {
		t.Fatalf("secret 未承载新明文：%s", got.Secret)
	}
}
