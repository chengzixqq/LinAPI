package openai

import (
	"bytes"
	"encoding/json"
	"fmt"

	"linapi/internal/adapter"
	"linapi/internal/canonical"
)

// NewStreamDecoder 创建 OpenAI SSE 解码器。
func (a *Adapter) NewStreamDecoder() adapter.StreamDecoder {
	return &streamDecoder{}
}

// streamDecoder 把 OpenAI 流式 SSE 解析为规范事件。
//
// OpenAI 流式是“扁平增量”：首个 chunk 的 delta 带 role，随后 chunk 带
// content 文本增量或 tool_calls 参数分片，最后一个 chunk 带 finish_reason，
// 若开启 stream_options 还会有一个仅含 usage 的尾块。
//
// 规范事件是“结构化 block 流”，因此解码器需要跨块维护状态：是否已发出
// message_start、当前处于哪个 block、各 block 的类型与索引。
type streamDecoder struct {
	started      bool // 是否已发出 message_start
	nextBlockIdx int  // 下一个要分配的 block 索引

	textOpen     bool // 文本 block 是否已开启
	textBlockIdx int

	// 工具调用：OpenAI 用 tool_calls[].index 标识第几个工具调用，
	// 这里把它映射到规范 block 索引，并记录是否已开启。
	toolBlocks map[int]int // openai tool_calls index -> canonical block index
}

func (d *streamDecoder) Decode(raw []byte) ([]canonical.Event, error) {
	// 去掉 SSE 的 "data: " 前缀与空白。
	line := bytes.TrimSpace(raw)
	line = bytes.TrimPrefix(line, []byte("data:"))
	line = bytes.TrimSpace(line)

	// 空行或结束标记。
	if len(line) == 0 {
		return nil, nil
	}
	if bytes.Equal(line, []byte("[DONE]")) {
		return d.finish(), nil
	}

	var chunk streamChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return nil, fmt.Errorf("openai: 解析流式块失败: %w", err)
	}

	var events []canonical.Event

	// 首块补发 message_start。
	if !d.started {
		d.started = true
		events = append(events, canonical.Event{
			Type:  canonical.EventMessageStart,
			ID:    chunk.ID,
			Model: chunk.Model,
			Usage: &canonical.Usage{},
		})
	}

	// 仅含 usage 的尾块（stream_options.include_usage）。
	if len(chunk.Choices) == 0 {
		if chunk.Usage != nil {
			events = append(events, canonical.Event{
				Type: canonical.EventMessageDelta,
				Usage: &canonical.Usage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
				},
			})
		}
		return events, nil
	}

	ch := chunk.Choices[0]
	if ch.Delta != nil {
		delta := ch.Delta

		// 文本增量
		if text, _ := contentToString(delta.Content); text != "" {
			if !d.textOpen {
				d.textOpen = true
				d.textBlockIdx = d.nextBlockIdx
				d.nextBlockIdx++
				events = append(events, canonical.Event{
					Type:       canonical.EventBlockStart,
					BlockIndex: d.textBlockIdx,
					BlockType:  canonical.BlockText,
				})
			}
			events = append(events, canonical.Event{
				Type:       canonical.EventBlockDelta,
				BlockIndex: d.textBlockIdx,
				Delta:      &canonical.Delta{Text: text},
			})
		}

		// 工具调用增量
		for _, tc := range delta.ToolCalls {
			events = append(events, d.handleToolCallDelta(tc)...)
		}
	}

	// finish_reason 出现即代表消息将结束。
	if ch.FinishReason != nil {
		events = append(events, d.closeOpenBlocks()...)
		ev := canonical.Event{
			Type:       canonical.EventMessageDelta,
			StopReason: mapFinishReasonToCanonical(*ch.FinishReason),
		}
		if chunk.Usage != nil {
			ev.Usage = &canonical.Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
		}
		events = append(events, ev)
	}

	return events, nil
}

// handleToolCallDelta 处理一个 tool_calls 增量分片。
// OpenAI 首个分片带 id+name，后续分片仅带 arguments 字符串片段。
func (d *streamDecoder) handleToolCallDelta(tc streamToolCall) []canonical.Event {
	if d.toolBlocks == nil {
		d.toolBlocks = make(map[int]int)
	}

	var events []canonical.Event
	idx := tc.Index

	blockIdx, open := d.toolBlocks[idx]
	if !open {
		// 新工具调用：先关掉可能开着的文本 block（OpenAI 文本在工具调用前结束）。
		events = append(events, d.closeTextBlock()...)

		blockIdx = d.nextBlockIdx
		d.nextBlockIdx++
		d.toolBlocks[idx] = blockIdx
		events = append(events, canonical.Event{
			Type:       canonical.EventBlockStart,
			BlockIndex: blockIdx,
			BlockType:  canonical.BlockToolUse,
			Delta: &canonical.Delta{
				ToolUseID: tc.ID,
				ToolName:  tc.Function.Name,
			},
		})
	}

	if tc.Function.Arguments != "" {
		events = append(events, canonical.Event{
			Type:       canonical.EventBlockDelta,
			BlockIndex: blockIdx,
			Delta:      &canonical.Delta{PartialInputJSON: tc.Function.Arguments},
		})
	}
	return events
}

// closeTextBlock 若文本 block 开着则发出其 block_stop。
func (d *streamDecoder) closeTextBlock() []canonical.Event {
	if !d.textOpen {
		return nil
	}
	d.textOpen = false
	return []canonical.Event{{Type: canonical.EventBlockStop, BlockIndex: d.textBlockIdx}}
}

// closeOpenBlocks 关闭所有仍开启的 block（文本 + 工具）。
func (d *streamDecoder) closeOpenBlocks() []canonical.Event {
	var events []canonical.Event
	events = append(events, d.closeTextBlock()...)
	for _, blockIdx := range d.toolBlocks {
		events = append(events, canonical.Event{Type: canonical.EventBlockStop, BlockIndex: blockIdx})
	}
	d.toolBlocks = nil
	return events
}

// finish 在收到 [DONE] 时收尾：关闭残留 block 并发出 message_stop。
func (d *streamDecoder) finish() []canonical.Event {
	events := d.closeOpenBlocks()
	events = append(events, canonical.Event{Type: canonical.EventMessageStop})
	return events
}
