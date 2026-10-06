package admin

import (
	"context"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// TestCreateChannelEncodesCredentialStyle 守护渠道级凭据注入形态的写入：
// 服务层把注入形态编码进 config JSON，键名与选路边界的读取口径一致。
func TestCreateChannelEncodesCredentialStyle(t *testing.T) {
	f := &fakeStore{}
	var got store.Channel
	f.insertChannel = func(_ context.Context, c store.Channel) (uint64, error) {
		got = c
		return 11, nil
	}
	id, err := newService(f).CreateChannel(context.Background(), ChannelInput{
		MerchantID:      1,
		Name:            "gemini",
		Type:            store.ChannelTypeGeminiGenerate,
		CredGroup:       "grp",
		BaseURL:         "https://generativelanguage.googleapis.com/v1beta",
		CredentialStyle: domain.CredentialHeaderQuery,
	})
	if err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if id != 11 {
		t.Fatalf("渠道 id = %d，期望 11", id)
	}
	if string(got.Config) != `{"credential_style":"query"}` {
		t.Fatalf("config = %s，期望 {\"credential_style\":\"query\"}", got.Config)
	}
}

// TestCreateChannelWithoutCredentialStyleLeavesConfigEmpty 守护未配置注入形态时不写 config。
func TestCreateChannelWithoutCredentialStyleLeavesConfigEmpty(t *testing.T) {
	f := &fakeStore{}
	var got store.Channel
	f.insertChannel = func(_ context.Context, c store.Channel) (uint64, error) {
		got = c
		return 1, nil
	}
	if _, err := newService(f).CreateChannel(context.Background(), ChannelInput{
		MerchantID: 1, Name: "ch", Type: store.ChannelTypeOpenAIChat, CredGroup: "g", BaseURL: "https://up",
	}); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if len(got.Config) != 0 {
		t.Fatalf("未配置注入形态时 config 应为空，实际 %s", got.Config)
	}
}

// TestCreateChannelRejectsUnknownCredentialStyle 守护非法注入形态在写入入口被拦下。
//
// config 里的写坏值只会让选路时静默回退到协议现状，把「配错了」藏起来；
// 写入入口是唯一能把它显式拦下的地方。
func TestCreateChannelRejectsUnknownCredentialStyle(t *testing.T) {
	f := &fakeStore{}
	if _, err := newService(f).CreateChannel(context.Background(), ChannelInput{
		MerchantID:      1,
		Name:            "ch",
		Type:            store.ChannelTypeGeminiGenerate,
		CredGroup:       "g",
		BaseURL:         "https://up",
		CredentialStyle: domain.CredentialHeaderStyle("bogus"),
	}); err == nil {
		t.Fatal("非法注入形态应当被拒绝")
	}
	if f.called("InsertChannel") {
		t.Error("校验失败不应触达存储层")
	}
}
