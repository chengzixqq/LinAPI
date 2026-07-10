package anthropic

import (
	"encoding/json"
	"fmt"

	"linapi/internal/adapter"
	"linapi/internal/canonical"
)

// NewStreamEncoder 创建 Anthropic SSE 编码器。
func (a *Adapter) NewStreamEncoder() adapter.StreamEncoder {
	return &streamEncoder{}
}

// streamEncoder 把规范事件编码为 Anthropic 流式 SSE。
// 记录各 block 类型，使 block_delta 能输出正确的 delta type
// （text_delta / input_json_delta / thinking_delta）。
type streamEncoder struct {
	blockTypes map[int]canonical.BlockType
}

func (e *streamEncoder) Encode(event canonical.Event) ([]byte, error) {
	switch event.Type {
	case canonical.EventMessageStart:
		msg := messagesResponse{
			ID:      event.ID,
			Type:    "message",
			Role:    "assistant",
			Model:   event.Model,
			Content: []block{},
		}
		if event.Usage != nil {
			msg.Usage = &usage{
				InputTokens:  event.Usage.InputTokens,
				OutputTokens: event.Usage.OutputTokens,
			}
		}
		return sse("message_start", map[string]any{"type": "message_start", "message": msg})

	case canonical.EventBlockStart:
		e.setBlockType(event.BlockIndex, event.BlockType)
		cb := e.startBlockPayload(event)
		return sse("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         event.BlockIndex,
			"content_block": cb,
		})

	case canonical.EventBlockDelta:
		delta := e.deltaPayload(event)
		if delta == nil {
			return nil, nil
		}
		return sse("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": event.BlockIndex,
			"delta": delta,
		})

	case canonical.EventBlockStop:
		return sse("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": event.BlockIndex,
		})

	case canonical.EventMessageDelta:
		delta := map[string]any{}
		if event.StopReason != "" {
			delta["stop_reason"] = mapStopReasonToWire(event.StopReason)
		}
		payload := map[string]any{"type": "message_delta", "delta": delta}
		if event.Usage != nil {
			payload["usage"] = map[string]any{"output_tokens": event.Usage.OutputTokens}
		}
		return sse("message_delta", payload)

	case canonical.EventMessageStop:
		return sse("message_stop", map[string]any{"type": "message_stop"})

	case canonical.EventPing:
		return sse("ping", map[string]any{"type": "ping"})

	case canonical.EventError:
		return sse("error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": event.Err},
		})
	}
	return nil, nil
}

// startBlockPayload 构造 content_block_start 的 content_block 字段。
func (e *streamEncoder) startBlockPayload(event canonical.Event) map[string]any {
	switch event.BlockType {
	case canonical.BlockToolUse:
		cb := map[string]any{"type": "tool_use", "input": map[string]any{}}
		if event.Delta != nil {
			cb["id"] = event.Delta.ToolUseID
			cb["name"] = event.Delta.ToolName
		}
		return cb
	case canonical.BlockThinking:
		return map[string]any{"type": "thinking", "thinking": ""}
	default:
		return map[string]any{"type": "text", "text": ""}
	}
}

// deltaPayload 构造 content_block_delta 的 delta 字段。
func (e *streamEncoder) deltaPayload(event canonical.Event) map[string]any {
	if event.Delta == nil {
		return nil
	}
	d := event.Delta
	switch e.blockType(event.BlockIndex) {
	case canonical.BlockToolUse:
		return map[string]any{"type": "input_json_delta", "partial_json": d.PartialInputJSON}
	case canonical.BlockThinking:
		if d.ThinkingSignature != "" {
			return map[string]any{"type": "signature_delta", "signature": d.ThinkingSignature}
		}
		return map[string]any{"type": "thinking_delta", "thinking": d.Thinking}
	default:
		return map[string]any{"type": "text_delta", "text": d.Text}
	}
}

func (e *streamEncoder) setBlockType(idx int, bt canonical.BlockType) {
	if e.blockTypes == nil {
		e.blockTypes = make(map[int]canonical.BlockType)
	}
	e.blockTypes[idx] = bt
}

func (e *streamEncoder) blockType(idx int) canonical.BlockType {
	if bt, ok := e.blockTypes[idx]; ok {
		return bt
	}
	return canonical.BlockText
}

// sse 把事件名与 payload 序列化为 Anthropic SSE 记录："event: X\ndata: {...}\n\n"。
func sse(event string, payload map[string]any) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("anthropic: 编码流式事件失败: %w", err)
	}
	out := make([]byte, 0, len(b)+len(event)+16)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, b...)
	out = append(out, "\n\n"...)
	return out, nil
}
