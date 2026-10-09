package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/sumwai/tokenmp/internal/store"
)

// TestValidationErrorsAreMarked 守护「输入校验失败可被调用方识别」。
//
// 校验错误必须命中 ErrInvalidInput，文案仍原样面向操作者（不带标记本身）：
// 页面层据此把「配错了」翻成 400，CLI 的报错文案不因此改变。标记一旦丢失，
// 配置错误会被报成 500，管理员会去查服务端而不是查自己填的字段。
func TestValidationErrorsAreMarked(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "必填字符串", err: requireString("渠道 name", ""), want: "admin: 渠道 name 不能为空"},
		{name: "必填主键", err: requireID("渠道 id", 0), want: "admin: 渠道 id 不能为 0"},
		{name: "空定点小数", err: requireDecimal("倍率", " "), want: "admin: 倍率 不能为空"},
		{name: "非定点小数", err: requireDecimal("倍率", "1x"), want: `admin: 倍率 不是合法数字 "1x"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("非法输入应当报错")
			}
			if !errors.Is(tc.err, ErrInvalidInput) {
				t.Fatalf("错误未带 ErrInvalidInput 标记：%v", tc.err)
			}
			if tc.err.Error() != tc.want {
				t.Fatalf("文案 = %q，期望 %q", tc.err.Error(), tc.want)
			}
		})
	}
	if err := requireString("渠道 name", "主渠道"); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
}

// TestChannelConfigErrorsAreMarked 守护渠道 config 的口径校验同样带标记。
//
// config 的结构校验没有对应的 CLI flag 白名单，只在服务层做；不带标记就只剩 500。
func TestChannelConfigErrorsAreMarked(t *testing.T) {
	cases := map[string]string{
		"不是 JSON": `not-json`,
		"不是对象":    `[]`,
		"头名被网关占用": `{"headers":{"Host":"example.com"}}`,
		"头值不是字符串": `{"headers":{"X-Env":1}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeStore{}
			_, err := newService(f).CreateChannel(context.Background(), ChannelInput{
				MerchantID: 1, Name: "c", Type: store.ChannelType("openai_chat"),
				CredGroup: "g", BaseURL: "https://u", Config: raw,
			})
			if err == nil {
				t.Fatal("非法 config 应当报错")
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("错误未带 ErrInvalidInput 标记：%v", err)
			}
			if f.called("InsertChannel") {
				t.Error("校验失败不应触达存储层")
			}
		})
	}
}

// TestStorageErrorsAreNotMarked 守护存储层错误不被误标成输入错误。
func TestStorageErrorsAreNotMarked(t *testing.T) {
	f := &fakeStore{}
	f.insertChannel = func(context.Context, store.Channel) (uint64, error) {
		return 0, store.ErrConflict
	}
	_, err := newService(f).CreateChannel(context.Background(), ChannelInput{
		MerchantID: 1, Name: "c", Type: store.ChannelType("openai_chat"),
		CredGroup: "g", BaseURL: "https://u",
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("应保留唯一键冲突哨兵：%v", err)
	}
	if errors.Is(err, ErrInvalidInput) {
		t.Fatalf("存储层错误不应带输入标记：%v", err)
	}
}
