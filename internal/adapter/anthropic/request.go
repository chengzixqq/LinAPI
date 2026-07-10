package anthropic

import (
	"encoding/json"
	"fmt"

	"linapi/internal/canonical"
)

// ParseRequest 把 Anthropic 线格式请求解析为规范请求。
func (a *Adapter) ParseRequest(raw []byte) (*canonical.Request, error) {
	var req messagesRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("anthropic: 解析请求失败: %w", err)
	}

	out := &canonical.Request{
		Model:       req.Model,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSeqs,
		Stream:      req.Stream,
		Metadata:    req.Metadata,
	}
	if req.MaxTokens > 0 {
		mt := req.MaxTokens
		out.MaxTokens = &mt
	}

	// system：字符串或 block 数组
	out.System = parseSystem(req.System)

	// messages
	for _, m := range req.Messages {
		role := canonical.RoleUser
		if m.Role == "assistant" {
			role = canonical.RoleAssistant
		}
		msg := canonical.Message{Role: role}
		for _, b := range m.Content {
			msg.Content = append(msg.Content, blockToCanonical(b))
		}
		out.Messages = append(out.Messages, msg)
	}

	// tools
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, canonical.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	if req.ToolChoice != nil {
		out.ToolChoice = parseToolChoice(req.ToolChoice)
	}

	return out, nil
}

// BuildRequest 把规范请求构造为 Anthropic 线格式。
func (a *Adapter) BuildRequest(req *canonical.Request) ([]byte, error) {
	out := messagesRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		StopSeqs:    req.Stop,
		Stream:      req.Stream,
		Metadata:    req.Metadata,
	}

	// Claude 要求 max_tokens 必填；未指定时给一个合理上限兜底。
	if req.MaxTokens != nil {
		out.MaxTokens = *req.MaxTokens
	} else {
		out.MaxTokens = 4096
	}

	// system 作为 block 数组输出（保留 cache_control 等信息）。
	if len(req.System) > 0 {
		var sys []block
		for _, b := range req.System {
			sys = append(sys, canonicalToBlock(b))
		}
		out.System = sys
	}

	for _, m := range req.Messages {
		role := "user"
		if m.Role == canonical.RoleAssistant {
			role = "assistant"
		}
		msg := message{Role: role}
		for _, b := range m.Content {
			msg.Content = append(msg.Content, canonicalToBlock(b))
		}
		out.Messages = append(out.Messages, msg)
	}

	for _, t := range req.Tools {
		out.Tools = append(out.Tools, tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	if req.ToolChoice != nil {
		out.ToolChoice = buildToolChoice(req.ToolChoice)
	}

	return json.Marshal(out)
}

// parseSystem 解析 system 字段（字符串或 block 数组）。
func parseSystem(v any) []canonical.ContentBlock {
	switch s := v.(type) {
	case string:
		if s == "" {
			return nil
		}
		return []canonical.ContentBlock{{Type: canonical.BlockText, Text: s}}
	case []any:
		var blocks []canonical.ContentBlock
		for _, item := range s {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "text" {
				if t, ok := m["text"].(string); ok {
					cb := canonical.ContentBlock{Type: canonical.BlockText, Text: t}
					if _, has := m["cache_control"]; has {
						cb.CacheControl = true
					}
					blocks = append(blocks, cb)
				}
			}
		}
		return blocks
	}
	return nil
}

func parseToolChoice(tc *toolChoice) *canonical.ToolChoice {
	switch tc.Type {
	case "auto":
		return &canonical.ToolChoice{Type: canonical.ToolChoiceAuto}
	case "any":
		return &canonical.ToolChoice{Type: canonical.ToolChoiceAny}
	case "none":
		return &canonical.ToolChoice{Type: canonical.ToolChoiceNone}
	case "tool":
		return &canonical.ToolChoice{Type: canonical.ToolChoiceTool, Name: tc.Name}
	}
	return nil
}

func buildToolChoice(tc *canonical.ToolChoice) *toolChoice {
	switch tc.Type {
	case canonical.ToolChoiceAuto:
		return &toolChoice{Type: "auto"}
	case canonical.ToolChoiceAny:
		return &toolChoice{Type: "any"}
	case canonical.ToolChoiceNone:
		return &toolChoice{Type: "none"}
	case canonical.ToolChoiceTool:
		return &toolChoice{Type: "tool", Name: tc.Name}
	}
	return nil
}
