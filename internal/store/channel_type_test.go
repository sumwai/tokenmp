package store

import "testing"

func TestChannelTypeKnown(t *testing.T) {
	known := []ChannelType{
		ChannelTypeOpenAIChat,
		ChannelTypeOpenAIResponses,
		ChannelTypeAnthropicMessages,
	}
	for _, ct := range known {
		if !ct.Known() {
			t.Errorf("%q 应当在白名单内", ct)
		}
	}
	if ChannelType("openai_embeddings").Known() {
		t.Error("未登记的协议方言不应出现在白名单内")
	}
}

func TestValidateChannelTypeRejectsUnknown(t *testing.T) {
	if err := ValidateChannelType(ChannelTypeOpenAIChat); err != nil {
		t.Errorf("已知协议不应报错，实际：%v", err)
	}
	if err := ValidateChannelType(ChannelType("openai_embeddings")); err == nil {
		t.Error("未知协议在写入口应当被拒绝")
	}
}

// TestChannelTypeFromDBToleratesUnknown 守护读方向的宽容策略：
// 数据库里出现本版本未登记的协议方言时，转换不得报错也不得 panic，
// 未知值原样返回，交由上层决定如何处理。
func TestChannelTypeFromDBToleratesUnknown(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "已知值原样返回", raw: string(ChannelTypeAnthropicMessages)},
		{name: "未知值不报错", raw: "openai_embeddings"},
		{name: "空值不报错", raw: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ChannelTypeFromDB(tt.raw); string(got) != tt.raw {
				t.Errorf("ChannelTypeFromDB(%q) = %q，期望原样返回", tt.raw, got)
			}
		})
	}
}
