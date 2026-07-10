package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"linapi/internal/adapter"
	"linapi/internal/adapter/anthropic"
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
	if events[0].Usage != nil {
		t.Fatalf("合成 message_start 不应伪造空 usage: %+v", events[0].Usage)
	}

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

func TestStreamDecodeUsagePresenceAndFinal(t *testing.T) {
	tests := []struct {
		name                    string
		usage                   string
		inputKnown, outputKnown bool
		totalKnown              bool
		total                   int
	}{
		{name: "total-only", usage: `{"total_tokens":7}`, totalKnown: true, total: 7},
		{name: "explicit-zero", usage: `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, inputKnown: true, outputKnown: true, totalKnown: true},
		{name: "partial", usage: `{"prompt_tokens":3}`, inputKnown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := (&Adapter{}).NewStreamDecoder()
			if _, err := d.Decode([]byte(`data: {"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)); err != nil {
				t.Fatal(err)
			}
			events, err := d.Decode([]byte(`data: {"id":"c1","model":"gpt-4o","choices":[],"usage":` + tt.usage + `}`))
			if err != nil {
				t.Fatalf("Decode 失败: %v", err)
			}
			if len(events) != 1 || events[0].Usage == nil || !events[0].UsageFinal {
				t.Fatalf("真实 usage 尾块必须标记 final: %+v", events)
			}
			u := events[0].Usage
			if u.InputTokensKnown != tt.inputKnown || u.OutputTokensKnown != tt.outputKnown ||
				u.TotalTokensKnown != tt.totalKnown || u.ReportedTotalTokens != tt.total {
				t.Fatalf("usage 字段存在性错误: %+v", u)
			}
		})
	}
}

func TestStreamDecodeRejectsUnexpectedMultipleChoices(t *testing.T) {
	d := (&Adapter{}).NewStreamDecoder()
	_, err := d.Decode([]byte(`data: {"id":"c1","model":"gpt-4o","choices":[` +
		`{"index":0,"delta":{"content":"a"}},` +
		`{"index":1,"delta":{"content":"b"}}]}`))
	if err == nil {
		t.Fatal("n=1 流中的多 choice 块必须显式拒绝")
	}
}

func TestStreamErrorRoundTrip(t *testing.T) {
	decoder := (&Adapter{}).NewStreamDecoder()
	events, err := decoder.Decode([]byte(`data: {"error":{"message":"upstream failed","type":"server_error"}}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(events) != 1 || events[0].Type != canonical.EventError || events[0].Err != "upstream failed" {
		t.Fatalf("错误块应解码为 EventError: %+v", events)
	}

	encoded, err := (&Adapter{}).NewStreamEncoder().Encode(events[0])
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(string(encoded), `"error"`) || !strings.Contains(string(encoded), "upstream failed") {
		t.Fatalf("EventError 应编码为 OpenAI SSE 错误块: %s", encoded)
	}
}

func TestStreamDecodeStandardSSEFields(t *testing.T) {
	decoder := (&Adapter{}).NewStreamDecoder()
	events, err := decoder.Decode([]byte("event: chunk\nid: 1\ndata: {\"id\":\"c1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(events) < 2 || events[len(events)-1].Delta == nil || events[len(events)-1].Delta.Text != "ok" {
		t.Fatalf("event/id 字段不应妨碍 data 解码: %+v", events)
	}
	ping, err := decoder.Decode([]byte(": keepalive"))
	if err != nil || len(ping) != 1 || ping[0].Type != canonical.EventPing {
		t.Fatalf("SSE 注释应作为心跳忽略: events=%+v err=%v", ping, err)
	}
}

func TestStreamEncodeFinalUsageTail(t *testing.T) {
	e := (&Adapter{}).NewStreamEncoder()
	_, _ = e.Encode(canonical.Event{Type: canonical.EventMessageStart, ID: "c1", Model: "gpt-4o"})
	out, err := e.Encode(canonical.Event{
		Type: canonical.EventMessageDelta,
		Usage: &canonical.Usage{
			InputTokens: 0, OutputTokens: 0,
			InputTokensKnown: true, OutputTokensKnown: true,
		},
		UsageFinal: true,
	})
	if err != nil {
		t.Fatalf("Encode 失败: %v", err)
	}
	s := string(out)
	for _, want := range []string{`"choices":[]`, `"prompt_tokens":0`, `"completion_tokens":0`, `"total_tokens":0`} {
		if !strings.Contains(s, want) {
			t.Fatalf("最终 usage 块缺少 %s: %s", want, s)
		}
	}
}

func TestStreamEncodeStopAndUsageUsesIndependentTailChunk(t *testing.T) {
	e := (&Adapter{}).NewStreamEncoder()
	_, _ = e.Encode(canonical.Event{Type: canonical.EventMessageStart, ID: "c1", Model: "gpt-4o"})
	out, err := e.Encode(canonical.Event{
		Type: canonical.EventMessageDelta, StopReason: canonical.StopEndTurn,
		Usage: &canonical.Usage{
			InputTokens: 7, OutputTokens: 3,
			InputTokensKnown: true, OutputTokensKnown: true,
		},
		UsageFinal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	finishPos := strings.Index(result, `"finish_reason":"stop"`)
	usagePos := strings.Index(result, `"choices":[]`)
	if finishPos < 0 || usagePos < 0 || finishPos >= usagePos {
		t.Fatalf("必须先输出 finish choice，再输出独立 usage 尾块: %s", result)
	}
	if strings.Count(result, `"usage":`) != 1 {
		t.Fatalf("最终 usage 只能输出一次: %s", result)
	}
}

func TestStreamEncodeFinalUsageIncludesCachedInput(t *testing.T) {
	e := (&Adapter{}).NewStreamEncoder()
	_, _ = e.Encode(canonical.Event{
		Type: canonical.EventMessageStart, ID: "c1", Model: "gpt-4o",
		Usage: &canonical.Usage{
			InputTokens: 20, InputTokensKnown: true,
			CacheCreationInputTokens: 5, CacheReadInputTokens: 80,
		},
	})
	out, err := e.Encode(canonical.Event{
		Type:  canonical.EventMessageDelta,
		Usage: &canonical.Usage{OutputTokens: 10, OutputTokensKnown: true}, UsageFinal: true,
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	s := string(out)
	for _, want := range []string{`"prompt_tokens":105`, `"cached_tokens":80`, `"completion_tokens":10`, `"total_tokens":115`} {
		if !strings.Contains(s, want) {
			t.Fatalf("缓存 usage 尾块缺少 %s: %s", want, s)
		}
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

func TestToolArgumentStreamFragmentsPreservedToAnthropic(t *testing.T) {
	decoder := (&Adapter{}).NewStreamDecoder()
	encoder := (&anthropic.Adapter{}).NewStreamEncoder()
	fragments := []string{
		`{"order_id":9007199254740993`,
		`,"nested":{"id":9223372036854775807}}`,
	}

	chunks := []streamChunk{
		{
			ID: "c1", Model: "gpt-4o",
			Choices: []streamChoice{{Index: 0, Delta: &streamDelta{ToolCalls: []streamToolCall{{
				Index: 0, ID: "call_1", Type: "function",
				Function: functionCall{Name: "submit", Arguments: fragments[0]},
			}}}}},
		},
		{
			ID: "c1", Model: "gpt-4o",
			Choices: []streamChoice{{Index: 0, Delta: &streamDelta{ToolCalls: []streamToolCall{{
				Index: 0, Function: functionCall{Arguments: fragments[1]},
			}}}}},
		},
	}

	var rebuilt string
	for _, chunk := range chunks {
		payload, _ := json.Marshal(chunk)
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
				Delta *struct {
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal(data, &envelope); err == nil && envelope.Delta != nil {
				rebuilt += envelope.Delta.PartialJSON
			}
		}
	}

	want := strings.Join(fragments, "")
	if rebuilt != want {
		t.Fatalf("跨格式流式工具参数被改写: got=%q want=%q", rebuilt, want)
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
