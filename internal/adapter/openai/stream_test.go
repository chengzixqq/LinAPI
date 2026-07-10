package openai

import (
	"testing"

	"linapi/internal/canonical"
)

// TestStreamDecodeText 验证纯文本流式解码：message_start -> block_start -> deltas -> stop。
func TestStreamDecodeText(t *testing.T) {
	d := (&Adapter{}).NewStreamDecoder()

	chunks := []string{
		`data: {"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`data: {"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"你"}}]}`,
		`data: {"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"好"}}]}`,
		`data: {"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}

	var events []canonical.Event
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("Decode 失败: %v", err)
		}
		events = append(events, evs...)
	}

	// 校验关键事件出现且顺序合理
	assertEventSeq(t, events, []canonical.EventType{
		canonical.EventMessageStart,
		canonical.EventBlockStart,
		canonical.EventBlockDelta,
		canonical.EventBlockDelta,
		canonical.EventBlockStop,
		canonical.EventMessageDelta,
		canonical.EventMessageStop,
	})

	// 拼接文本增量应为 "你好"
	var text string
	for _, e := range events {
		if e.Type == canonical.EventBlockDelta && e.Delta != nil {
			text += e.Delta.Text
		}
	}
	if text != "你好" {
		t.Errorf("文本增量拼接错误: 期望 你好, 得到 %q", text)
	}
}

// TestStreamDecodeToolCall 验证工具调用流式解码：分片的 arguments 能被逐片透传。
func TestStreamDecodeToolCall(t *testing.T) {
	d := (&Adapter{}).NewStreamDecoder()

	chunks := []string{
		`data: {"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`data: {"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`data: {"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`,
		`data: {"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"北京\"}"}}]}}]}`,
		`data: {"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}

	var events []canonical.Event
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("Decode 失败: %v", err)
		}
		events = append(events, evs...)
	}

	// 应有一个 tool_use 的 block_start，携带 id 与 name
	var foundStart bool
	var argJSON string
	for _, e := range events {
		if e.Type == canonical.EventBlockStart && e.BlockType == canonical.BlockToolUse {
			foundStart = true
			if e.Delta == nil || e.Delta.ToolUseID != "call_1" || e.Delta.ToolName != "get_weather" {
				t.Errorf("tool_use block_start 元信息错误: %+v", e.Delta)
			}
		}
		if e.Type == canonical.EventBlockDelta && e.Delta != nil {
			argJSON += e.Delta.PartialInputJSON
		}
	}
	if !foundStart {
		t.Error("未产出 tool_use 的 block_start")
	}
	if argJSON != `{"city":"北京"}` {
		t.Errorf("工具参数分片拼接错误: 得到 %q", argJSON)
	}

	// 停止原因应为 tool_use
	var stop canonical.StopReason
	for _, e := range events {
		if e.Type == canonical.EventMessageDelta && e.StopReason != "" {
			stop = e.StopReason
		}
	}
	if stop != canonical.StopToolUse {
		t.Errorf("停止原因错误: 期望 tool_use, 得到 %q", stop)
	}
}

// assertEventSeq 断言 events 中依次包含 want 里的事件类型（子序列匹配）。
func assertEventSeq(t *testing.T, events []canonical.Event, want []canonical.EventType) {
	t.Helper()
	i := 0
	for _, e := range events {
		if i < len(want) && e.Type == want[i] {
			i++
		}
	}
	if i != len(want) {
		var got []canonical.EventType
		for _, e := range events {
			got = append(got, e.Type)
		}
		t.Errorf("事件序列不匹配:\n期望子序列 %v\n实际 %v", want, got)
	}
}
