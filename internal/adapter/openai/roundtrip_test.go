package openai

import (
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
	if len(got.System) != 1 || got.System[0].Text != "你是助手" {
		t.Errorf("System 丢失: %+v", got.System)
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
		Usage:      canonical.Usage{InputTokens: 3, OutputTokens: 2},
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
