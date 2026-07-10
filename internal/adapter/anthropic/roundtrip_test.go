package anthropic

import (
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
		Usage:      canonical.Usage{InputTokens: 10, OutputTokens: 5},
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
}
