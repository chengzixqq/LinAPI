package openai

import (
	"encoding/json"
	"fmt"

	"linapi/internal/canonical"
)

// BuildRequest 把规范请求构造为 OpenAI 线格式（出向：content-block → 扁平折叠）。
func (a *Adapter) BuildRequest(req *canonical.Request) ([]byte, error) {
	out := chatRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      req.Stream,
	}
	if req.Stream {
		// 请求上游在流式末尾附带 usage，供计费使用。
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	// system → 一条 system 消息置于最前
	if sysText := blocksToText(req.System); sysText != "" {
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: sysText})
	}

	// 逐条消息折叠
	for _, m := range req.Messages {
		switch m.Role {
		case canonical.RoleUser:
			msgs, err := buildUserMessages(m.Content)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, msgs...)

		case canonical.RoleAssistant:
			out.Messages = append(out.Messages, buildAssistantMessage(m.Content))

		default:
			return nil, fmt.Errorf("openai: 无法构造角色 %q", m.Role)
		}
	}

	// 工具定义
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, tool{
			Type: "function",
			Function: functionDefn{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	if req.ToolChoice != nil {
		out.ToolChoice = buildToolChoice(req.ToolChoice)
	}

	return json.Marshal(out)
}

// buildAssistantMessage 把助手 content-block 折叠为一条扁平 assistant 消息：
// text block 合并进 content；tool_use block 折叠进 tool_calls 数组。
func buildAssistantMessage(blocks []canonical.ContentBlock) chatMessage {
	msg := chatMessage{Role: "assistant"}
	var text string

	for _, b := range blocks {
		switch b.Type {
		case canonical.BlockText:
			text += b.Text
		case canonical.BlockToolUse:
			args, _ := json.Marshal(b.ToolInput)
			if b.ToolInput == nil {
				args = []byte("{}")
			}
			msg.ToolCalls = append(msg.ToolCalls, toolCall{
				ID:   b.ToolUseID,
				Type: "function",
				Function: functionCall{
					Name:      b.ToolName,
					Arguments: string(args),
				},
			})
		case canonical.BlockThinking:
			// OpenAI 无原生 thinking 字段，跨格式转出时丢弃思维链正文
			// （避免污染 content）。同格式走直通不会经过这里。
		}
	}

	if text != "" {
		msg.Content = text
	}
	return msg
}

// buildUserMessages 把用户 content-block 折叠为 OpenAI 消息。
// 注意：tool_result block 在 OpenAI 里必须拆成独立的 role==tool 消息，
// 因此一条 canonical user 消息可能产出多条 OpenAI 消息。
func buildUserMessages(blocks []canonical.ContentBlock) ([]chatMessage, error) {
	var msgs []chatMessage
	var parts []contentPart
	var toolMsgs []chatMessage

	for _, b := range blocks {
		switch b.Type {
		case canonical.BlockText:
			parts = append(parts, contentPart{Type: "text", Text: b.Text})
		case canonical.BlockImage:
			if b.Image != nil {
				parts = append(parts, contentPart{
					Type:     "image_url",
					ImageURL: &imageURL{URL: imageSourceToURL(b.Image)},
				})
			}
		case canonical.BlockToolResult:
			toolMsgs = append(toolMsgs, chatMessage{
				Role:       "tool",
				ToolCallID: b.ToolResultID,
				Content:    blocksToText(b.ToolResult),
			})
		}
	}

	// 普通内容合成一条 user 消息（纯文本时用字符串，含图片时用数组）。
	if len(parts) > 0 {
		msgs = append(msgs, userMessageFromParts(parts))
	}
	// tool 结果各自成条，排在其后。
	msgs = append(msgs, toolMsgs...)
	return msgs, nil
}

// userMessageFromParts 在“纯文本”与“多模态数组”之间选择更简洁的表示。
func userMessageFromParts(parts []contentPart) chatMessage {
	if len(parts) == 1 && parts[0].Type == "text" {
		return chatMessage{Role: "user", Content: parts[0].Text}
	}
	return chatMessage{Role: "user", Content: parts}
}

// blocksToText 把若干 content block 里的文本拼接起来。
func blocksToText(blocks []canonical.ContentBlock) string {
	var s string
	for _, b := range blocks {
		if b.Type == canonical.BlockText {
			s += b.Text
		}
	}
	return s
}

// imageSourceToURL 把规范图片来源还原为 OpenAI 的 image_url 字符串。
func imageSourceToURL(img *canonical.ImageSource) string {
	if img.Type == "base64" {
		return fmt.Sprintf("data:%s;base64,%s", img.MediaType, img.Data)
	}
	return img.URL
}

// buildToolChoice 把规范 tool_choice 还原为 OpenAI 表示。
func buildToolChoice(tc *canonical.ToolChoice) any {
	switch tc.Type {
	case canonical.ToolChoiceAuto:
		return "auto"
	case canonical.ToolChoiceNone:
		return "none"
	case canonical.ToolChoiceAny:
		return "required"
	case canonical.ToolChoiceTool:
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}
	}
	return nil
}
