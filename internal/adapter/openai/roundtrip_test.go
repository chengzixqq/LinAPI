package openai

import (
	"encoding/json"
	"fmt"
	"testing"

	"linapi/internal/canonical"
)

// TestRequestRoundTrip 验证 canonical -> OpenAI 线格式 -> canonical。
// 重点：assistant 的 text + tool_use 折叠进扁平结构后能否正确展开回来。
func TestRequestRoundTrip(t *testing.T) {
	a := &Adapter{}
	mt := 512
	orig := &canonical.Request{
		Model:     "gpt-4o",
		MaxTokens: &mt,
		System: []canonical.ContentBlock{
			{Type: canonical.BlockText, Text: "你是助手"},
		},
		Messages: []canonical.Message{
			{
				Role:    canonical.RoleUser,
				Content: []canonical.ContentBlock{{Type: canonical.BlockText, Text: "天气"}},
			},
			{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentBlock{
					{Type: canonical.BlockText, Text: "查询中"},
					{
						Type:      canonical.BlockToolUse,
						ToolUseID: "call_1",
						ToolName:  "get_weather",
						ToolInput: map[string]any{"city": "上海"},
					},
				},
			},
			{
				Role: canonical.RoleUser,
				Content: []canonical.ContentBlock{
					{
						Type:         canonical.BlockToolResult,
						ToolResultID: "call_1",
						ToolResult:   []canonical.ContentBlock{{Type: canonical.BlockText, Text: "多云"}},
					},
				},
			},
		},
		Tools: []canonical.Tool{
			{Name: "get_weather", Description: "查询天气", InputSchema: map[string]any{"type": "object"}},
		},
		ToolChoice: &canonical.ToolChoice{Type: canonical.ToolChoiceAuto},
	}

	raw, err := a.BuildRequest(orig)
	if err != nil {
		t.Fatalf("BuildRequest 失败: %v", err)
	}
	got, err := a.ParseRequest(raw)
	if err != nil {
		t.Fatalf("ParseRequest 失败: %v", err)
	}

	if got.Model != "gpt-4o" {
		t.Errorf("Model 不一致: %q", got.Model)
	}
	if len(got.Messages) == 0 || got.Messages[0].Role != canonical.RoleSystem ||
		len(got.Messages[0].Content) != 1 || got.Messages[0].Content[0].Text != "你是助手" {
		t.Errorf("System 消息丢失或乱序: %+v", got.Messages)
	}

	// assistant 消息应展开回 text + tool_use 两个 block
	var asst *canonical.Message
	for i := range got.Messages {
		if got.Messages[i].Role == canonical.RoleAssistant {
			asst = &got.Messages[i]
			break
		}
	}
	if asst == nil {
		t.Fatal("未找到 assistant 消息")
	}
	if len(asst.Content) != 2 {
		t.Fatalf("assistant block 数应为 2, 得到 %d: %+v", len(asst.Content), asst.Content)
	}
	if asst.Content[0].Type != canonical.BlockText || asst.Content[0].Text != "查询中" {
		t.Errorf("assistant 文本丢失: %+v", asst.Content[0])
	}
	tu := asst.Content[1]
	if tu.Type != canonical.BlockToolUse || tu.ToolName != "get_weather" || tu.ToolUseID != "call_1" {
		t.Errorf("tool_use 折叠/展开出错: %+v", tu)
	}
	if tu.ToolInput["city"] != "上海" {
		t.Errorf("tool_use 参数丢失: %+v", tu.ToolInput)
	}

	// tool_result 应能从独立 tool 消息还原
	var foundToolResult bool
	for _, m := range got.Messages {
		for _, b := range m.Content {
			if b.Type == canonical.BlockToolResult && b.ToolResultID == "call_1" {
				foundToolResult = true
			}
		}
	}
	if !foundToolResult {
		t.Error("tool_result 丢失")
	}
}

// TestResponseRoundTrip 验证 OpenAI 响应方向。
func TestResponseRoundTrip(t *testing.T) {
	a := &Adapter{}
	orig := &canonical.Response{
		ID:    "chatcmpl-1",
		Model: "gpt-4o",
		Role:  canonical.RoleAssistant,
		Content: []canonical.ContentBlock{
			{Type: canonical.BlockText, Text: "你好"},
		},
		StopReason: canonical.StopEndTurn,
		Usage: canonical.Usage{
			InputTokens: 3, OutputTokens: 2,
			InputTokensKnown: true, OutputTokensKnown: true,
		},
	}

	raw, err := a.BuildResponse(orig)
	if err != nil {
		t.Fatalf("BuildResponse 失败: %v", err)
	}
	got, err := a.ParseResponse(raw)
	if err != nil {
		t.Fatalf("ParseResponse 失败: %v", err)
	}

	if len(got.Content) != 1 || got.Content[0].Text != "你好" {
		t.Errorf("响应文本丢失: %+v", got.Content)
	}
	if got.StopReason != canonical.StopEndTurn {
		t.Errorf("stop_reason 不一致: %q（原始 %q）", got.StopReason, orig.StopReason)
	}
	if got.Usage.InputTokens != 3 || got.Usage.OutputTokens != 2 {
		t.Errorf("usage 不一致: %+v", got.Usage)
	}
	if !got.Usage.InputTokensKnown || !got.Usage.OutputTokensKnown ||
		!got.Usage.TotalTokensKnown || got.Usage.ReportedTotalTokens != 5 {
		t.Errorf("usage 字段存在性或 total 丢失: %+v", got.Usage)
	}
}

func TestParseResponseUsagePresence(t *testing.T) {
	tests := []struct {
		name                    string
		usageField              string
		input, output, total    int
		inputKnown, outputKnown bool
		totalKnown              bool
	}{
		{name: "missing"},
		{name: "null", usageField: `,"usage":null`},
		{name: "empty", usageField: `,"usage":{}`},
		{name: "total-only", usageField: `,"usage":{"total_tokens":7}`, total: 7, totalKnown: true},
		{name: "input-only", usageField: `,"usage":{"prompt_tokens":3}`, input: 3, inputKnown: true},
		{name: "output-only", usageField: `,"usage":{"completion_tokens":4}`, output: 4, outputKnown: true},
		{name: "explicit-zero", usageField: `,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, inputKnown: true, outputKnown: true, totalKnown: true},
		{name: "complete", usageField: `,"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}`, input: 3, output: 4, total: 7, inputKnown: true, outputKnown: true, totalKnown: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]%s}`, tt.usageField)
			got, err := (&Adapter{}).ParseResponse([]byte(raw))
			if err != nil {
				t.Fatalf("ParseResponse 失败: %v", err)
			}
			u := got.Usage
			if u.InputTokens != tt.input || u.OutputTokens != tt.output || u.ReportedTotalTokens != tt.total ||
				u.InputTokensKnown != tt.inputKnown || u.OutputTokensKnown != tt.outputKnown ||
				u.TotalTokensKnown != tt.totalKnown {
				t.Fatalf("usage 映射错误: got=%+v", u)
			}
		})
	}
}

func TestUsageCachedTokensRoundTrip(t *testing.T) {
	prompt, completion, total, cached := 100, 10, 110, 80
	canon := canonicalUsageFromWire(&usage{
		PromptTokens:       intPointer(prompt),
		CompletionTokens:   intPointer(completion),
		TotalTokens:        intPointer(total),
		PromptTokenDetails: &promptTokenDetails{CachedTokens: intPointer(cached)},
	})
	if canon == nil || canon.InputTokens != 20 || canon.CacheReadInputTokens != 80 || !canon.InputTokensKnown {
		t.Fatalf("OpenAI cached_tokens 应从普通输入中拆分: %+v", canon)
	}
	wire := wireUsageFromCanonical(*canon)
	if wire == nil || wire.PromptTokens == nil || *wire.PromptTokens != 100 ||
		wire.PromptTokenDetails == nil || wire.PromptTokenDetails.CachedTokens == nil ||
		*wire.PromptTokenDetails.CachedTokens != 80 || wire.TotalTokens == nil || *wire.TotalTokens != 110 {
		t.Fatalf("缓存 usage 反向编码错误: %+v", wire)
	}
}

func TestParseRequestMaxCompletionTokens(t *testing.T) {
	tests := []struct {
		name    string
		limits  string
		want    int
		wantErr bool
	}{
		{name: "modern-only", limits: `"max_completion_tokens":2048`, want: 2048},
		{name: "same-values", limits: `"max_tokens":2048,"max_completion_tokens":2048`, want: 2048},
		{name: "conflict", limits: `"max_tokens":1024,"max_completion_tokens":2048`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"model":"gpt-4o","messages":[],%s}`, tt.limits)
			got, err := (&Adapter{}).ParseRequest([]byte(raw))
			if tt.wantErr {
				if err == nil {
					t.Fatal("冲突字段应被拒绝")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRequest 失败: %v", err)
			}
			if got.MaxTokens == nil || *got.MaxTokens != tt.want {
				t.Fatalf("MaxTokens = %v, want %d", got.MaxTokens, tt.want)
			}
		})
	}
}

func TestParseRequestRejectsMultipleChoices(t *testing.T) {
	for _, n := range []int{-1, 0, 2, 10} {
		raw := fmt.Sprintf(`{"model":"gpt-4o","messages":[],"n":%d}`, n)
		if _, err := (&Adapter{}).ParseRequest([]byte(raw)); err == nil {
			t.Fatalf("n=%d 必须在打上游前拒绝", n)
		}
	}
	if _, err := (&Adapter{}).ParseRequest([]byte(`{"model":"gpt-4o","messages":[],"n":1}`)); err != nil {
		t.Fatalf("n=1 应允许: %v", err)
	}
}

func TestParseResponseRejectsUnexpectedMultipleChoices(t *testing.T) {
	raw := []byte(`{"id":"c1","model":"gpt-4o","choices":[` +
		`{"index":0,"message":{"role":"assistant","content":"a"},"finish_reason":"stop"},` +
		`{"index":1,"message":{"role":"assistant","content":"b"},"finish_reason":"stop"}` +
		`]}`)
	if _, err := (&Adapter{}).ParseResponse(raw); err == nil {
		t.Fatal("n=1 的异常多 choice 响应必须显式拒绝，不能静默丢弃")
	}
}

func TestBuildStreamRequestForcesUsage(t *testing.T) {
	raw, err := (&Adapter{}).BuildRequest(&canonical.Request{Model: "gpt-4o", Stream: true})
	if err != nil {
		t.Fatalf("BuildRequest 失败: %v", err)
	}
	var got chatRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析构造结果失败: %v", err)
	}
	if got.StreamOptions == nil || !got.StreamOptions.IncludeUsage {
		t.Fatalf("流式请求必须强制 include_usage=true: %s", raw)
	}
}

func TestParseRequestStopStringOrArray(t *testing.T) {
	tests := []struct {
		name string
		stop string
		want []string
	}{
		{name: "string", stop: `"END"`, want: []string{"END"}},
		{name: "array", stop: `["END","STOP"]`, want: []string{"END", "STOP"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"model":"gpt-4o","messages":[],"stop":%s}`, tt.stop)
			got, err := (&Adapter{}).ParseRequest([]byte(raw))
			if err != nil {
				t.Fatalf("ParseRequest 失败: %v", err)
			}
			if len(got.Stop) != len(tt.want) {
				t.Fatalf("stop = %#v, want %#v", got.Stop, tt.want)
			}
			for i := range tt.want {
				if got.Stop[i] != tt.want[i] {
					t.Fatalf("stop = %#v, want %#v", got.Stop, tt.want)
				}
			}
		})
	}
}

func TestToolArgumentsRequestPreservesRawJSONAndLargeIntegers(t *testing.T) {
	const arguments = `{"order_id":9007199254740993,"nested":{"items":[{"id":9223372036854775807}]}}`
	quoted, _ := json.Marshal(arguments)
	raw := fmt.Sprintf(`{"model":"gpt-4o","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"submit","arguments":%s}}]}]}`, quoted)

	got, err := (&Adapter{}).ParseRequest([]byte(raw))
	if err != nil {
		t.Fatalf("ParseRequest 失败: %v", err)
	}
	block := got.Messages[0].Content[0]
	if string(block.ToolInputJSON) != arguments {
		t.Fatalf("原始 arguments 被改写: %q", block.ToolInputJSON)
	}
	if id, ok := block.ToolInput["order_id"].(json.Number); !ok || id.String() != "9007199254740993" {
		t.Fatalf("大整数对象视图丢失精度: %#v", block.ToolInput["order_id"])
	}
	nested := block.ToolInput["nested"].(map[string]any)
	items := nested["items"].([]any)
	deepID := items[0].(map[string]any)["id"].(json.Number)
	if deepID.String() != "9223372036854775807" {
		t.Fatalf("深层大整数丢失精度: %s", deepID)
	}

	built, err := (&Adapter{}).BuildRequest(got)
	if err != nil {
		t.Fatalf("BuildRequest 失败: %v", err)
	}
	var wire chatRequest
	if err := json.Unmarshal(built, &wire); err != nil {
		t.Fatalf("解析构造结果失败: %v", err)
	}
	if gotArgs := wire.Messages[0].ToolCalls[0].Function.Arguments; gotArgs != arguments {
		t.Fatalf("arguments 往返后被改写: %q", gotArgs)
	}
}

func TestToolArgumentsResponsePreservesTruncatedAndNonObjectJSON(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
	}{
		{name: "empty", arguments: ``},
		{name: "truncated", arguments: `{"order_id":`},
		{name: "array", arguments: `[9007199254740993,{"id":9223372036854775807}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quoted, _ := json.Marshal(tt.arguments)
			raw := fmt.Sprintf(`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"submit","arguments":%s}}]},"finish_reason":"tool_calls"}]}`, quoted)
			parsed, err := (&Adapter{}).ParseResponse([]byte(raw))
			if err != nil {
				t.Fatalf("ParseResponse 不应拒绝原始 arguments: %v", err)
			}
			block := parsed.Content[0]
			if string(block.ToolInputJSON) != tt.arguments || block.ToolInput != nil {
				t.Fatalf("原始 arguments 未保真: raw=%q object=%#v", block.ToolInputJSON, block.ToolInput)
			}
			built, err := (&Adapter{}).BuildResponse(parsed)
			if err != nil {
				t.Fatalf("BuildResponse 失败: %v", err)
			}
			var wire chatResponse
			if err := json.Unmarshal(built, &wire); err != nil {
				t.Fatalf("解析构造结果失败: %v", err)
			}
			if got := wire.Choices[0].Message.ToolCalls[0].Function.Arguments; got != tt.arguments {
				t.Fatalf("arguments 往返后被改写: %q", got)
			}
		})
	}
}

// TestToolChoiceMapping 验证 tool_choice 各形态双向映射。
func TestToolChoiceMapping(t *testing.T) {
	cases := []struct {
		tc   *canonical.ToolChoice
		want canonical.ToolChoiceType
	}{
		{&canonical.ToolChoice{Type: canonical.ToolChoiceAuto}, canonical.ToolChoiceAuto},
		{&canonical.ToolChoice{Type: canonical.ToolChoiceNone}, canonical.ToolChoiceNone},
		{&canonical.ToolChoice{Type: canonical.ToolChoiceAny}, canonical.ToolChoiceAny},
		{&canonical.ToolChoice{Type: canonical.ToolChoiceTool, Name: "f"}, canonical.ToolChoiceTool},
	}
	for _, c := range cases {
		v := buildToolChoice(c.tc)
		got := parseToolChoice(v)
		if got == nil || got.Type != c.want {
			t.Errorf("tool_choice %v 映射错误: 得到 %+v", c.want, got)
		}
	}
}
