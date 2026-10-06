// Package gemini 实现 Gemini 原生 generateContent 协议
// （POST /v1beta/models/{model}:generateContent 与 :streamGenerateContent?alt=sse）
// 与 internal/domain 统一内部格式之间的双向转换。
//
// 协议差异只能在本包内表达，流水线只依赖 domain.Adapter。本包只依赖标准库与 internal/domain。
//
// 端点形态与既有三方言不同，因此本包额外实现 domain.EndpointFormat：
// 客户端路径与上游路径都含模型名，流式由路径后缀与查询参数表达而不是请求体字段。
// 装配层据此注册客户端端点路径、解析路径里的模型名与流式形态，上游调用据此拼接上游地址；
// 模型名替换因此在路径上完成，请求体里没有 model 字段。
//
// 关于请求 id：Gemini 请求体没有 request_id 字段，故 DecodeRequest 返回的
// Request.RequestID 为空，由调用方注入后再调用 Request.Validate。
package gemini

import (
	"encoding/json"
)

// 本文件定义 Gemini 线协议的 JSON 结构，只列出转换所需字段。
// 解码对未知字段宽容：encoding/json 默认忽略未声明的字段。

// wireRequest 是 generateContent 的请求体。
//
// 模型名不在请求体里：原生 Gemini 由 URL 路径携带，本适配器不改写 body 里的模型字段。
type wireRequest struct {
	Contents          []wireContent   `json:"contents"`
	SystemInstruction *wireContent    `json:"systemInstruction,omitempty"`
	Tools             []wireTool      `json:"tools,omitempty"`
	ToolConfig        *wireToolConfig `json:"toolConfig,omitempty"`
	GenerationConfig  json.RawMessage `json:"generationConfig,omitempty"`
}

// wireGenerationConfig 是请求体里本适配器建模的生成参数。
type wireGenerationConfig struct {
	MaxOutputTokens *int     `json:"maxOutputTokens"`
	Temperature     *float64 `json:"temperature"`
}

type wireContent struct {
	Role  string     `json:"role"`
	Parts []wirePart `json:"parts"`
}

// wirePart 是内容片段。按字段出现情况判定片段类型，与 Gemini 的 oneof 语义一致。
type wirePart struct {
	Text             string                `json:"text,omitempty"`
	Thought          bool                  `json:"thought,omitempty"`
	InlineData       *wireBlob             `json:"inlineData,omitempty"`
	FileData         *wireFileData         `json:"fileData,omitempty"`
	FunctionCall     *wireFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *wireFunctionResponse `json:"functionResponse,omitempty"`
}

type wireBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type wireFileData struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type wireFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type wireFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
}

type wireTool struct {
	FunctionDeclarations []wireFunctionDeclaration `json:"functionDeclarations"`
}

type wireFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireToolConfig struct {
	FunctionCallingConfig *wireFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type wireFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// wireResponse 是 generateContent 的响应体，也用作 SSE 每一帧的 JSON 负载。
type wireResponse struct {
	Candidates     []wireCandidate     `json:"candidates"`
	UsageMetadata  *wireUsage          `json:"usageMetadata,omitempty"`
	ModelVersion   string              `json:"modelVersion,omitempty"`
	PromptFeedback *wirePromptFeedback `json:"promptFeedback,omitempty"`
	Error          *wireError          `json:"error,omitempty"`
}

type wireCandidate struct {
	Content      wireContent `json:"content"`
	FinishReason string      `json:"finishReason,omitempty"`
	Index        int         `json:"index"`
}

type wirePromptFeedback struct {
	BlockReason string `json:"blockReason,omitempty"`
}

// wireUsage 是 usageMetadata。
//
// promptTokenCount 已包含 cachedContentTokenCount，故内部口径的输入总数直接取它、
// 缓存读作为子项；candidatesTokenCount 是输出总数；thoughtsTokenCount 是输出里的推理子项。
type wireUsage struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
}

// wireError 是 Gemini 的错误体，也是 SSE 错误帧的负载。
//
// status 是 gRPC 状态名（INVALID_ARGUMENT、UNAUTHENTICATED 等），code 是 HTTP 状态码。
type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// errorEnvelope 是面向客户端的错误响应体。
type errorEnvelope struct {
	Error wireError `json:"error"`
}
