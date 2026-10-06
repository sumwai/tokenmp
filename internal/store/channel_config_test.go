package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestInsertChannelWritesConfigJSON 守护渠道 config 随插入落库。
//
// config 是选路边界读取凭据注入形态的唯一来源；插入不写这一列，
// 「渠道 config 可切查询参数形态」在管理面就无从配置。
func TestInsertChannelWritesConfigJSON(t *testing.T) {
	fake := &recordedExec{}
	config := json.RawMessage(`{"credential_style":"query"}`)
	if _, err := insertChannel(context.Background(), fake, Channel{
		MerchantID: 1, Name: "n", Type: ChannelTypeGeminiGenerate, CredGroup: "g", BaseURL: "u",
		Config: config,
	}); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !strings.Contains(fake.query, "config") {
		t.Errorf("插入语句应包含 config 列：%s", fake.query)
	}
	if got := fake.args[len(fake.args)-1]; !reflect.DeepEqual(got, []byte(config)) {
		t.Errorf("config 参数 = %#v，期望 %#v", got, []byte(config))
	}
}

// TestInsertChannelWritesNullForEmptyConfig 守护未配置时写 SQL NULL：
// upstream_channel.config 是 JSON 列，空字节串不是合法 JSON。
func TestInsertChannelWritesNullForEmptyConfig(t *testing.T) {
	fake := &recordedExec{}
	if _, err := insertChannel(context.Background(), fake, Channel{
		MerchantID: 1, Name: "n", Type: ChannelTypeOpenAIChat, CredGroup: "g", BaseURL: "u",
	}); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if got := fake.args[len(fake.args)-1]; got != nil {
		t.Errorf("空配置应写成 SQL NULL，实际 %#v", got)
	}
}
