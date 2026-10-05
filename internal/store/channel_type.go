package store

import "fmt"

// ChannelType 是渠道的协议方言。
//
// 协议是数据面的分发键：请求进来先知道客户端说的是哪个协议，再据此选渠道。
// 厂商只是标签，放在 upstream_channel.vendor 列，不参与 type。
// 因此同一厂商的多个端点各占一行渠道，各自标注协议。
//
// 新增协议 = 新增适配器 + 这里加一个取值 + DDL 注释同步，
// 不改表结构。取值清单与 migrations/0001_init.sql 中 type 列的注释保持一致。
type ChannelType string

// knownChannelTypes 是写入白名单，也是列举取值的唯一出处。
var knownChannelTypes = map[ChannelType]struct{}{
	ChannelTypeOpenAIChat:        {},
	ChannelTypeOpenAIResponses:   {},
	ChannelTypeAnthropicMessages: {},
}

const (
	// ChannelTypeOpenAIChat 是 OpenAI Chat Completions 协议。
	ChannelTypeOpenAIChat ChannelType = "openai_chat"
	// ChannelTypeOpenAIResponses 是 OpenAI Responses 协议。
	ChannelTypeOpenAIResponses ChannelType = "openai_responses"
	// ChannelTypeAnthropicMessages 是 Anthropic Messages 协议。
	ChannelTypeAnthropicMessages ChannelType = "anthropic_messages"
)

// Known 报告该协议方言是否在写入白名单内。
func (t ChannelType) Known() bool {
	_, ok := knownChannelTypes[t]
	return ok
}

// ChannelTypeFromDB 把数据库列值转成 ChannelType，刻意不做白名单校验。
//
// 读方向必须宽容：版本回滚后旧代码会遇到新增协议的渠道行，
// 若在这里报错，同一批查询里已知协议的渠道也会一起失败，
// 一次不认识的协议会放大成整批不可用。未知值原样返回，由上层决定
// 是跳过该行还是按不匹配处理。
func ChannelTypeFromDB(raw string) ChannelType {
	return ChannelType(raw)
}

// ValidateChannelType 是写入口的校验：未知协议方言直接拒绝，避免脏数据落库。
//
// 与 ChannelTypeFromDB 的不对称是有意的 —— 写入由本进程产生，
// 必须是已知取值；读取可能面对更新版本写入的数据，未知不代表错误。
func ValidateChannelType(t ChannelType) error {
	if !t.Known() {
		return fmt.Errorf("store: 未知的渠道协议方言 %q", string(t))
	}
	return nil
}
