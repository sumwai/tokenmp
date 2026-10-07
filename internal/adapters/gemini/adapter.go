package gemini

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
)

// 客户端端点形态与上游端点形态的固定字面量。
//
// 客户端端点是 /v1beta/models/{model}:generateContent 形态，模型名与流式后缀都在路径上；
// 上游相对渠道根地址的拼接段是 /models/{model}:generateContent，
// 流式额外带 ?alt=sse 让上游以 SSE 帧下发。两处差异都只在本包表达。
const (
	clientPathPrefix = "/v1beta/models/"
	generateMethod   = "generateContent"
	streamMethod     = "streamGenerateContent"
	// streamQuery 是流式端点要求的查询参数：alt=sse 让上游逐帧下发而不是返回 JSON 数组流。
	streamQuery = "alt=sse"

	roleUser  = "user"
	roleModel = "model"
)

// Gemini 的 finishReason 字面量。
const (
	finishStop        = "STOP"
	finishMaxTokens   = "MAX_TOKENS"
	finishSafety      = "SAFETY"
	finishRecitation  = "RECITATION"
	finishBlocklist   = "BLOCKLIST"
	finishProhibited  = "PROHIBITED_CONTENT"
	finishSpii        = "SPII"
	finishImageSafety = "IMAGE_SAFETY"
)

// toolConfig.functionCallingConfig.mode 的取值。
const (
	callModeAuto  = "AUTO"
	callModeAny   = "ANY"
	callModeNone  = "NONE"
	callModeUnset = "MODE_UNSPECIFIED"
)

const (
	contentTypeJSON = "application/json"
	contentTypeSSE  = "text/event-stream"

	// jsonNullLiteral 是 JSON null 的文本表示，用于判断可选字段是否显式给出。
	jsonNullLiteral = "null"
)

// Adapter 实现 domain.Adapter 与 domain.EndpointFormat。
//
// 非流式编解码与错误编码是无状态实现。流式解码需要跨帧累计结束原因与用量，
// 流式编码需要缓存工具调用分片（Gemini 的 functionCall 是完整对象，不能逐片下发），
// 因此一次流必须使用一个独立实例，不同并发流不得共享实例：NewStream 派生这样一个实例。
type Adapter struct {
	// decodeMu 保护解码方向的跨帧累计状态，兜底并发误用同一实例造成的数据竞争。
	decodeMu sync.Mutex
	decoder  streamDecoder

	// encodeMu 保护编码方向的工具调用缓存，兜底并发误用。
	encodeMu sync.Mutex
	toolBuf  map[int]*bufferedToolCall
}

var (
	_ domain.Adapter        = (*Adapter)(nil)
	_ domain.EndpointFormat = (*Adapter)(nil)
)

// New 返回一个适配器实例。
func New() *Adapter {
	return &Adapter{}
}

// NewStream 派生一个只服务单次流式响应的新适配器实例。
//
// 流式解码与流式编码都持有一轮内的累计状态，返回自身会让两条并发流互相覆盖，
// 因此本方法每次返回全新实例，不继承任何流式状态。
func (a *Adapter) NewStream() domain.Adapter {
	return &Adapter{}
}

// Protocol 返回本适配器对应的协议。
func (a *Adapter) Protocol() domain.Protocol {
	return domain.ProtocolGeminiGenerate
}

// ContentType 返回非流式响应的 Content-Type。
func (a *Adapter) ContentType() string {
	return contentTypeJSON
}

// StreamContentType 返回流式响应的 Content-Type。
func (a *Adapter) StreamContentType() string {
	return contentTypeSSE
}

// ---------------------------------------------------------------------------
// 端点格式（domain.EndpointFormat）
// ---------------------------------------------------------------------------

// ClientPathPrefix 返回客户端端点前缀：以它开头的路径由本协议处理。
func (a *Adapter) ClientPathPrefix() string {
	return clientPathPrefix
}

// MatchClientPath 报告 path 是否属于本协议，并返回路径携带的模型名与流式形态。
//
// 客户端路径形如 /v1beta/models/{model}:generateContent；流式取 :streamGenerateContent。
// 查询串（例如 alt=sse）不参与判定，模型名里允许出现 / 以承载 Vertex 的
// publishers/google/models/{model} 形态。路径不是该端点形态时返回 false，
// 由装配层继续匹配其它协议。
func (a *Adapter) MatchClientPath(path string) (string, bool, bool) {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	rest, ok := strings.CutPrefix(path, clientPathPrefix)
	if !ok {
		return "", false, false
	}
	model, method, ok := strings.Cut(rest, ":")
	if !ok || strings.TrimSpace(model) == "" {
		return "", false, false
	}
	switch method {
	case generateMethod:
		return model, false, true
	case streamMethod:
		return model, true, true
	default:
		return "", false, false
	}
}

// UpstreamPath 返回上游端点相对渠道根地址的拼接段。
//
// 非流式为 /models/{model}:generateContent，流式追加 ?alt=sse。model 为空时不拼端点，
// 返回空串让调用方沿用根地址；空模型发出去的请求必然 404，不在此处发明拒绝。
func (a *Adapter) UpstreamPath(model string, stream bool) string {
	if strings.TrimSpace(model) == "" {
		return ""
	}
	if stream {
		return "/models/" + model + ":" + streamMethod + "?" + streamQuery
	}
	return "/models/" + model + ":" + generateMethod
}

// ---------------------------------------------------------------------------
// 请求解码
// ---------------------------------------------------------------------------

// DecodeRequest 把 generateContent 请求体解码为内部统一请求。
//
// 模型名与流式形态由客户端路径携带（见 MatchClientPath），请求体里没有这两个字段，
// 故此处 Model 留空、Stream 恒为 false，由入口层用路径解析结果补齐。
func (a *Adapter) DecodeRequest(body []byte) (*domain.Request, error) {
	var wire wireRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, invalidRequest("请求体不是合法 JSON").WithCause(err)
	}
	if len(wire.Contents) == 0 {
		return nil, invalidRequest("缺少必填字段 contents")
	}

	tracker := &toolIDTracker{}
	messages := make([]domain.Message, 0, len(wire.Contents)+1)
	systemParts, err := decodeSystemInstruction(wire.SystemInstruction)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeContents(wire.Contents, tracker)
	if err != nil {
		return nil, err
	}
	if len(decoded) == 0 {
		return nil, invalidRequest("contents 中没有可解码的消息")
	}
	if len(systemParts) > 0 {
		messages = append(messages, domain.Message{Role: domain.RoleSystem, Parts: systemParts})
	}
	messages = append(messages, decoded...)

	toolChoice, err := decodeToolChoice(wire.ToolConfig)
	if err != nil {
		return nil, err
	}
	generation, err := decodeGenerationConfig(wire.GenerationConfig)
	if err != nil {
		return nil, err
	}
	maxTokens := 0
	if generation.MaxOutputTokens != nil {
		maxTokens = *generation.MaxOutputTokens
	}

	req := &domain.Request{
		Protocol:    domain.ProtocolGeminiGenerate,
		Messages:    messages,
		Tools:       decodeTools(wire.Tools),
		ToolChoice:  toolChoice,
		MaxTokens:   maxTokens,
		Temperature: generation.Temperature,
		// RawBody 必须拷贝：所有权归 Request，调用方后续复用或改写传入的 body
		// 不得影响已解码请求（RawBody 会被协议回退重放）。
		RawBody: bytes.Clone(body),
	}

	a.decodeMu.Lock()
	a.decoder.reset()
	a.decodeMu.Unlock()
	return req, nil
}

// decodeSystemInstruction 把顶层 systemInstruction 归一化为 system 消息的片段。
//
// systemInstruction 只承载文本；图片、工具结果等片段无法用 system 消息表达，按不可编码处理。
func decodeSystemInstruction(instruction *wireContent) ([]domain.Part, error) {
	if instruction == nil {
		return nil, nil
	}
	parts := make([]domain.Part, 0, len(instruction.Parts))
	for i, part := range instruction.Parts {
		if part.Text == "" {
			return nil, invalidRequest(fmt.Sprintf("systemInstruction 第 %d 个片段不是文本", i))
		}
		parts = append(parts, domain.Part{Kind: domain.PartText, Text: part.Text})
	}
	return parts, nil
}

// decodeContents 把 contents 数组解码为内部消息。
//
// Gemini 只有 user 与 model 两种角色，模型角色映射为内部 assistant；角色缺失按 user 处理
// （Gemini 的默认语义）。未识别的角色与只含未识别片段的 entry 跳过，跳过全部时由调用方报错。
func decodeContents(contents []wireContent, tracker *toolIDTracker) ([]domain.Message, error) {
	messages := make([]domain.Message, 0, len(contents))
	for _, content := range contents {
		role, ok := decodeRole(content.Role)
		if !ok {
			continue
		}
		parts, _, err := convertParts(content.Parts, false, tracker)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			// 结构上的空 entry 与只含未识别片段的 entry 都跳过：前者无法编码成合法消息，
			// 后者内部格式表达不了，都不该让整条请求失败。
			continue
		}
		messages = append(messages, domain.Message{Role: role, Parts: parts})
	}
	return messages, nil
}

// decodeRole 把 Gemini 角色映射为内部角色。第二个返回值为 false 表示角色未识别，跳过该条。
func decodeRole(role string) (domain.Role, bool) {
	switch strings.TrimSpace(role) {
	case roleUser, "":
		return domain.RoleUser, true
	case roleModel:
		return domain.RoleAssistant, true
	default:
		return "", false
	}
}

// convertParts 把内容片段归一化为统一片段。
//
// decodeReasoning 为 true（响应方向）时把 thought 文本建模为 PartReasoning；
// 为 false（请求方向）时与未见过的片段类型一样跳过。已知类型缺少必填字段仍返回错误。
func convertParts(parts []wirePart, decodeReasoning bool, tracker *toolIDTracker) ([]domain.Part, bool, error) {
	out := make([]domain.Part, 0, len(parts))
	skipped := false
	for _, part := range parts {
		switch {
		case part.Text != "":
			if part.Thought {
				if !decodeReasoning {
					skipped = true
					continue
				}
				out = append(out, domain.Part{Kind: domain.PartReasoning, Text: part.Text})
				continue
			}
			out = append(out, domain.Part{Kind: domain.PartText, Text: part.Text})
		case part.InlineData != nil:
			if part.InlineData.MimeType == "" || part.InlineData.Data == "" {
				return nil, false, invalidRequest("inlineData 缺少 mimeType 或 data")
			}
			out = append(out, domain.Part{
				Kind:     domain.PartImage,
				ImageURL: "data:" + part.InlineData.MimeType + ";base64," + part.InlineData.Data,
			})
		case part.FileData != nil:
			if part.FileData.FileURI == "" {
				return nil, false, invalidRequest("fileData 缺少 fileUri")
			}
			out = append(out, domain.Part{Kind: domain.PartImage, ImageURL: part.FileData.FileURI})
		case part.FunctionCall != nil:
			if part.FunctionCall.Name == "" {
				return nil, false, invalidRequest("functionCall 缺少 name")
			}
			args, err := compactJSONObject(part.FunctionCall.Args)
			if err != nil {
				return nil, false, err
			}
			out = append(out, domain.Part{
				Kind: domain.PartToolCall,
				ToolCall: &domain.ToolCall{
					ID:        tracker.next(part.FunctionCall.Name),
					Name:      part.FunctionCall.Name,
					Arguments: args,
				},
			})
		case part.FunctionResponse != nil:
			if part.FunctionResponse.Name == "" {
				return nil, false, invalidRequest("functionResponse 缺少 name")
			}
			content, err := compactJSONValue(part.FunctionResponse.Response)
			if err != nil {
				return nil, false, err
			}
			out = append(out, domain.Part{
				Kind: domain.PartToolResult,
				ToolResult: &domain.ToolResult{
					ToolCallID: tracker.resolve(part.FunctionResponse.Name),
					Content:    content,
				},
			})
		default:
			// 未识别的片段类型（executableCode、codeExecutionResult 等）：跳过该片段并继续。
			skipped = true
		}
	}
	return out, skipped, nil
}

// decodeTools 把顶层 tools 归一化为内部工具定义。
func decodeTools(tools []wireTool) []domain.ToolSpec {
	out := make([]domain.ToolSpec, 0, len(tools))
	for _, tool := range tools {
		for _, decl := range tool.FunctionDeclarations {
			if strings.TrimSpace(decl.Name) == "" {
				continue
			}
			spec := domain.ToolSpec{Name: decl.Name, Description: decl.Description}
			if parameters, err := compactJSONObject(decl.Parameters); err == nil {
				spec.ParametersJSON = parameters
			}
			out = append(out, spec)
		}
	}
	return out
}

// decodeToolChoice 把 functionCallingConfig 映射为内部工具选择。
//
// ANY 且只允许一个函数名时映射为指定工具，否则映射为「必须调用至少一个工具」。
// 未识别的模式报错：静默丢弃客户端的工具选择要求会让上游按自己的默认策略执行。
func decodeToolChoice(config *wireToolConfig) (domain.ToolChoice, error) {
	if config == nil || config.FunctionCallingConfig == nil {
		return domain.ToolChoice{}, nil
	}
	cfg := config.FunctionCallingConfig
	switch strings.ToUpper(strings.TrimSpace(cfg.Mode)) {
	case "", callModeUnset:
		return domain.ToolChoice{}, nil
	case callModeAuto:
		return domain.ToolChoice{Mode: domain.ToolChoiceAuto}, nil
	case callModeNone:
		return domain.ToolChoice{Mode: domain.ToolChoiceNone}, nil
	case callModeAny:
		if len(cfg.AllowedFunctionNames) == 1 && strings.TrimSpace(cfg.AllowedFunctionNames[0]) != "" {
			return domain.ToolChoice{Mode: domain.ToolChoiceTool, Name: cfg.AllowedFunctionNames[0]}, nil
		}
		return domain.ToolChoice{Mode: domain.ToolChoiceRequired}, nil
	default:
		return domain.ToolChoice{}, invalidRequest(fmt.Sprintf("toolConfig 模式 %q 不受支持", cfg.Mode))
	}
}

// decodeGenerationConfig 读取本适配器建模的生成参数。
//
// 形状非对象时报 invalid_request；未建模的字段（topP、stopSequences 等）忽略。
func decodeGenerationConfig(raw json.RawMessage) (wireGenerationConfig, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == jsonNullLiteral {
		return wireGenerationConfig{}, nil
	}
	if trimmed[0] != '{' {
		return wireGenerationConfig{}, invalidRequest("generationConfig 必须是 JSON 对象")
	}
	var config wireGenerationConfig
	if err := json.Unmarshal(trimmed, &config); err != nil {
		return wireGenerationConfig{}, invalidRequest("generationConfig 不是合法对象").WithCause(err)
	}
	if config.MaxOutputTokens != nil && *config.MaxOutputTokens < 0 {
		return wireGenerationConfig{}, invalidRequest("maxOutputTokens 不得为负")
	}
	return config, nil
}

// ---------------------------------------------------------------------------
// 响应编解码
// ---------------------------------------------------------------------------

// DecodeResponse 把上游非流式响应体归一化为统一响应。
//
// 上游可能用 2xx 回错误体（{"error":{...}}）或空 candidates：前者按上游错误分级返回，
// 后者只在 promptFeedback.blockReason 给出时才判为内容拦截，否则视作无法识别的响应。
func (a *Adapter) DecodeResponse(body []byte) (*domain.Response, error) {
	var wire wireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, invalidRequest("响应体不是合法 JSON").WithCause(err)
	}
	if wire.Error != nil {
		return nil, upstreamError(wire.Error)
	}
	if len(wire.Candidates) == 0 {
		if wire.PromptFeedback != nil && wire.PromptFeedback.BlockReason != "" {
			// 内容被上游前置拦截：没有候选可回，按内容拦截结束原因建模，
			// 用量仍取上游给出的 usageMetadata。
			return &domain.Response{
				Model:        wire.ModelVersion,
				Message:      domain.Message{Role: domain.RoleAssistant},
				FinishReason: domain.FinishContentFilter,
				Usage:        decodeUsage(wire.UsageMetadata),
			}, nil
		}
		return nil, invalidRequest("响应缺少 candidates")
	}
	tracker := &toolIDTracker{}
	parts, _, err := convertParts(wire.Candidates[0].Content.Parts, true, tracker)
	if err != nil {
		return nil, err
	}
	return &domain.Response{
		Model:        wire.ModelVersion,
		Message:      domain.Message{Role: domain.RoleAssistant, Parts: parts},
		FinishReason: decodeFinishReason(wire.Candidates[0].FinishReason),
		Usage:        decodeUsage(wire.UsageMetadata),
	}, nil
}

// EncodeResponse 把统一响应编码为 Gemini 非流式响应体。
func (a *Adapter) EncodeResponse(resp *domain.Response) ([]byte, error) {
	if resp == nil {
		return nil, domain.NewError(domain.CodeInternal, "响应为空")
	}
	parts, err := encodeParts(resp.Message.Parts)
	if err != nil {
		return nil, err
	}
	wire := wireResponse{
		Candidates: []wireCandidate{{
			Content:      wireContent{Role: roleModel, Parts: parts},
			FinishReason: encodeFinishReason(resp.FinishReason),
			Index:        0,
		}},
		UsageMetadata: encodeUsage(resp.Usage),
		ModelVersion:  resp.Model,
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, domain.NewError(domain.CodeInternal, "编码响应失败").WithCause(err)
	}
	return body, nil
}

// encodeParts 把统一片段编码为 Gemini 内容片段。
//
// 推理片段不下发（与其它适配器一致）；工具结果不会出现在响应方向，出现即按不可编码处理。
func encodeParts(parts []domain.Part) ([]wirePart, error) {
	out := make([]wirePart, 0, len(parts))
	for i, part := range parts {
		switch part.Kind {
		case domain.PartText:
			out = append(out, wirePart{Text: part.Text})
		case domain.PartToolCall:
			if part.ToolCall == nil {
				return nil, domain.NewError(domain.CodeInternal, fmt.Sprintf("第 %d 个片段缺少工具调用", i))
			}
			args, err := compactJSONObjectString(part.ToolCall.Arguments)
			if err != nil {
				return nil, err
			}
			out = append(out, wirePart{FunctionCall: &wireFunctionCall{
				Name: part.ToolCall.Name,
				Args: json.RawMessage(args),
			}})
		case domain.PartReasoning:
			// 推理内容不下发，显式跳过。
		default:
			return nil, domain.NewError(domain.CodeInternal,
				fmt.Sprintf("响应片段类型 %q 无法编码为 Gemini 内容", string(part.Kind)))
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 结束原因与用量
// ---------------------------------------------------------------------------

// decodeFinishReason 把 Gemini 结束原因映射为内部枚举。未识别字面量一律归为 FinishUnknown。
func decodeFinishReason(literal string) domain.FinishReason {
	switch strings.ToUpper(strings.TrimSpace(literal)) {
	case finishStop:
		return domain.FinishStop
	case finishMaxTokens:
		return domain.FinishLength
	case finishSafety, finishRecitation, finishBlocklist, finishProhibited, finishSpii, finishImageSafety:
		return domain.FinishContentFilter
	default:
		return domain.FinishUnknown
	}
}

// encodeFinishReason 把内部结束原因映射为 Gemini 字面量。
//
// 工具调用在 Gemini 里没有独立的结束原因（functionCall 是内容片段，结束原因仍是 STOP），
// 故 FinishToolCalls 映射为 STOP。FinishUnknown 没有对应字面量，返回空串省略该字段。
func encodeFinishReason(reason domain.FinishReason) string {
	switch reason {
	case domain.FinishStop, domain.FinishToolCalls:
		return finishStop
	case domain.FinishLength:
		return finishMaxTokens
	case domain.FinishContentFilter:
		return finishSafety
	default:
		return ""
	}
}

// decodeUsage 把 usageMetadata 归一化为内部用量。
//
// promptTokenCount 是输入总数、candidatesTokenCount 是输出总数，
// cachedContentTokenCount 与 thoughtsTokenCount 分别是输入与输出的子项，不再叠加进主计数。
// 上游未给出 usageMetadata 时返回零值（来源未知的「未取得用量」）。
func decodeUsage(wire *wireUsage) domain.Usage {
	if wire == nil {
		return domain.Usage{}
	}
	usage := domain.Usage{
		Source:          domain.UsageSourceUpstream,
		InputTokens:     wire.PromptTokenCount,
		OutputTokens:    wire.CandidatesTokenCount,
		CacheReadTokens: wire.CachedContentTokenCount,
		ReasoningTokens: wire.ThoughtsTokenCount,
	}
	return usage.BoundSubitemsToMain()
}

// encodeUsage 把内部用量映射为 usageMetadata。
//
// totalTokenCount 取输入与输出之和；子项为零时不写出，避免制造没有信息的零值字段。
func encodeUsage(usage domain.Usage) *wireUsage {
	wire := &wireUsage{
		PromptTokenCount:     usage.InputTokens,
		CandidatesTokenCount: usage.OutputTokens,
		TotalTokenCount:      usage.InputTokens + usage.OutputTokens,
	}
	if usage.CacheReadTokens != 0 {
		wire.CachedContentTokenCount = usage.CacheReadTokens
	}
	if usage.ReasoningTokens != 0 {
		wire.ThoughtsTokenCount = usage.ReasoningTokens
	}
	return wire
}

// ---------------------------------------------------------------------------
// 错误编码
// ---------------------------------------------------------------------------

// EncodeError 把错误编码为 Gemini 错误响应体与 HTTP 状态码。
func (a *Adapter) EncodeError(err error) (int, []byte) {
	status := domain.HTTPStatus(err)
	grpcStatus := "INTERNAL"
	message := "服务内部错误"
	if e := domain.AsError(err); e != nil {
		grpcStatus = grpcStatusForCode(e.Code)
		if e.Message != "" {
			message = e.Message
		}
	}
	body, marshalErr := json.Marshal(errorEnvelope{Error: wireError{
		Code:    status,
		Message: message,
		Status:  grpcStatus,
	}})
	if marshalErr != nil {
		// 结构体只含整数与字符串且序列化不会失败，此处兜底避免返回空 body。
		return status, []byte(`{"error":{"code":500,"message":"服务内部错误","status":"INTERNAL"}}`)
	}
	return status, body
}

// grpcStatusForCode 把统一错误码映射为 Gemini 的 gRPC 状态名。
func grpcStatusForCode(code domain.Code) string {
	switch code {
	case domain.CodeInvalidRequest:
		return "INVALID_ARGUMENT"
	case domain.CodeUnauthorized:
		return "UNAUTHENTICATED"
	case domain.CodeForbidden:
		return "PERMISSION_DENIED"
	case domain.CodeModelNotFound, domain.CodeNotFound:
		return "NOT_FOUND"
	case domain.CodeRateLimited, domain.CodeQuotaExceeded, domain.CodeGatewayOverloaded, domain.CodeUpstreamRateLimited:
		return "RESOURCE_EXHAUSTED"
	case domain.CodeUpstreamTimeout:
		return "DEADLINE_EXCEEDED"
	case domain.CodeUpstreamUnavailable, domain.CodeUpstreamRejected:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}

// upstreamGRPCClass 把上游 gRPC 状态名映射为失败类别。
//
// Gemini 用 gRPC 状态名而不是 HTTP 语义表达错误，且认证失败也用 400 配
// UNAUTHENTICATED 表达，只看 HTTP 状态码会把「该换 key」误判成「请求有问题」。
func upstreamGRPCClass(status string) failure.Class {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "OUT_OF_RANGE", "UNIMPLEMENTED", "NOT_FOUND":
		return failure.ClassRequest
	case "UNAUTHENTICATED", "PERMISSION_DENIED":
		return failure.ClassAuth
	case "RESOURCE_EXHAUSTED":
		return failure.ClassRateLimit
	case "DEADLINE_EXCEEDED":
		return failure.ClassTimeout
	default:
		// UNAVAILABLE 与认不出的状态名：上游侧故障，换渠道可能恢复。
		// 调用方在归类后还会用报文措辞兜底一次。
		return failure.ClassUpstream
	}
}

// upstreamError 把上游错误体转为统一错误。
//
// 分类交给 upstreamGRPCClass，处置由类别策略表给出：请求级与模型不可用不重试，
// 凭据级换 key，资源耗尽与超时换渠道。
// 上游原文只进 Detail，不作为面向用户的 Message。
func upstreamError(wire *wireError) error {
	if wire == nil {
		return failure.NewError(domain.CodeUpstreamUnavailable, "上游返回错误", "", failure.ClassUpstream)
	}
	class := upstreamGRPCClass(wire.Status)
	// 状态名没给出可用线索时退回报文措辞：部分渠道只用 message 表达原因。
	// 有结构化状态名时不用措辞——那比自然语言可靠。
	if class == failure.ClassUpstream {
		if byWording := failure.ClassifyWording(wire.Message); byWording != failure.ClassOther {
			class = byWording
		}
	}
	detail := fmt.Sprintf("上游错误 code=%d status=%q", wire.Code, wire.Status)
	if message := strings.TrimSpace(wire.Message); message != "" {
		detail += " message=" + message
	}
	return failure.NewError(failure.CodeForClass(class), "上游返回错误", detail, class)
}

// ---------------------------------------------------------------------------
// 工具调用 id 匹配
// ---------------------------------------------------------------------------

// toolIDTracker 在单次解码里为 Gemini 的工具调用合成内部 id。
//
// Gemini 的 functionCall 不带 id，functionResponse 只按函数名关联；
// 内部格式要求工具调用与结果各带一个可对齐的 id，故此处按出现顺序合成，
// 并记住尚未被应答的调用以便把结果对上。
type toolIDTracker struct {
	seq     int
	pending []toolRef
}

type toolRef struct {
	name string
	id   string
}

// next 为一个新的工具调用合成 id 并记下它等待应答。
func (t *toolIDTracker) next(name string) string {
	t.seq++
	id := fmt.Sprintf("call_%d", t.seq)
	t.pending = append(t.pending, toolRef{name: name, id: id})
	return id
}

// resolve 返回同名且尚未被应答的工具调用 id，并把它从等待集合里移除。
//
// 找不到对应调用时仍合成一个新 id：让内部格式保持自洽，是否合法交由上游判定，
// 不在此处发明拒绝。
func (t *toolIDTracker) resolve(name string) string {
	for i, ref := range t.pending {
		if ref.name == name {
			t.pending = append(t.pending[:i], t.pending[i+1:]...)
			return ref.id
		}
	}
	t.seq++
	return fmt.Sprintf("call_%d", t.seq)
}

// compactJSONObject 返回 JSON 对象的紧凑原文；空值返回 "{}"，非对象或非法 JSON 报错。
func compactJSONObject(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == jsonNullLiteral {
		return "{}", nil
	}
	if trimmed[0] != '{' {
		return "", invalidRequest("JSON 字段必须是对象")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "", invalidRequest("JSON 对象不是合法 JSON")
	}
	return compact.String(), nil
}

// compactJSONObjectString 返回工具调用参数字符串的紧凑对象原文；空串返回 "{}"。
func compactJSONObjectString(arguments string) (string, error) {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}", nil
	}
	if trimmed[0] != '{' || !json.Valid([]byte(trimmed)) {
		return "", domain.NewError(domain.CodeInternal, "工具调用参数不是合法 JSON 对象")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(trimmed)); err != nil {
		return "", domain.NewError(domain.CodeInternal, "工具调用参数不是合法 JSON 对象").WithCause(err)
	}
	return compact.String(), nil
}

// compactJSONValue 返回任意 JSON 值的紧凑原文；空值返回 "{}"。
//
// 工具结果的内部表示是字符串，Gemini 的 functionResponse.response 是对象；
// 没有对象可取时给空对象，保证内部表示自洽。
func compactJSONValue(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == jsonNullLiteral {
		return "{}", nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "", invalidRequest("functionResponse.response 不是合法 JSON")
	}
	return compact.String(), nil
}

// invalidRequest 构造 invalid_request 统一错误。
func invalidRequest(message string) *domain.Error {
	return domain.NewError(domain.CodeInvalidRequest, message)
}
