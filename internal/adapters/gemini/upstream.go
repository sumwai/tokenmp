package gemini

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/sumwai/tokenmp/internal/domain"
)

// 编译期断言：本适配器实现上游请求构建、上游请求头与流式错误帧三项能力。
var (
	_ domain.UpstreamRequestBuilder = (*Adapter)(nil)
	_ domain.UpstreamHeaderProvider = (*Adapter)(nil)
	_ domain.StreamErrorEncoder     = (*Adapter)(nil)
)

// 请求体重建涉及的字段名。模型名不在其中：Gemini 的模型由 URL 路径携带。
const (
	generationConfigField = "generationConfig"
	maxOutputTokensField  = "maxOutputTokens"
	temperatureField      = "temperature"
)

// upstreamGenerationConfig 是重建上游请求体时使用的 generationConfig 结构。
type upstreamGenerationConfig struct {
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
}

// EncodeRequest 按内部统一格式重建 Gemini generateContent 请求体，用于跨协议转发。
//
// 模型名不写进请求体：上游端点由 URL 路径携带模型（见 EndpointFormat.UpstreamPath），
// 调用方按 domain.UpstreamModelName 取本次实际模型名拼路径。
//
// 第二个返回值报告本次重建的改写项：模型名被替换或补齐记 request_model
// （虽然体现在路径上，但对外部观察者仍是「网关改写了模型名」），
// 输出上限被钳制或补齐记 request_output_limit。Gemini 没有索取用量的请求开关，
// IncludeUsage 不产生标注。
func (a *Adapter) EncodeRequest(req *domain.Request, options domain.RewriteOptions) ([]byte, domain.RewriteParts, error) {
	if req == nil {
		return nil, nil, domain.NewError(domain.CodeInternal, "请求为空")
	}
	system, contents, err := encodeContents(req.Messages)
	if err != nil {
		return nil, nil, err
	}
	if len(contents) == 0 {
		return nil, nil, domain.NewError(domain.CodeInternal, "Gemini generateContent 要求至少一条 content")
	}
	toolConfig, err := encodeToolChoice(req.ToolChoice)
	if err != nil {
		return nil, nil, err
	}
	wire := wireRequest{
		Contents:          contents,
		SystemInstruction: system,
		Tools:             encodeTools(req.Tools),
		ToolConfig:        toolConfig,
	}
	generation, err := encodeGenerationConfig(req, options)
	if err != nil {
		return nil, nil, err
	}
	if generation != nil {
		encoded, marshalErr := json.Marshal(generation)
		if marshalErr != nil {
			return nil, nil, domain.NewError(domain.CodeInternal, "编码 generationConfig 失败").WithCause(marshalErr)
		}
		wire.GenerationConfig = encoded
	}

	var parts domain.RewriteParts
	if domain.UpstreamModelName(req.Model, options) != req.Model {
		parts = append(parts, domain.RewritePartRequestModel)
	}
	if generation != nil && generation.MaxOutputTokens != nil && *generation.MaxOutputTokens != req.MaxTokens {
		parts = append(parts, domain.RewritePartRequestOutputLimit)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, nil, domain.NewError(domain.CodeInternal, "编码上游请求失败").WithCause(err)
	}
	// 覆盖项在重建产物上按顶层键合并；非法覆盖项在选路边界已被丢弃，此处按跳过处理。
	if merged, changed, mergeErr := domain.ApplyRequestOverrides(body, options.RequestOverrides); mergeErr == nil && changed {
		body = merged
		parts = append(parts, domain.RewritePartRequestOverrides)
	}
	return body, parts, nil
}

// encodeGenerationConfig 按统一口径决定 generationConfig。
//
// 输出上限走 domain.OutputLimit 的统一口径：未声明则补齐，已声明且超限则钳制。
// temperature 原样带上。两者都没有时返回 nil，请求体里不出现 generationConfig。
func encodeGenerationConfig(req *domain.Request, options domain.RewriteOptions) (*upstreamGenerationConfig, error) {
	config := &upstreamGenerationConfig{Temperature: req.Temperature}
	if limit := domain.OutputLimit(req.MaxTokens, options.MaxOutputTokens); limit > 0 {
		config.MaxOutputTokens = &limit
	}
	if config.MaxOutputTokens == nil && config.Temperature == nil {
		return nil, nil
	}
	return config, nil
}

// RewriteRawBody 对同协议透传的原始报文做字段级改写。
//
// 改写项只有输出上限（generationConfig.maxOutputTokens）与渠道 × 模型覆盖项：
//   - 模型名由 URL 路径承载，本方法不写请求体里的模型字段；
//   - Gemini 没有索取用量的请求开关，用量随响应固有下发，IncludeUsage 不产生改写。
//
// 第二个返回值按实际发生的变化填报；没有任何改动时逐字节返回入参。
func (a *Adapter) RewriteRawBody(body []byte, options domain.RewriteOptions) ([]byte, domain.RewriteParts, error) {
	if options.MaxOutputTokens == nil && len(options.RequestOverrides) == 0 {
		return body, nil, nil
	}
	fields, err := domain.DecodeRawFields(body)
	if err != nil {
		return nil, nil, err
	}
	var parts domain.RewriteParts
	if changed, rewriteErr := rewriteMaxOutputTokens(fields, options.MaxOutputTokens); rewriteErr != nil {
		return nil, nil, rewriteErr
	} else if changed {
		parts = append(parts, domain.RewritePartRequestOutputLimit)
	}
	// 覆盖项最后合并：它按顶层键整体替换，应在其它改写项之后落地。
	if changed, mergeErr := domain.MergeRawOverrides(fields, options.RequestOverrides); mergeErr == nil && changed {
		parts = append(parts, domain.RewritePartRequestOverrides)
	}
	if len(parts) == 0 {
		return body, nil, nil
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, domain.NewError(domain.CodeInternal, "编码改写后的请求失败").WithCause(err)
	}
	return encoded, parts, nil
}

// rewriteMaxOutputTokens 按统一口径改写 generationConfig.maxOutputTokens，返回是否发生改写。
//
// 该字段嵌在 generationConfig 里，domain.SetRawOutputLimit 只处理顶层字段，
// 故嵌套口径在本包内实现。generationConfig 缺失时新建；字段取值不是整数时不动，交由上游判定。
func rewriteMaxOutputTokens(fields map[string]json.RawMessage, limit *int) (bool, error) {
	if limit == nil || *limit <= 0 {
		return false, nil
	}
	generation := map[string]json.RawMessage{}
	if raw, ok := fields[generationConfigField]; ok && !isJSONNull(raw) {
		if err := json.Unmarshal(raw, &generation); err != nil {
			return false, domain.NewError(domain.CodeInvalidRequest, "generationConfig 必须是 JSON 对象")
		}
	}
	declared := 0
	if raw, ok := generation[maxOutputTokensField]; ok {
		if err := json.Unmarshal(raw, &declared); err != nil {
			// 字段取值不是整数时不动，交由上游判定，不把它当成改写失败。
			return false, nil //nolint:nilerr // 宽容改写：形状非预期时保持原文。
		}
	}
	target := domain.OutputLimit(declared, limit)
	if target == declared {
		return false, nil
	}
	generation[maxOutputTokensField] = json.RawMessage(strconv.Itoa(target))
	encoded, err := json.Marshal(generation)
	if err != nil {
		return false, domain.NewError(domain.CodeInternal, "编码 generationConfig 失败").WithCause(err)
	}
	fields[generationConfigField] = encoded
	return true, nil
}

// isJSONNull 报告原始 JSON 是否为 null。
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// UpstreamHeaders 返回调用上游 generateContent 时应携带的请求头。
//
// 本协议没有内置的必需请求头：凭据由凭据提供者按渠道配置注入 x-goog-api-key
// 或查询参数 key=，故这里只回传渠道配置；nil 入参返回非 nil 的空 map。
func (a *Adapter) UpstreamHeaders(channelHeaders http.Header) http.Header {
	return domain.CloneHeader(channelHeaders)
}

// EncodeStreamError 报告本协议不支持流式错误帧。
//
// Gemini 的 SSE 流没有独立的错误事件：错误在流建立前以非 2xx 状态码返回，
// 流建立后中断只能靠连接关闭表达。因此返回 supported=false，调用方按降级处理。
func (a *Adapter) EncodeStreamError(err error) ([]byte, bool) {
	return nil, false
}

// ---------------------------------------------------------------------------
// 重建：消息与工具
// ---------------------------------------------------------------------------

// encodedContent 是 contents 数组的一条，逐条编码后再合并相邻同角色条目。
type encodedContent struct {
	role  string
	parts []wirePart
}

// encodeContents 把内部消息编码为 systemInstruction 与 contents。
//
// Gemini 只有 user 与 model 两种角色：内部 system 消息上提为顶层 systemInstruction，
// 工具结果（内部 tool 角色或 user 消息里的 tool_result 片段）编码为 user 消息里的
// functionResponse 片段，并按函数名回填工具调用 id 对应的函数名。
func encodeContents(messages []domain.Message) (*wireContent, []wireContent, error) {
	system := &wireContent{}
	callNames := map[string]string{}
	contents := make([]encodedContent, 0, len(messages))
	for i, message := range messages {
		if message.Role == domain.RoleSystem {
			for j, part := range message.Parts {
				if part.Kind != domain.PartText {
					return nil, nil, domain.NewError(domain.CodeInternal,
						fmt.Sprintf("第 %d 条 system 消息第 %d 个片段不是文本，Gemini 无法表达", i, j))
				}
				system.Parts = append(system.Parts, wirePart{Text: part.Text})
			}
			continue
		}
		role := roleForDomain(message.Role)
		parts := make([]wirePart, 0, len(message.Parts))
		for j, part := range message.Parts {
			encoded, isToolResult, err := encodeContentPart(i, j, role, part, callNames)
			if err != nil {
				return nil, nil, err
			}
			if encoded == nil {
				continue
			}
			// 工具结果的应答在 Gemini 里必须放在 user 回合：承载它的消息整体改为 user。
			if isToolResult {
				role = roleUser
			}
			parts = append(parts, *encoded)
		}
		if len(parts) == 0 {
			continue
		}
		if n := len(contents); n > 0 && contents[n-1].role == role {
			contents[n-1].parts = append(contents[n-1].parts, parts...)
			continue
		}
		contents = append(contents, encodedContent{role: role, parts: parts})
	}
	out := make([]wireContent, 0, len(contents))
	for _, item := range contents {
		out = append(out, wireContent{Role: item.role, Parts: item.parts})
	}
	if len(system.Parts) == 0 {
		return nil, out, nil
	}
	return system, out, nil
}

// roleForDomain 把内部角色映射为 Gemini 角色。工具角色的消息整体作 user 回合。
func roleForDomain(role domain.Role) string {
	switch role {
	case domain.RoleAssistant:
		return roleModel
	default:
		return roleUser
	}
}

// encodeContentPart 编码一个内部片段。第二个返回值为 true 表示该片段是工具结果。
func encodeContentPart(index, partIndex int, role string, part domain.Part, callNames map[string]string) (*wirePart, bool, error) {
	switch part.Kind {
	case domain.PartText:
		return &wirePart{Text: part.Text}, false, nil
	case domain.PartImage:
		if part.ImageURL == "" {
			return nil, false, domain.NewError(domain.CodeInternal,
				fmt.Sprintf("第 %d 条消息第 %d 个图片片段缺少 image_url", index, partIndex))
		}
		mediaType, data, ok := splitDataURI(part.ImageURL)
		if ok {
			return &wirePart{InlineData: &wireBlob{MimeType: mediaType, Data: data}}, false, nil
		}
		return &wirePart{FileData: &wireFileData{FileURI: part.ImageURL}}, false, nil
	case domain.PartToolCall:
		if role != roleModel {
			return nil, false, domain.NewError(domain.CodeInternal,
				fmt.Sprintf("第 %d 条消息不是 model 角色，不能携带工具调用", index))
		}
		if part.ToolCall == nil {
			return nil, false, domain.NewError(domain.CodeInternal,
				fmt.Sprintf("第 %d 条消息的工具调用片段缺少内容", index))
		}
		args, err := compactJSONObjectString(part.ToolCall.Arguments)
		if err != nil {
			return nil, false, err
		}
		callNames[part.ToolCall.ID] = part.ToolCall.Name
		return &wirePart{FunctionCall: &wireFunctionCall{
			Name: part.ToolCall.Name,
			Args: json.RawMessage(args),
		}}, false, nil
	case domain.PartToolResult:
		if part.ToolResult == nil {
			return nil, false, domain.NewError(domain.CodeInternal,
				fmt.Sprintf("第 %d 条消息的工具结果片段缺少内容", index))
		}
		name := callNames[part.ToolResult.ToolCallID]
		if name == "" {
			// 找不到对应的工具调用时用调用 id 兜底：是否合法由上游判定，
			// 不在网关侧发明拒绝。
			name = part.ToolResult.ToolCallID
		}
		return &wirePart{FunctionResponse: &wireFunctionResponse{
			Name:     name,
			Response: toolResponseJSON(part.ToolResult.Content),
		}}, true, nil
	case domain.PartReasoning:
		// 推理内容在编码方向不下发。
		return nil, false, nil
	default:
		return nil, false, domain.NewError(domain.CodeInternal,
			fmt.Sprintf("第 %d 条消息包含无法编码的片段类型 %q", index, string(part.Kind)))
	}
}

// toolResponseJSON 把内部工具结果字符串编码为 functionResponse.response 对象。
//
// 内容本身是 JSON 对象时原样使用；否则包一层 {"result": ...}，
// 因为 Gemini 要求该字段是对象而内部只保存字符串。
func toolResponseJSON(content string) json.RawMessage {
	trimmed := strings.TrimSpace(content)
	if trimmed != "" && trimmed[0] == '{' && json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	encoded, err := json.Marshal(map[string]string{"result": content})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

// splitDataURI 拆解 base64 图片的 data URI，返回 media type 与 base64 数据。
// 非 data URI 或缺少 base64 标记时第二个返回值报告失败。
func splitDataURI(uri string) (string, string, bool) {
	rest, ok := strings.CutPrefix(uri, "data:")
	if !ok {
		return "", "", false
	}
	header, data, ok := strings.Cut(rest, ",")
	if !ok {
		return "", "", false
	}
	mediaType, ok := strings.CutSuffix(header, ";base64")
	if !ok || mediaType == "" {
		return "", "", false
	}
	return mediaType, data, true
}

// encodeTools 把内部工具定义编码为 Gemini 的 functionDeclarations。
func encodeTools(tools []domain.ToolSpec) []wireTool {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]wireFunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		declaration := wireFunctionDeclaration{Name: tool.Name, Description: tool.Description}
		if parameters, err := compactJSONObjectString(tool.ParametersJSON); err == nil && parameters != "{}" {
			declaration.Parameters = json.RawMessage(parameters)
		}
		declarations = append(declarations, declaration)
	}
	return []wireTool{{FunctionDeclarations: declarations}}
}

// encodeToolChoice 把内部工具选择编码为 Gemini 的 functionCallingConfig。
//
// 指定具体工具用 ANY 加 allowedFunctionNames 表达；未指定时不携带该字段。
func encodeToolChoice(choice domain.ToolChoice) (*wireToolConfig, error) {
	switch choice.Mode {
	case domain.ToolChoiceUnset:
		return nil, nil
	case domain.ToolChoiceAuto:
		return functionCallingConfig(callModeAuto, nil), nil
	case domain.ToolChoiceNone:
		return functionCallingConfig(callModeNone, nil), nil
	case domain.ToolChoiceRequired:
		return functionCallingConfig(callModeAny, nil), nil
	case domain.ToolChoiceTool:
		name := strings.TrimSpace(choice.Name)
		if name == "" {
			return nil, domain.NewError(domain.CodeInternal, "指定工具时缺少工具名")
		}
		return functionCallingConfig(callModeAny, []string{name}), nil
	default:
		return nil, domain.NewError(domain.CodeInternal,
			fmt.Sprintf("无法编码的工具选择模式 %q", string(choice.Mode)))
	}
}

// functionCallingConfig 组装 toolConfig。
func functionCallingConfig(mode string, names []string) *wireToolConfig {
	return &wireToolConfig{FunctionCallingConfig: &wireFunctionCallingConfig{
		Mode:                 mode,
		AllowedFunctionNames: names,
	}}
}
