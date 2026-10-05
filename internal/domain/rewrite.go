package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

// RewriteOptions 配置网关对上游请求的改写项。
//
// 零值表示不改写任何字段。每个改写项均可单独开启或关闭：
// 指针为 nil 或字符串为空串时即表示对应改写项关闭。
type RewriteOptions struct {
	// MaxOutputTokens 是渠道配置的输出上限，同时也是客户端未声明时的补齐值。
	//
	// 口径统一为「客户端未声明就补上限」：
	//   - 客户端已声明且未超过：保持客户端声明值；
	//   - 客户端已声明但已超过：钳制到渠道配置上限；
	//   - 客户端未声明：用渠道配置上限补齐。
	//
	// nil 或非正表示渠道未配置输出上限，此时不设置该字段。
	MaxOutputTokens *int
	// IncludeUsage 为 true 时向支持该开关的上游协议索取用量。
	//
	// 目前只有 OpenAI Chat Completions 有这个概念（流式请求的 stream_options.include_usage）；
	// OpenAI Responses 与 Anthropic Messages 的用量本就随流下发，实现保持原样。
	// 只对流式请求有意义：非流式请求的用量随响应体一并返回。
	IncludeUsage bool
	// UpstreamModel 是本次转发要写入上游请求体的模型名，取自选路结果的
	// Route.UpstreamModel；空串表示本项关闭、不改写模型名。
	//
	// 非空时两条路径都按它取模型名：同协议透传路径改写原始报文的顶层模型字段
	// （字段存在则替换、缺失则补齐），跨协议重建路径用它替代 Request.Model。
	UpstreamModel string
	// RequestOverrides 是渠道 × 模型级别的请求参数覆盖 JSON，取自
	// Route.RequestOverrides；nil 或空对象表示本项关闭。
	//
	// 合并语义为顶层键覆盖：出现的顶层键整体替换请求体里的同名字段，其余保持不变。
	// 刻意不做结构递归合并——递归会让「数组该替换还是追加」这类语义落在协议无关的
	// 通用层，各协议无法各自表达；顶层覆盖已能覆盖 temperature、top_p 一类调参需求。
	RequestOverrides json.RawMessage
}

// OutputLimit 按统一口径计算要向上游请求声明的输出上限，返回值 <= 0 表示不设置该字段。
//
// declared 是客户端声明的输出上限，<= 0 表示未声明；limit 是渠道配置的上限。
// 三种协议的输出上限字段名不同，但口径由本函数单一实现。
func OutputLimit(declared int, limit *int) int {
	if limit == nil || *limit <= 0 {
		return declared
	}
	if declared > 0 && declared <= *limit {
		return declared
	}
	return *limit
}

// SetRawOutputLimit 按统一口径改写原始请求体顶层字段表里的输出上限字段，并返回是否发生改写。
//
// existingFields 按优先级排列：只处理第一个存在的字段，其余保持原样。
// fillField 是全部缺失时要补的字段名（落地「客户端未声明就补上限」）；它与existingFields 分开，
// 是因为有的协议有两个同义字段，新模型只认替代字段，补错字段会被上游拒绝。字段取值不是整数时不动，
// 交由上游判定。limit 为 nil 或非正时不改写。
func SetRawOutputLimit(fields map[string]json.RawMessage, existingFields []string, fillField string, limit *int) bool {
	if len(existingFields) == 0 || fillField == "" || limit == nil || *limit <= 0 {
		return false
	}
	for _, name := range existingFields {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		var declared int
		if err := json.Unmarshal(raw, &declared); err != nil {
			return false
		}
		target := OutputLimit(declared, limit)
		if target == declared {
			return false
		}
		fields[name] = json.RawMessage(strconv.Itoa(target))
		return true
	}
	fields[fillField] = json.RawMessage(strconv.Itoa(*limit))
	return true
}

// UpstreamModelName 按改写项选择本次上游请求使用的模型名：
// RewriteOptions.UpstreamModel 非空时用它，空串时回退到内部请求的模型名。
//
// 同协议透传路径与跨协议重建路径共用本函数。
func UpstreamModelName(requestModel string, options RewriteOptions) string {
	if options.UpstreamModel != "" {
		return options.UpstreamModel
	}
	return requestModel
}

// SetRawStringField 改写顶层字符串字段：存在则替换、缺失则补齐，返回是否发生改写。
//
// name 是协议字段名，一律由适配器以参数传入，本包不出现任何协议字段字面量。
//
// 未改写判据：现有内容去掉首尾空白后，与新取值的编码结果逐字节相同即视为未改写，
// 返回 false。调用方据此把「实际改写过哪些字段」随产物回报。
func SetRawStringField(fields map[string]json.RawMessage, name, value string) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	if existing, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(existing), encoded) {
		return false
	}
	fields[name] = encoded
	return true
}

// DecodeRawFields 把请求体解析为顶层字段表，非 JSON 对象返回 CodeInvalidRequest。
//
// 协议无关：三个适配器的同协议透传改写路径共用本函数。
func DecodeRawFields(body []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, NewError(CodeInvalidRequest, "请求体必须是 JSON 对象")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, NewError(CodeInvalidRequest, "请求体不是合法 JSON").WithCause(err)
	}
	if fields == nil {
		return nil, NewError(CodeInvalidRequest, "请求体必须是 JSON 对象")
	}
	return fields, nil
}

// MergeRawOverrides 在已解析的请求字段表上按顶层键覆盖合并覆盖项，返回是否发生改写。
//
// overrides 为空或 JSON null 时不做任何事；不是 JSON 对象时返回错误，
// 由调用方决定跳过（形状在选路边界已收敛，这里是防御性判定）。
func MergeRawOverrides(fields map[string]json.RawMessage, overrides json.RawMessage) (bool, error) {
	patch, err := parseRequestOverrides(overrides)
	if err != nil || len(patch) == 0 {
		return false, err
	}
	changed := false
	for key, value := range patch {
		if existing, ok := fields[key]; ok && bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(value)) {
			continue
		}
		// 复制一份：覆盖项与请求字段表可能共享底层数组，避免调用方后续修改互相影响。
		fields[key] = json.RawMessage(append([]byte(nil), value...))
		changed = true
	}
	return changed, nil
}

// ApplyRequestOverrides 在已编码的 JSON 对象请求体上按顶层键覆盖合并覆盖项。
//
// 供跨协议重建路径使用：重建产物是字节，与同协议透传路径共用一个顶层覆盖语义。
// overrides 为空时逐字节返回入参；发生改写时返回重新编码的请求体。
func ApplyRequestOverrides(body []byte, overrides json.RawMessage) ([]byte, bool, error) {
	patch, err := parseRequestOverrides(overrides)
	if err != nil {
		return nil, false, err
	}
	if len(patch) == 0 {
		return body, false, nil
	}
	fields, err := DecodeRawFields(body)
	if err != nil {
		return nil, false, err
	}
	changed, err := MergeRawOverrides(fields, overrides)
	if err != nil {
		return nil, false, err
	}
	if !changed {
		return body, false, nil
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, false, NewError(CodeInternal, "编码覆盖后的请求失败").WithCause(err)
	}
	return encoded, true, nil
}

// parseRequestOverrides 把覆盖项解析为顶层字段表。
//
// 空值与 JSON null 视为未配置（返回 nil, nil）；不是 JSON 对象时返回错误。
func parseRequestOverrides(overrides json.RawMessage) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(overrides)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &patch); err != nil {
		return nil, errors.New("请求覆盖项不是合法 JSON 对象")
	}
	if patch == nil {
		return nil, errors.New("请求覆盖项不是合法 JSON 对象")
	}
	return patch, nil
}

// RewritePart 标识一次转发中被网关改写过的报文部分。
//
// 它让「网关动过哪些部分」在尝试记录与日志中可审计，而不是只留一个布尔。
//
// 请求侧与响应侧的改写项都在此登记：请求侧（输出上限、索取用量开关、模型名）
// 由适配器随产物回报，响应侧（响应被重新编码、用量被抑制）由流水线的非流式写出
// 分支或流式下沉目标上报，两者合并后写入 AttemptRecord.RewrittenParts。
type RewritePart string

const (
	// RewritePartRequestOutputLimit 表示上游请求的输出上限被网关改写：
	// 客户端声明值超限被钳制，或客户端未声明被渠道上限补齐。
	RewritePartRequestOutputLimit RewritePart = "request_output_limit"
	// RewritePartRequestUsageSwitch 表示上游请求被网关补上索取用量的开关。
	// 只有支持该开关的协议（当前是 OpenAI Chat Completions）会产生本取值。
	RewritePartRequestUsageSwitch RewritePart = "request_usage_switch"
	// RewritePartRequestModel 表示上游请求的模型名被网关改写：
	// 用渠道对应的上游模型名替换或补齐客户端请求里的模型名。
	RewritePartRequestModel RewritePart = "request_model"
	// RewritePartRequestOverrides 表示上游请求体被渠道 × 模型的覆盖项改写。
	// 只有确实合并进至少一个顶层键时才产生本取值。
	RewritePartRequestOverrides RewritePart = "request_overrides"
	// RewritePartResponseReencoded 表示面向客户端的响应由网关按客户端协议重新编码。
	// 两个生产者：非流式跨协议重建分支；流式重建下沉目标在确实写出过网关编码字节时。
	// 同协议透传的两条路径（非流式逐字节写出上游原始字节、流式按帧原样下沉）均不产生本取值。
	RewritePartResponseReencoded RewritePart = "response_reencoded"
)

// RewriteParts 是一次转发中被网关改写过的报文部分集合；零值表示未改动任何部分。
type RewriteParts []RewritePart

// Has 报告集合中是否包含指定部分。
func (p RewriteParts) Has(part RewritePart) bool {
	for _, item := range p {
		if item == part {
			return true
		}
	}
	return false
}
