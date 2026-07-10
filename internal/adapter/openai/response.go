package openai

import (
	"encoding/json"
	"fmt"

	"linapi/internal/canonical"
)

// ParseResponse 把 OpenAI 非流式响应解析为规范响应。
func (a *Adapter) ParseResponse(raw []byte) (*canonical.Response, error) {
	var resp chatResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("openai: 解析响应失败: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai: 响应不含 choices")
	}

	ch := resp.Choices[0]
	out := &canonical.Response{
		ID:    resp.ID,
		Model: resp.Model,
		Role:  canonical.RoleAssistant,
	}

	// content 文本
	if text, err := contentToString(ch.Message.Content); err != nil {
		return nil, err
	} else if text != "" {
		out.Content = append(out.Content, canonical.ContentBlock{
			Type: canonical.BlockText,
			Text: text,
		})
	}

	// tool_calls → tool_use blocks
	for _, tc := range ch.Message.ToolCalls {
		var input map[string]any
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				return nil, fmt.Errorf("openai: 解析响应工具参数失败: %w", err)
			}
		}
		out.Content = append(out.Content, canonical.ContentBlock{
			Type:      canonical.BlockToolUse,
			ToolUseID: tc.ID,
			ToolName:  tc.Function.Name,
			ToolInput: input,
		})
	}

	if ch.FinishReason != nil {
		out.StopReason = mapFinishReasonToCanonical(*ch.FinishReason)
	}
	if resp.Usage != nil {
		out.Usage = canonical.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}
	}

	return out, nil
}

// BuildResponse 把规范响应构造为 OpenAI 非流式响应体。
func (a *Adapter) BuildResponse(resp *canonical.Response) ([]byte, error) {
	msg := chatMessage{Role: "assistant"}
	var text string

	for _, b := range resp.Content {
		switch b.Type {
		case canonical.BlockText:
			text += b.Text
		case canonical.BlockToolUse:
			args, _ := json.Marshal(b.ToolInput)
			if b.ToolInput == nil {
				args = []byte("{}")
			}
			msg.ToolCalls = append(msg.ToolCalls, toolCall{
				ID:       b.ToolUseID,
				Type:     "function",
				Function: functionCall{Name: b.ToolName, Arguments: string(args)},
			})
		}
	}
	if text != "" {
		msg.Content = text
	}

	finish := mapStopReasonToOpenAI(resp.StopReason)
	out := chatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Model:   resp.Model,
		Choices: []choice{{Index: 0, Message: msg, FinishReason: &finish}},
		Usage: &usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.TotalTokens(),
		},
	}
	return json.Marshal(out)
}

// mapFinishReasonToCanonical 把 OpenAI finish_reason 映射为规范停止原因。
func mapFinishReasonToCanonical(r string) canonical.StopReason {
	switch r {
	case "stop":
		return canonical.StopEndTurn
	case "length":
		return canonical.StopMaxTokens
	case "tool_calls":
		return canonical.StopToolUse
	case "content_filter":
		return canonical.StopError
	default:
		return canonical.StopEndTurn
	}
}

// mapStopReasonToOpenAI 把规范停止原因映射回 OpenAI finish_reason。
func mapStopReasonToOpenAI(r canonical.StopReason) string {
	switch r {
	case canonical.StopEndTurn, canonical.StopStop:
		return "stop"
	case canonical.StopMaxTokens:
		return "length"
	case canonical.StopToolUse:
		return "tool_calls"
	case canonical.StopError:
		return "content_filter"
	default:
		return "stop"
	}
}
