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

// TestCreateChannelWritesStaticHeaders 守护渠道静态请求头随 config 落库。
//
// 数据面只从 config 读它，写入不落库则「强制自定义头的上游」在管理面无从配置。
func TestCreateChannelWritesStaticHeaders(t *testing.T) {
	f := &fakeStore{}
	var got store.Channel
	f.insertChannel = func(_ context.Context, c store.Channel) (uint64, error) {
		got = c
		return 1, nil
	}
	config := `{"headers":{"x-static-header":"1111"}}`
	if _, err := newService(f).CreateChannel(context.Background(), ChannelInput{
		MerchantID: 1, Name: "ch", Type: store.ChannelTypeOpenAIChat, CredGroup: "g", BaseURL: "https://up",
		Config: config,
	}); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if string(got.Config) != config {
		t.Fatalf("config = %s，期望 %s", got.Config, config)
	}
}

// TestCreateChannelRejectsReservedStaticHeader 守护网关自身占用的头名在写入入口被拦下。
//
// 鉴权头被渠道静态头占住时，客户端就能经透传取得上游凭据的位置；报文控制头被占住
// 则会让 Content-Type 一类与实际报文形态不符。两者都不能只靠读取侧的宽容。
func TestCreateChannelRejectsReservedStaticHeader(t *testing.T) {
	cases := []struct {
		name   string
		config string
	}{
		{name: "鉴权头", config: `{"headers":{"Authorization":"Bearer x"}}`},
		{name: "凭据头另一种形态", config: `{"headers":{"X-Api-Key":"x"}}`},
		{name: "报文类型头", config: `{"headers":{"Content-Type":"text/plain"}}`},
		{name: "传输层头", config: `{"headers":{"Host":"other"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			if _, err := newService(f).CreateChannel(context.Background(), ChannelInput{
				MerchantID: 1, Name: "ch", Type: store.ChannelTypeOpenAIChat, CredGroup: "g", BaseURL: "https://up",
				Config: tc.config,
			}); err == nil {
				t.Fatalf("保留头名应当被拒绝：%s", tc.config)
			}
			if f.called("InsertChannel") {
				t.Error("校验失败不应触达存储层")
			}
		})
	}
}

// TestCreateChannelRejectsMalformedStaticHeaders 守护 headers 的结构在写入入口被校验。
//
// 读取侧对这些形态只让该渠道没有额外请求头；写入侧报错是为了不让操作者
// 误以为头已生效。
func TestCreateChannelRejectsMalformedStaticHeaders(t *testing.T) {
	cases := []struct {
		name   string
		config string
	}{
		{name: "headers 是数组", config: `{"headers":["x"]}`},
		{name: "headers 的值不是字符串", config: `{"headers":{"x-static-header":1}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			if _, err := newService(f).CreateChannel(context.Background(), ChannelInput{
				MerchantID: 1, Name: "ch", Type: store.ChannelTypeOpenAIChat, CredGroup: "g", BaseURL: "https://up",
				Config: tc.config,
			}); err == nil {
				t.Fatalf("非法的 headers 应当被拒绝：%s", tc.config)
			}
			if f.called("InsertChannel") {
				t.Error("校验失败不应触达存储层")
			}
		})
	}
}
