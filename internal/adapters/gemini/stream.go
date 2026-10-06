package gemini

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sumwai/tokenmp/internal/domain"
)

// streamDecoder 把上游 SSE 的逐帧 JSON 解码为内部统一分片，并跨帧保存累计状态。
//
// 一次上游流使用一个独立实例（经 Adapter.NewStream 派生），由 Adapter.decodeMu 兜底保护。
type streamDecoder struct {
	model        string
	finishReason domain.FinishReason
	// finishSeen 记录本轮是否观察到 candidates[].finishReason。它是 Gemini 的结束信号：
	// 从未观察到就 EOF 属真截断，Adapter.FinishStream 不得产出结束分片。
	finishSeen bool
	usage      *domain.Usage
	// ended 记录本轮是否已产出结束分片，保证 FinishStream 幂等。
	ended bool
}

// reset 清空一轮流的解码状态。
func (d *streamDecoder) reset() {
	*d = streamDecoder{}
}

// finish 在读取循环退出后给出收尾分片。
//
// 观察到结束原因时产出唯一的 ChunkStreamEnd（携带累计用量与结束原因），否则返回空，
// 由调用方按截断处理。用量对象恒非 nil，未取得用量时是来源未知的零值。
func (d *streamDecoder) finish() []domain.Chunk {
	if d.ended || !d.finishSeen {
		return nil
	}
	d.ended = true
	usage := d.usage
	if usage == nil {
		usage = &domain.Usage{}
	}
	return []domain.Chunk{{
		Kind:         domain.ChunkStreamEnd,
		Model:        d.model,
		Usage:        usage,
		FinishReason: d.finishReason,
	}}
}

// DecodeStreamFrame 把上游一帧 SSE 解码为内部统一分片。
//
// Gemini 的 SSE 帧没有事件名，data 为完整的 wireResponse JSON 片段。返回空切片表示该帧
// 只更新状态（例如只带 usageMetadata 的帧）。结束分片不在帧内产出：结束原因先累积，
// 由 FinishStream 在 EOF 处一并交付，使「用量帧可能晚于结束原因到达」不丢数据。
func (a *Adapter) DecodeStreamFrame(_ string, data []byte) ([]domain.Chunk, error) {
	a.decodeMu.Lock()
	defer a.decodeMu.Unlock()

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	// 上一轮已产出结束分片时，后到的数据属新的一轮（实例顺序复用时），先复位累计状态。
	if a.decoder.ended {
		a.decoder.reset()
	}
	var wire wireResponse
	if err := json.Unmarshal(trimmed, &wire); err != nil {
		return nil, invalidRequest("上游流式帧不是合法 JSON").WithCause(err)
	}
	if wire.Error != nil {
		return nil, upstreamError(wire.Error)
	}
	if wire.ModelVersion != "" {
		a.decoder.model = wire.ModelVersion
	}
	if wire.UsageMetadata != nil {
		usage := decodeUsage(wire.UsageMetadata)
		a.decoder.usage = &usage
	}
	chunks := make([]domain.Chunk, 0, 1)
	for _, candidate := range wire.Candidates {
		if reason := decodeFinishReason(candidate.FinishReason); candidate.FinishReason != "" {
			a.decoder.finishReason = reason
			a.decoder.finishSeen = true
		}
		texts, calls, err := convertStreamParts(candidate.Content.Parts)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, texts...)
		chunks = append(chunks, calls...)
	}
	if len(chunks) == 0 {
		return nil, nil
	}
	return chunks, nil
}

// convertStreamParts 把流式帧的内容片段转成增量分片。
//
// 文本按 thought 标记分成推理增量与正文增量；functionCall 是完整对象（Gemini 不切片下发参数），
// 故一次给出完整的工具调用分片，下标按同一帧内的出现顺序。
func convertStreamParts(parts []wirePart) ([]domain.Chunk, []domain.Chunk, error) {
	var texts []domain.Chunk
	var calls []domain.Chunk
	for _, part := range parts {
		switch {
		case part.Text != "":
			if part.Thought {
				texts = append(texts, domain.Chunk{Kind: domain.ChunkReasoningDelta, TextDelta: part.Text})
				continue
			}
			texts = append(texts, domain.Chunk{Kind: domain.ChunkTextDelta, TextDelta: part.Text})
		case part.FunctionCall != nil:
			if part.FunctionCall.Name == "" {
				return nil, nil, invalidRequest("functionCall 缺少 name")
			}
			args, err := compactJSONObject(part.FunctionCall.Args)
			if err != nil {
				return nil, nil, err
			}
			calls = append(calls, domain.Chunk{
				Kind: domain.ChunkToolCallDelta,
				ToolCall: &domain.ToolCall{
					Index:     len(calls),
					Name:      part.FunctionCall.Name,
					Arguments: args,
				},
			})
		default:
			// 未识别片段跳过，与响应方向同一口径。
		}
	}
	return texts, calls, nil
}

// FinishStream 在 EOF 处给出本轮收尾分片，恒返回至多一个 ChunkStreamEnd。
func (a *Adapter) FinishStream() []domain.Chunk {
	a.decodeMu.Lock()
	defer a.decodeMu.Unlock()
	return a.decoder.finish()
}

// ---------------------------------------------------------------------------
// 流式编码
// ---------------------------------------------------------------------------

// bufferedToolCall 是流式编码期间按下标缓存的工具调用。
//
// Gemini 的 functionCall 是一个完整对象，不能像 OpenAI 那样逐片下发参数，
// 因此参数分片先缓存，到流结束时合成一个完整的 functionCall 片段。
type bufferedToolCall struct {
	name      string
	fragments []string
}

// EncodeStreamStart 返回流开始帧。
//
// Gemini 的 SSE 流没有独立的开始帧，第一帧就是带内容或结束原因的候选帧，故返回 (nil, nil)。
func (a *Adapter) EncodeStreamStart(string) ([]byte, error) {
	return nil, nil
}

// EncodeChunk 把一个内容分片编码为 Gemini SSE 帧。
//
// 文本增量即时下发；工具调用增量先缓存参数分片，到 EncodeStreamEnd 时合成完整 functionCall，
// 因为 Gemini 不接受半个 JSON 对象的 functionCall。推理增量在编码方向不下发，显式跳过。
func (a *Adapter) EncodeChunk(chunk domain.Chunk) ([]byte, error) {
	switch chunk.Kind {
	case domain.ChunkTextDelta:
		payload := wireResponse{
			Candidates: []wireCandidate{{
				Content: wireContent{Role: roleModel, Parts: []wirePart{{Text: chunk.TextDelta}}},
				Index:   0,
			}},
			ModelVersion: chunk.Model,
		}
		return marshalSSE(payload)
	case domain.ChunkToolCallDelta:
		if chunk.ToolCall == nil {
			return nil, domain.NewError(domain.CodeInternal, "工具调用分片缺少 ToolCall")
		}
		a.encodeMu.Lock()
		defer a.encodeMu.Unlock()
		if a.toolBuf == nil {
			a.toolBuf = make(map[int]*bufferedToolCall)
		}
		buffered := a.toolBuf[chunk.ToolCall.Index]
		if buffered == nil {
			buffered = &bufferedToolCall{}
			a.toolBuf[chunk.ToolCall.Index] = buffered
		}
		if buffered.name == "" {
			buffered.name = chunk.ToolCall.Name
		}
		if chunk.ToolCall.Arguments != "" {
			buffered.fragments = append(buffered.fragments, chunk.ToolCall.Arguments)
		}
		return nil, nil
	case domain.ChunkReasoningDelta:
		return nil, nil
	case domain.ChunkUsage, domain.ChunkFinish, domain.ChunkStreamEnd:
		return nil, domain.NewError(domain.CodeInternal,
			fmt.Sprintf("分片类型 %q 必须经 EncodeStreamEnd 下发", string(chunk.Kind)))
	default:
		return nil, domain.NewError(domain.CodeInternal, fmt.Sprintf("未知分片类型 %q", string(chunk.Kind)))
	}
}

// EncodeStreamEnd 把结束分片编码为 Gemini 的收尾帧。
//
// 先把缓存的工具调用按下标升序各出一个完整 functionCall 帧，再出一个带结束原因与
// usageMetadata 的收尾帧。Gemini 的 SSE 流以连接关闭为终点，没有单独的终止帧。
func (a *Adapter) EncodeStreamEnd(chunk domain.Chunk) ([]byte, error) {
	a.encodeMu.Lock()
	defer a.encodeMu.Unlock()

	var buf bytes.Buffer
	if err := a.flushToolCallsLocked(&buf); err != nil {
		return nil, err
	}
	usage := chunk.Usage
	if usage == nil {
		usage = &domain.Usage{}
	}
	payload := wireResponse{
		Candidates: []wireCandidate{{
			Content:      wireContent{Role: roleModel, Parts: []wirePart{}},
			FinishReason: encodeFinishReason(chunk.FinishReason),
			Index:        0,
		}},
		UsageMetadata: encodeUsage(*usage),
		ModelVersion:  chunk.Model,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, domain.NewError(domain.CodeInternal, "编码流式结束帧失败").WithCause(err)
	}
	writeSSEPayload(&buf, data)
	a.toolBuf = nil
	return buf.Bytes(), nil
}

// flushToolCallsLocked 把缓存的工具调用按下标升序各写一个完整的 functionCall 帧。
//
// 缺少工具名的调用无法写出合法帧，返回内部错误；参数分片按到达顺序拼接后必须是合法 JSON 对象。
func (a *Adapter) flushToolCallsLocked(buf *bytes.Buffer) error {
	if len(a.toolBuf) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(a.toolBuf))
	for index := range a.toolBuf {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		buffered := a.toolBuf[index]
		if strings.TrimSpace(buffered.name) == "" {
			return domain.NewError(domain.CodeInternal,
				fmt.Sprintf("工具调用下标 %d 缺少函数名，无法编码为 Gemini 的 functionCall", index))
		}
		args, err := compactJSONObjectString(strings.Join(buffered.fragments, ""))
		if err != nil {
			return err
		}
		payload := wireResponse{
			Candidates: []wireCandidate{{
				Content: wireContent{Role: roleModel, Parts: []wirePart{{
					FunctionCall: &wireFunctionCall{Name: buffered.name, Args: json.RawMessage(args)},
				}}},
				Index: index,
			}},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return domain.NewError(domain.CodeInternal, "编码工具调用帧失败").WithCause(err)
		}
		writeSSEPayload(buf, data)
	}
	return nil
}

// marshalSSE 把负载序列化为一帧 Gemini SSE 字节。
func marshalSSE(payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, domain.NewError(domain.CodeInternal, "编码流式帧失败").WithCause(err)
	}
	var buf bytes.Buffer
	writeSSEPayload(&buf, data)
	return buf.Bytes(), nil
}

// writeSSEPayload 按 `data: <json>\n\n` 写出一帧。Gemini 的 SSE 帧没有事件名。
func writeSSEPayload(buf *bytes.Buffer, payload []byte) {
	buf.WriteString("data: ")
	buf.Write(payload)
	buf.WriteString("\n\n")
}
