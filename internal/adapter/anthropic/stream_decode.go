package anthropic

import (
	"encoding/json"
	"fmt"

	"linapi/internal/adapter"
	"linapi/internal/canonical"
)

// NewStreamDecoder 创建 Anthropic SSE 解码器。
func (a *Adapter) NewStreamDecoder() adapter.StreamDecoder {
	return &streamDecoder{}
}

// streamDecoder 把 Anthropic 流式 SSE 解析为规范事件。
// Claude 事件与 canonical 事件几乎一一对应，基本是字段搬运；
// 需要记录每个 block 的类型，供 block_delta 判断增量种类。
type streamDecoder struct {
	blockTypes map[int]canonical.BlockType
}

func (d *streamDecoder) Decode(raw []byte) ([]canonical.Event, error) {
	line, hasData := adapter.SSEData(raw)
	if !hasData {
		if len(raw) == 0 {
			return nil, nil
		}
		return []canonical.Event{{Type: canonical.EventPing}}, nil
	}
	if len(line) == 0 {
		return nil, nil
	}

	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil, fmt.Errorf("anthropic: 解析流式事件失败: %w", err)
	}

	switch ev.Type {
	case "message_start":
		out := canonical.Event{Type: canonical.EventMessageStart}
		if ev.Message != nil {
			out.ID = ev.Message.ID
			out.Model = ev.Message.Model
			if ev.Message.Usage != nil {
				out.Usage = canonicalUsageFromWire(ev.Message.Usage)
			}
		}
		return []canonical.Event{out}, nil

	case "content_block_start":
		bt := canonical.BlockText
		out := canonical.Event{Type: canonical.EventBlockStart, BlockIndex: ev.Index}
		if ev.ContentBlock != nil {
			bt = wireBlockType(ev.ContentBlock.Type)
			out.BlockType = bt
			// tool_use 起始块带 id + name。
			if bt == canonical.BlockToolUse {
				out.Delta = &canonical.Delta{
					ToolUseID: ev.ContentBlock.ID,
					ToolName:  ev.ContentBlock.Name,
				}
			}
		}
		d.setBlockType(ev.Index, bt)
		return []canonical.Event{out}, nil

	case "content_block_delta":
		return []canonical.Event{d.decodeBlockDelta(ev)}, nil

	case "content_block_stop":
		return []canonical.Event{{Type: canonical.EventBlockStop, BlockIndex: ev.Index}}, nil

	case "message_delta":
		out := canonical.Event{Type: canonical.EventMessageDelta}
		terminal := ev.Delta != nil && ev.Delta.StopReason != ""
		if terminal {
			out.StopReason = mapStopReasonToCanonical(ev.Delta.StopReason)
		}
		if ev.Usage != nil {
			out.Usage = canonicalUsageFromWire(ev.Usage)
			// Anthropic 的最终 usage 与终止 message_delta 同步出现。兼容上游
			// 若在生成中途发送 usage，只能作为临时观测值，不能据此精确结算。
			out.UsageFinal = terminal
		}
		return []canonical.Event{out}, nil

	case "message_stop":
		return []canonical.Event{{Type: canonical.EventMessageStop}}, nil

	case "ping":
		return []canonical.Event{{Type: canonical.EventPing}}, nil

	case "error":
		msg := ""
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		return []canonical.Event{{Type: canonical.EventError, Err: msg}}, nil
	}

	return nil, nil
}

// decodeBlockDelta 根据当前 block 类型解释增量。
func (d *streamDecoder) decodeBlockDelta(ev streamEvent) canonical.Event {
	out := canonical.Event{Type: canonical.EventBlockDelta, BlockIndex: ev.Index}
	if ev.Delta == nil {
		return out
	}
	delta := &canonical.Delta{}
	switch d.blockType(ev.Index) {
	case canonical.BlockToolUse:
		delta.PartialInputJSON = ev.Delta.PartialJSON
	case canonical.BlockThinking:
		delta.Thinking = ev.Delta.Thinking
		delta.ThinkingSignature = ev.Delta.Signature
	default:
		delta.Text = ev.Delta.Text
	}
	out.Delta = delta
	return out
}

func (d *streamDecoder) setBlockType(idx int, bt canonical.BlockType) {
	if d.blockTypes == nil {
		d.blockTypes = make(map[int]canonical.BlockType)
	}
	d.blockTypes[idx] = bt
}

func (d *streamDecoder) blockType(idx int) canonical.BlockType {
	if bt, ok := d.blockTypes[idx]; ok {
		return bt
	}
	return canonical.BlockText
}

// wireBlockType 把 Anthropic block type 字符串映射为规范块类型。
func wireBlockType(t string) canonical.BlockType {
	switch t {
	case "text":
		return canonical.BlockText
	case "thinking":
		return canonical.BlockThinking
	case "tool_use":
		return canonical.BlockToolUse
	case "image":
		return canonical.BlockImage
	default:
		return canonical.BlockText
	}
}

// extractDataLine 从可能含 "event:" / "data:" 多行的 SSE 记录中取出 data 的 JSON 部分。
