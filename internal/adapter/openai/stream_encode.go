package openai

import (
	"encoding/json"
	"fmt"

	"linapi/internal/adapter"
	"linapi/internal/canonical"
)

// NewStreamEncoder 创建 OpenAI SSE 编码器。
func (a *Adapter) NewStreamEncoder() adapter.StreamEncoder {
	return &streamEncoder{}
}

// streamEncoder 把规范事件流编码为 OpenAI 流式 SSE。
//
// 需要跨事件维护状态：OpenAI 每个 chunk 的结构里，工具调用要带上 index，
// 且首片需带 role/id/name。这里把规范 block 索引映射为 OpenAI 的 tool_calls index，
// 并记录哪些工具块已发过首片。
type streamEncoder struct {
	id        string
	model     string
	usage     canonical.Usage
	usageSent bool

	// 规范 block 索引 -> OpenAI tool_calls index
	toolIndex map[int]int
	nextTool  int
	// 已发过首片（带 id/name）的工具 block
	toolStarted map[int]bool
}

func (e *streamEncoder) Encode(event canonical.Event) ([]byte, error) {
	switch event.Type {
	case canonical.EventMessageStart:
		e.id = event.ID
		e.model = event.Model
		e.mergeUsage(event.Usage)
		// OpenAI 首个 chunk 通常发一个仅含 role 的 delta。
		return e.marshalChunk(streamChoice{
			Index: 0,
			Delta: &streamDelta{Role: "assistant"},
		}, nil)

	case canonical.EventBlockStart:
		if event.BlockType == canonical.BlockToolUse && event.Delta != nil {
			// 工具块首片：带 id + name，arguments 留空。
			oaIdx := e.assignToolIndex(event.BlockIndex)
			e.markToolStarted(event.BlockIndex)
			return e.marshalChunk(streamChoice{
				Index: 0,
				Delta: &streamDelta{
					ToolCalls: []streamToolCall{{
						Index:    oaIdx,
						ID:       event.Delta.ToolUseID,
						Type:     "function",
						Function: functionCall{Name: event.Delta.ToolName},
					}},
				},
			}, nil)
		}
		// 文本块开始无需单独输出（OpenAI 无对应事件）。
		return nil, nil

	case canonical.EventBlockDelta:
		return e.encodeDelta(event)

	case canonical.EventBlockStop:
		// OpenAI 无 block 结束事件。
		return nil, nil

	case canonical.EventMessageDelta:
		e.mergeUsage(event.Usage)
		if event.StopReason == "" {
			if event.UsageFinal && event.Usage != nil && !e.usageSent {
				e.usageSent = true
				return e.marshalUsageChunk(wireUsageFromCanonical(e.usage))
			}
			return nil, nil
		}
		finish := mapStopReasonToOpenAI(event.StopReason)
		finishChunk, err := e.marshalChunk(streamChoice{
			Index:        0,
			Delta:        &streamDelta{},
			FinishReason: &finish,
		}, nil)
		if err != nil {
			return nil, err
		}
		// OpenAI stream_options.include_usage 的标准形态是：先发带
		// finish_reason 的 choice，再发 choices:[] 的独立最终 usage 块。
		// Anthropic 的 stop_reason 与最终 usage 同处一个 canonical 事件，故这里
		// 一次 Encode 返回两条相邻 SSE record，不能把 usage 塞进 choice 块。
		if event.UsageFinal && event.Usage != nil && !e.usageSent {
			usageChunk, err := e.marshalUsageChunk(wireUsageFromCanonical(e.usage))
			if err != nil {
				return nil, err
			}
			e.usageSent = true
			return append(finishChunk, usageChunk...), nil
		}
		return finishChunk, nil

	case canonical.EventMessageStop:
		// OpenAI 以 "data: [DONE]" 结束整个流。
		return []byte("data: [DONE]\n\n"), nil

	case canonical.EventError:
		return marshalErrorEvent(event.Err)

	case canonical.EventPing:
		return nil, nil
	}
	return nil, nil
}

func marshalErrorEvent(message string) ([]byte, error) {
	payload := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "api_error",
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai: 编码流式错误失败: %w", err)
	}
	out := make([]byte, 0, len(b)+8)
	out = append(out, "data: "...)
	out = append(out, b...)
	out = append(out, "\n\n"...)
	return out, nil
}

func (e *streamEncoder) mergeUsage(in *canonical.Usage) {
	if in == nil {
		return
	}
	if in.InputTokensKnown {
		e.usage.InputTokens = in.InputTokens
		e.usage.InputTokensKnown = true
	}
	if in.OutputTokensKnown {
		e.usage.OutputTokens = in.OutputTokens
		e.usage.OutputTokensKnown = true
	}
	if in.TotalTokensKnown {
		e.usage.ReportedTotalTokens = in.ReportedTotalTokens
		e.usage.TotalTokensKnown = true
	}
	if in.CacheCreationInputTokens > e.usage.CacheCreationInputTokens {
		e.usage.CacheCreationInputTokens = in.CacheCreationInputTokens
	}
	if in.CacheReadInputTokens > e.usage.CacheReadInputTokens {
		e.usage.CacheReadInputTokens = in.CacheReadInputTokens
	}
}

// encodeDelta 编码一个 block 增量。
func (e *streamEncoder) encodeDelta(event canonical.Event) ([]byte, error) {
	if event.Delta == nil {
		return nil, nil
	}
	d := event.Delta

	// 文本增量
	if d.Text != "" {
		return e.marshalChunk(streamChoice{
			Index: 0,
			Delta: &streamDelta{Content: d.Text},
		}, nil)
	}

	// 工具参数增量
	if d.PartialInputJSON != "" {
		oaIdx := e.assignToolIndex(event.BlockIndex)
		return e.marshalChunk(streamChoice{
			Index: 0,
			Delta: &streamDelta{
				ToolCalls: []streamToolCall{{
					Index:    oaIdx,
					Function: functionCall{Arguments: d.PartialInputJSON},
				}},
			},
		}, nil)
	}

	return nil, nil
}

// assignToolIndex 为规范 block 索引分配（或复用）OpenAI 的 tool_calls index。
func (e *streamEncoder) assignToolIndex(blockIdx int) int {
	if e.toolIndex == nil {
		e.toolIndex = make(map[int]int)
	}
	if idx, ok := e.toolIndex[blockIdx]; ok {
		return idx
	}
	idx := e.nextTool
	e.nextTool++
	e.toolIndex[blockIdx] = idx
	return idx
}

func (e *streamEncoder) markToolStarted(blockIdx int) {
	if e.toolStarted == nil {
		e.toolStarted = make(map[int]bool)
	}
	e.toolStarted[blockIdx] = true
}

// marshalChunk 把一个 choice（可选 usage）包装为 OpenAI SSE chunk 字节。
func (e *streamEncoder) marshalChunk(ch streamChoice, u *usage) ([]byte, error) {
	chunk := streamChunk{
		ID:      e.id,
		Object:  "chat.completion.chunk",
		Model:   e.model,
		Choices: []streamChoice{ch},
		Usage:   u,
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return nil, fmt.Errorf("openai: 编码流式块失败: %w", err)
	}
	out := make([]byte, 0, len(b)+8)
	out = append(out, "data: "...)
	out = append(out, b...)
	out = append(out, "\n\n"...)
	return out, nil
}

func (e *streamEncoder) marshalUsageChunk(u *usage) ([]byte, error) {
	chunk := streamChunk{
		ID:      e.id,
		Object:  "chat.completion.chunk",
		Model:   e.model,
		Choices: []streamChoice{},
		Usage:   u,
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return nil, fmt.Errorf("openai: 编码流式 usage 块失败: %w", err)
	}
	out := make([]byte, 0, len(b)+8)
	out = append(out, "data: "...)
	out = append(out, b...)
	out = append(out, "\n\n"...)
	return out, nil
}
