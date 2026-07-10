package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"linapi/internal/adapter"
	"linapi/internal/canonical"
	// 导入 openai 以触发其 init 注册，供跨格式编码使用。
	_ "linapi/internal/adapter/openai"
)

// TestStreamDecode 验证 Anthropic 原生 SSE 事件流解码为规范事件。
func TestStreamDecode(t *testing.T) {
	d := (&Adapter{}).NewStreamDecoder()

	records := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-3-5-sonnet\",\"usage\":{\"input_tokens\":10}}}",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"你好\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
	}

	var events []canonical.Event
	for _, r := range records {
		evs, err := d.Decode([]byte(r))
		if err != nil {
			t.Fatalf("Decode 失败: %v", err)
		}
		events = append(events, evs...)
	}

	if len(events) == 0 {
		t.Fatal("未产出任何事件")
	}
	if events[0].Type != canonical.EventMessageStart {
		t.Errorf("首事件应为 message_start, 得到 %q", events[0].Type)
	}
	if events[0].ID != "msg_1" || events[0].Model != "claude-3-5-sonnet" {
		t.Errorf("message_start 元信息丢失: id=%q model=%q", events[0].ID, events[0].Model)
	}
	if events[0].Usage == nil || !events[0].Usage.InputTokensKnown || events[0].Usage.OutputTokensKnown || events[0].UsageFinal {
		t.Fatalf("message_start usage 应只累计且非 final: %+v", events[0])
	}

	var text string
	var stop canonical.StopReason
	var finalUsage *canonical.Event
	for _, e := range events {
		if e.Type == canonical.EventBlockDelta && e.Delta != nil {
			text += e.Delta.Text
		}
		if e.Type == canonical.EventMessageDelta && e.StopReason != "" {
			stop = e.StopReason
		}
		if e.UsageFinal {
			ev := e
			finalUsage = &ev
		}
	}
	if text != "你好" {
		t.Errorf("文本增量拼接错误: 得到 %q", text)
	}
	if stop != canonical.StopEndTurn {
		t.Errorf("停止原因错误: 得到 %q", stop)
	}
	if finalUsage == nil || finalUsage.Usage == nil || !finalUsage.Usage.OutputTokensKnown ||
		finalUsage.Usage.InputTokensKnown || finalUsage.Usage.OutputTokens != 5 {
		t.Fatalf("message_delta usage 应标记 final 且保留字段存在性: %+v", finalUsage)
	}
}

func TestStreamUsageWithoutStopReasonIsNotFinal(t *testing.T) {
	decoder := (&Adapter{}).NewStreamDecoder()
	events, err := decoder.Decode([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":1}}"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(events) != 1 || events[0].Usage == nil || !events[0].Usage.OutputTokensKnown {
		t.Fatalf("应保留临时 usage: %+v", events)
	}
	if events[0].UsageFinal {
		t.Fatalf("没有 stop_reason 的 message_delta 不得标记为最终 usage: %+v", events[0])
	}
}

// TestCrossFormatStream 跨格式流式：Claude SSE -> canonical -> OpenAI SSE。
// 这是网关核心价值的直接验证——用 Claude 渠道服务 OpenAI 格式客户端。
func TestCrossFormatStream(t *testing.T) {
	dec := (&Adapter{}).NewStreamDecoder()

	oa, err := adapter.MustGet("openai")
	if err != nil {
		t.Fatalf("取 openai 适配器失败: %v", err)
	}
	enc := oa.NewStreamEncoder()

	records := []string{
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-3-5-sonnet\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}",
		"data: {\"type\":\"content_block_stop\",\"index\":0}",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}",
		"data: {\"type\":\"message_stop\"}",
	}

	var out strings.Builder
	for _, r := range records {
		evs, err := dec.Decode([]byte(r))
		if err != nil {
			t.Fatalf("解码失败: %v", err)
		}
		for _, e := range evs {
			b, err := enc.Encode(e)
			if err != nil {
				t.Fatalf("编码失败: %v", err)
			}
			out.Write(b)
		}
	}

	result := out.String()
	// 输出应是 OpenAI 格式的 SSE：含 chat.completion.chunk、文本 Hi、[DONE]
	if !strings.Contains(result, "chat.completion.chunk") {
		t.Errorf("输出不是 OpenAI chunk 格式:\n%s", result)
	}
	if !strings.Contains(result, "Hi") {
		t.Errorf("文本内容丢失:\n%s", result)
	}
	if !strings.Contains(result, "[DONE]") {
		t.Errorf("缺少 OpenAI 流结束标记 [DONE]:\n%s", result)
	}
	if !strings.Contains(result, `"finish_reason":"stop"`) {
		t.Errorf("停止原因未正确转为 OpenAI stop:\n%s", result)
	}
	for _, want := range []string{`"prompt_tokens":10`, `"completion_tokens":5`, `"total_tokens":15`} {
		if !strings.Contains(result, want) {
			t.Errorf("跨格式最终 usage 缺少 %s:\n%s", want, result)
		}
	}
}

func TestToolArgumentStreamFragmentsPreservedToOpenAI(t *testing.T) {
	decoder := (&Adapter{}).NewStreamDecoder()
	openAI, err := adapter.MustGet("openai")
	if err != nil {
		t.Fatalf("取 openai 适配器失败: %v", err)
	}
	encoder := openAI.NewStreamEncoder()
	fragments := []string{
		`{"order_id":9007199254740993`,
		`,"nested":{"id":9223372036854775807}}`,
	}
	records := []map[string]any{
		{"type": "message_start", "message": map[string]any{"id": "m1", "model": "claude"}},
		{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "submit", "input": map[string]any{}}},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": fragments[0]}},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": fragments[1]}},
	}

	var rebuilt string
	for _, record := range records {
		payload, _ := json.Marshal(record)
		events, err := decoder.Decode(append([]byte("data: "), payload...))
		if err != nil {
			t.Fatalf("Decode 失败: %v", err)
		}
		for _, event := range events {
			out, err := encoder.Encode(event)
			if err != nil {
				t.Fatalf("Encode 失败: %v", err)
			}
			data, ok := adapter.SSEData(out)
			if !ok || len(data) == 0 {
				continue
			}
			var envelope struct {
				Choices []struct {
					Delta struct {
						ToolCalls []struct {
							Function struct {
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(data, &envelope); err == nil && len(envelope.Choices) > 0 && len(envelope.Choices[0].Delta.ToolCalls) > 0 {
				rebuilt += envelope.Choices[0].Delta.ToolCalls[0].Function.Arguments
			}
		}
	}

	want := strings.Join(fragments, "")
	if rebuilt != want {
		t.Fatalf("跨格式流式工具参数被改写: got=%q want=%q", rebuilt, want)
	}
}
