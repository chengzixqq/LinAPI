package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"linapi/internal/canonical"
)

// ParseRequest 把 OpenAI 线格式请求解析为规范请求（入向：扁平 → content-block 展开）。
func (a *Adapter) ParseRequest(raw []byte) (*canonical.Request, error) {
	var req chatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("openai: 解析请求失败: %w", err)
	}

	out := &canonical.Request{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      req.Stream,
	}

	// 工具定义
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, canonical.Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}
	if tc := parseToolChoice(req.ToolChoice); tc != nil {
		out.ToolChoice = tc
	}

	// 消息：system 提升到顶层；tool 结果并入前一条 user 消息的 ToolResult block。
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			blocks, err := parseContentToText(m.Content)
			if err != nil {
				return nil, err
			}
			out.System = append(out.System, blocks...)

		case "user":
			blocks, err := parseUserContent(m.Content)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, canonical.Message{
				Role:    canonical.RoleUser,
				Content: blocks,
			})

		case "assistant":
			blocks, err := parseAssistantContent(m)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: blocks,
			})

		case "tool":
			// OpenAI 的 tool 结果是独立消息；canonical 里作为 user 消息中的
			// tool_result block。若上一条已是 user，则并入；否则新建一条 user。
			text, err := contentToString(m.Content)
			if err != nil {
				return nil, err
			}
			block := canonical.ContentBlock{
				Type:         canonical.BlockToolResult,
				ToolResultID: m.ToolCallID,
				ToolResult:   []canonical.ContentBlock{{Type: canonical.BlockText, Text: text}},
			}
			n := len(out.Messages)
			if n > 0 && out.Messages[n-1].Role == canonical.RoleUser {
				out.Messages[n-1].Content = append(out.Messages[n-1].Content, block)
			} else {
				out.Messages = append(out.Messages, canonical.Message{
					Role:    canonical.RoleUser,
					Content: []canonical.ContentBlock{block},
				})
			}

		default:
			return nil, fmt.Errorf("openai: 未知消息角色 %q", m.Role)
		}
	}

	return out, nil
}

// parseAssistantContent 把 assistant 消息展开为 content-block：
// content 字符串 → text block；tool_calls 数组 → 各自的 tool_use block。
func parseAssistantContent(m chatMessage) ([]canonical.ContentBlock, error) {
	var blocks []canonical.ContentBlock

	if m.Content != nil {
		text, err := contentToString(m.Content)
		if err != nil {
			return nil, err
		}
		if text != "" {
			blocks = append(blocks, canonical.ContentBlock{
				Type: canonical.BlockText,
				Text: text,
			})
		}
	}

	for _, tc := range m.ToolCalls {
		var input map[string]any
		if tc.Function.Arguments != "" {
			// arguments 是 JSON 字符串，展开为结构化 map。
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				return nil, fmt.Errorf("openai: 解析工具参数失败: %w", err)
			}
		}
		blocks = append(blocks, canonical.ContentBlock{
			Type:      canonical.BlockToolUse,
			ToolUseID: tc.ID,
			ToolName:  tc.Function.Name,
			ToolInput: input,
		})
	}

	return blocks, nil
}

// parseUserContent 解析 user 消息内容，支持纯文本与多模态数组。
func parseUserContent(content any) ([]canonical.ContentBlock, error) {
	// 纯字符串
	if s, ok := content.(string); ok {
		return []canonical.ContentBlock{{Type: canonical.BlockText, Text: s}}, nil
	}

	// 多模态数组
	parts, err := toContentParts(content)
	if err != nil {
		return nil, err
	}
	var blocks []canonical.ContentBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			blocks = append(blocks, canonical.ContentBlock{Type: canonical.BlockText, Text: p.Text})
		case "image_url":
			if p.ImageURL == nil {
				continue
			}
			blocks = append(blocks, canonical.ContentBlock{
				Type:  canonical.BlockImage,
				Image: parseImageURL(p.ImageURL.URL),
			})
		}
	}
	return blocks, nil
}

// parseContentToText 把内容解析为若干 text block（用于 system）。
func parseContentToText(content any) ([]canonical.ContentBlock, error) {
	text, err := contentToString(content)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, nil
	}
	return []canonical.ContentBlock{{Type: canonical.BlockText, Text: text}}, nil
}

// contentToString 把 content（字符串或多模态数组）拼接为纯文本。
func contentToString(content any) (string, error) {
	if content == nil {
		return "", nil
	}
	if s, ok := content.(string); ok {
		return s, nil
	}
	parts, err := toContentParts(content)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String(), nil
}

// toContentParts 把 any（来自 json 解码的 []any）还原为 []contentPart。
func toContentParts(content any) ([]contentPart, error) {
	arr, ok := content.([]any)
	if !ok {
		return nil, fmt.Errorf("openai: content 既非字符串也非数组")
	}
	// 借道 JSON 重新解码为强类型，避免手工断言每个字段。
	b, err := json.Marshal(arr)
	if err != nil {
		return nil, err
	}
	var parts []contentPart
	if err := json.Unmarshal(b, &parts); err != nil {
		return nil, fmt.Errorf("openai: 解析多模态 content 失败: %w", err)
	}
	return parts, nil
}

// parseImageURL 区分 data: 内联 base64 与普通 URL。
func parseImageURL(url string) *canonical.ImageSource {
	const dataPrefix = "data:"
	if strings.HasPrefix(url, dataPrefix) {
		// 形如 data:image/png;base64,XXXX
		if i := strings.Index(url, ","); i >= 0 {
			meta := url[len(dataPrefix):i] // image/png;base64
			mediaType := meta
			if j := strings.Index(meta, ";"); j >= 0 {
				mediaType = meta[:j]
			}
			return &canonical.ImageSource{
				Type:      "base64",
				MediaType: mediaType,
				Data:      url[i+1:],
			}
		}
	}
	return &canonical.ImageSource{Type: "url", URL: url}
}

// parseToolChoice 归一化 tool_choice 字段。
func parseToolChoice(v any) *canonical.ToolChoice {
	switch t := v.(type) {
	case string:
		switch t {
		case "auto":
			return &canonical.ToolChoice{Type: canonical.ToolChoiceAuto}
		case "none":
			return &canonical.ToolChoice{Type: canonical.ToolChoiceNone}
		case "required":
			return &canonical.ToolChoice{Type: canonical.ToolChoiceAny}
		}
	case map[string]any:
		// {"type":"function","function":{"name":"xxx"}}
		if fn, ok := t["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				return &canonical.ToolChoice{Type: canonical.ToolChoiceTool, Name: name}
			}
		}
	}
	return nil
}
