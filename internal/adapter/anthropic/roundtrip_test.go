package anthropic

import (
	"encoding/json"
	"fmt"
	"testing"

	"linapi/internal/canonical"
)

// 构造一个覆盖多种 block 类型的规范请求，用于 round-trip 测试。
func sampleRequest() *canonical.Request {
	mt := 1024
	temp := 0.7
	return &canonical.Request{
		Model:       "claude-3-5-sonnet",
		MaxTokens:   &mt,
		Temperature: &temp,
		System: []canonical.ContentBlock{
			{Type: canonical.BlockText, Text: "你是一个助手"},
		},
		Messages: []canonical.Message{
			{
				Role: canonical.RoleUser,
				Content: []canonical.ContentBlock{
					{Type: canonical.BlockText, Text: "北京天气如何"},
				},
			},
			{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentBlock{
					{Type: canonical.BlockText, Text: "我查一下"},
					{
						Type:      canonical.BlockToolUse,
						ToolUseID: "toolu_1",
						ToolName:  "get_weather",
						ToolInput: map[string]any{"city": "北京"},
					},
				},
			},
			{
				Role: canonical.RoleUser,
				Content: []canonical.ContentBlock{
					{
						Type:         canonical.BlockToolResult,
						ToolResultID: "toolu_1",
						ToolResult:   []canonical.ContentBlock{{Type: canonical.BlockText, Text: "晴，25度"}},
					},
				},
			},
		},
		Tools: []canonical.Tool{
			{
				Name:        "get_weather",
				Description: "查询天气",
				InputSchema: map[string]any{"type": "object"},
			},
		},
		ToolChoice: &canonical.ToolChoice{Type: canonical.ToolChoiceAuto},
	}
}

// TestRequestRoundTrip 验证 canonical -> Anthropic 线格式 -> canonical 不丢信息。
func TestRequestRoundTrip(t *testing.T) {
	a := &Adapter{}
	orig := sampleRequest()

	raw, err := a.BuildRequest(orig)
	if err != nil {
		t.Fatalf("BuildRequest 失败: %v", err)
	}

	got, err := a.ParseRequest(raw)
	if err != nil {
		t.Fatalf("ParseRequest 失败: %v", err)
	}

	if got.Model != orig.Model {
		t.Errorf("Model 不一致: 期望 %q 得到 %q", orig.Model, got.Model)
	}
	if got.MaxTokens == nil || *got.MaxTokens != *orig.MaxTokens {
		t.Errorf("MaxTokens 不一致: 期望 %v", *orig.MaxTokens)
	}
	if len(got.System) != 1 || got.System[0].Text != "你是一个助手" {
		t.Errorf("System 丢失或不一致: %+v", got.System)
	}
	if len(got.Messages) != len(orig.Messages) {
		t.Fatalf("消息数不一致: 期望 %d 得到 %d", len(orig.Messages), len(got.Messages))
	}

	// 校验工具调用 block 完整保留
	asst := got.Messages[1]
	if len(asst.Content) != 2 {
		t.Fatalf("assistant block 数不一致: 期望 2 得到 %d", len(asst.Content))
	}
	tu := asst.Content[1]
	if tu.Type != canonical.BlockToolUse || tu.ToolName != "get_weather" || tu.ToolUseID != "toolu_1" {
		t.Errorf("tool_use block 丢失或不一致: %+v", tu)
	}
	if tu.ToolInput["city"] != "北京" {
		t.Errorf("tool_use 参数丢失: %+v", tu.ToolInput)
	}

	// 校验 tool_result
	tr := got.Messages[2].Content[0]
	if tr.Type != canonical.BlockToolResult || tr.ToolResultID != "toolu_1" {
		t.Errorf("tool_result block 丢失或不一致: %+v", tr)
	}

	// 校验工具定义与 tool_choice
	if len(got.Tools) != 1 || got.Tools[0].Name != "get_weather" {
		t.Errorf("工具定义丢失: %+v", got.Tools)
	}
	if got.ToolChoice == nil || got.ToolChoice.Type != canonical.ToolChoiceAuto {
		t.Errorf("tool_choice 丢失或不一致: %+v", got.ToolChoice)
	}
}

// TestResponseRoundTrip 验证响应方向 round-trip，含 thinking block（Claude 特有）。
func TestResponseRoundTrip(t *testing.T) {
	a := &Adapter{}
	orig := &canonical.Response{
		ID:    "msg_1",
		Model: "claude-3-5-sonnet",
		Role:  canonical.RoleAssistant,
		Content: []canonical.ContentBlock{
			{Type: canonical.BlockThinking, Thinking: "让我想想", ThinkingSignature: "sig_abc"},
			{Type: canonical.BlockText, Text: "答案是 42"},
		},
		StopReason: canonical.StopEndTurn,
		Usage: canonical.Usage{
			InputTokens: 10, OutputTokens: 5,
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

	if len(got.Content) != 2 {
		t.Fatalf("响应 block 数不一致: 期望 2 得到 %d", len(got.Content))
	}
	think := got.Content[0]
	if think.Type != canonical.BlockThinking || think.Thinking != "让我想想" {
		t.Errorf("thinking block 丢失或不一致: %+v", think)
	}
	if think.ThinkingSignature != "sig_abc" {
		t.Errorf("thinking 签名丢失: %q", think.ThinkingSignature)
	}
	if got.StopReason != canonical.StopEndTurn {
		t.Errorf("stop_reason 不一致: %q", got.StopReason)
	}
	if got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 5 {
		t.Errorf("usage 不一致: %+v", got.Usage)
	}
	if !got.Usage.InputTokensKnown || !got.Usage.OutputTokensKnown {
		t.Errorf("usage 字段存在性丢失: %+v", got.Usage)
	}
}

func TestParseRequestRejectsNonPositiveMaxTokens(t *testing.T) {
	for _, value := range []int{-1, 0} {
		raw := fmt.Sprintf(`{"model":"claude","max_tokens":%d,"messages":[]}`, value)
		if _, err := (&Adapter{}).ParseRequest([]byte(raw)); err == nil {
			t.Fatalf("max_tokens=%d 必须拒绝", value)
		}
	}
}

func TestParseRequestMessageContentStringOrBlocks(t *testing.T) {
	raw := []byte(`{
		"model":"claude","max_tokens":128,
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[{"type":"text","text":"world"}]}
		]
	}`)
	got, err := (&Adapter{}).ParseRequest(raw)
	if err != nil {
		t.Fatalf("ParseRequest 失败: %v", err)
	}
	if len(got.Messages) != 2 || len(got.Messages[0].Content) != 1 || got.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("字符串 content 未归一为文本 block: %+v", got.Messages)
	}
	if len(got.Messages[1].Content) != 1 || got.Messages[1].Content[0].Text != "world" {
		t.Fatalf("数组 content 解析异常: %+v", got.Messages[1])
	}
}

func TestToolInputRequestPreservesRawJSONAndLargeIntegers(t *testing.T) {
	const input = `{"order_id":9007199254740993,"nested":{"items":[{"id":9223372036854775807}]}}`
	raw := []byte(`{"model":"claude","max_tokens":128,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"submit","input":` + input + `}]}]}`)
	got, err := (&Adapter{}).ParseRequest(raw)
	if err != nil {
		t.Fatalf("ParseRequest 失败: %v", err)
	}
	block := got.Messages[0].Content[0]
	if string(block.ToolInputJSON) != input {
		t.Fatalf("原始 input 被改写: %q", block.ToolInputJSON)
	}
	if id, ok := block.ToolInput["order_id"].(json.Number); !ok || id.String() != "9007199254740993" {
		t.Fatalf("大整数对象视图丢失精度: %#v", block.ToolInput["order_id"])
	}
	nested := block.ToolInput["nested"].(map[string]any)
	items := nested["items"].([]any)
	if id := items[0].(map[string]any)["id"].(json.Number); id.String() != "9223372036854775807" {
		t.Fatalf("深层大整数丢失精度: %s", id)
	}

	built, err := (&Adapter{}).BuildRequest(got)
	if err != nil {
		t.Fatalf("BuildRequest 失败: %v", err)
	}
	var wire messagesRequest
	if err := json.Unmarshal(built, &wire); err != nil {
		t.Fatalf("解析构造结果失败: %v", err)
	}
	if rebuilt := string(wire.Messages[0].Content[0].Input); rebuilt != input {
		t.Fatalf("input 往返后被改写: %s", rebuilt)
	}
}

func TestToolInputResponsePreservesNonObjectJSON(t *testing.T) {
	const input = `[9007199254740993,{"id":9223372036854775807}]`
	raw := []byte(`{"id":"m1","type":"message","role":"assistant","model":"claude","content":[{"type":"tool_use","id":"toolu_1","name":"submit","input":` + input + `}],"stop_reason":"tool_use"}`)
	parsed, err := (&Adapter{}).ParseResponse(raw)
	if err != nil {
		t.Fatalf("ParseResponse 失败: %v", err)
	}
	block := parsed.Content[0]
	if string(block.ToolInputJSON) != input || block.ToolInput != nil {
		t.Fatalf("非对象 input 未保真: raw=%q object=%#v", block.ToolInputJSON, block.ToolInput)
	}
	built, err := (&Adapter{}).BuildResponse(parsed)
	if err != nil {
		t.Fatalf("BuildResponse 失败: %v", err)
	}
	var wire messagesResponse
	if err := json.Unmarshal(built, &wire); err != nil {
		t.Fatalf("解析构造结果失败: %v", err)
	}
	if rebuilt := string(wire.Content[0].Input); rebuilt != input {
		t.Fatalf("input 往返后被改写: %s", rebuilt)
	}
}

func TestToolResultContentRoundTripPreservesImageSources(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"model":"claude","max_tokens":128,"stream":%t,
				"messages":[{"role":"user","content":[
					{"type":"tool_result","tool_use_id":"call_text","content":"plain result"},
					{"type":"tool_result","tool_use_id":"call_mixed","content":[
						{"type":"text","text":"mixed result"},
						{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},
						{"type":"image","source":{"type":"url","url":"https://example.com/result.png"}}
					]}
				]}]
			}`, stream))

			parsed, err := (&Adapter{}).ParseRequest(raw)
			if err != nil {
				t.Fatalf("ParseRequest 失败: %v", err)
			}
			assertToolResultImages(t, parsed)

			built, err := (&Adapter{}).BuildRequest(parsed)
			if err != nil {
				t.Fatalf("BuildRequest 失败: %v", err)
			}
			roundTripped, err := (&Adapter{}).ParseRequest(built)
			if err != nil {
				t.Fatalf("往返后 ParseRequest 失败: %v", err)
			}
			if roundTripped.Stream != stream {
				t.Fatalf("stream 标记丢失: got=%t want=%t", roundTripped.Stream, stream)
			}
			assertToolResultImages(t, roundTripped)
		})
	}
}

func assertToolResultImages(t *testing.T, req *canonical.Request) {
	t.Helper()
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 2 {
		t.Fatalf("tool_result 消息结构异常: %+v", req.Messages)
	}
	plain := req.Messages[0].Content[0]
	if plain.Type != canonical.BlockToolResult || len(plain.ToolResult) != 1 || plain.ToolResult[0].Text != "plain result" {
		t.Fatalf("字符串 tool_result 丢失: %+v", plain)
	}
	mixed := req.Messages[0].Content[1]
	if mixed.Type != canonical.BlockToolResult || len(mixed.ToolResult) != 3 || mixed.ToolResult[0].Text != "mixed result" {
		t.Fatalf("混合 tool_result 结构异常: %+v", mixed)
	}
	base64Image := mixed.ToolResult[1].Image
	if base64Image == nil || base64Image.Type != "base64" || base64Image.MediaType != "image/png" || base64Image.Data != "iVBORw0KGgo=" {
		t.Fatalf("base64 图片 source 丢失: %+v", base64Image)
	}
	urlImage := mixed.ToolResult[2].Image
	if urlImage == nil || urlImage.Type != "url" || urlImage.URL != "https://example.com/result.png" {
		t.Fatalf("URL 图片 source 丢失: %+v", urlImage)
	}
}

func TestParseResponseUsagePresence(t *testing.T) {
	tests := []struct {
		name                    string
		usageField              string
		input, output           int
		inputKnown, outputKnown bool
	}{
		{name: "missing"},
		{name: "null", usageField: `,"usage":null`},
		{name: "empty", usageField: `,"usage":{}`},
		{name: "input-only", usageField: `,"usage":{"input_tokens":3}`, input: 3, inputKnown: true},
		{name: "output-only", usageField: `,"usage":{"output_tokens":4}`, output: 4, outputKnown: true},
		{name: "explicit-zero", usageField: `,"usage":{"input_tokens":0,"output_tokens":0}`, inputKnown: true, outputKnown: true},
		{name: "complete", usageField: `,"usage":{"input_tokens":3,"output_tokens":4}`, input: 3, output: 4, inputKnown: true, outputKnown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"id":"m1","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":"end_turn"%s}`, tt.usageField)
			got, err := (&Adapter{}).ParseResponse([]byte(raw))
			if err != nil {
				t.Fatalf("ParseResponse 失败: %v", err)
			}
			u := got.Usage
			if u.InputTokens != tt.input || u.OutputTokens != tt.output ||
				u.InputTokensKnown != tt.inputKnown || u.OutputTokensKnown != tt.outputKnown {
				t.Fatalf("usage 映射错误: %+v", u)
			}
		})
	}
}
