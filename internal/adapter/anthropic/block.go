package anthropic

import (
	"linapi/internal/canonical"
)

// ---- block <-> canonical.ContentBlock 双向转换 ----
// Claude 的 block 与 canonical.ContentBlock 高度同构，这里集中处理，
// 请求/响应两个方向复用。

// blockToCanonical 把一个 Anthropic block 转为规范 content block。
func blockToCanonical(b block) canonical.ContentBlock {
	out := canonical.ContentBlock{CacheControl: b.CacheControl != nil}

	switch b.Type {
	case "text":
		out.Type = canonical.BlockText
		out.Text = b.Text

	case "thinking":
		out.Type = canonical.BlockThinking
		out.Thinking = b.Thinking
		out.ThinkingSignature = b.Signature

	case "image":
		out.Type = canonical.BlockImage
		out.Image = imageSourceToCanonical(b.Source)

	case "tool_use":
		out.Type = canonical.BlockToolUse
		out.ToolUseID = b.ID
		out.ToolName = b.Name
		out.ToolInput = b.Input

	case "tool_result":
		out.Type = canonical.BlockToolResult
		out.ToolResultID = b.ToolUseID
		out.ToolResultError = b.IsError
		out.ToolResult = toolResultContentToCanonical(b.Content)
	}

	return out
}

// canonicalToBlock 把一个规范 content block 转为 Anthropic block。
func canonicalToBlock(c canonical.ContentBlock) block {
	out := block{}
	if c.CacheControl {
		out.CacheControl = &cacheControl{Type: "ephemeral"}
	}

	switch c.Type {
	case canonical.BlockText:
		out.Type = "text"
		out.Text = c.Text

	case canonical.BlockThinking:
		out.Type = "thinking"
		out.Thinking = c.Thinking
		out.Signature = c.ThinkingSignature

	case canonical.BlockImage:
		out.Type = "image"
		out.Source = imageSourceToWire(c.Image)

	case canonical.BlockToolUse:
		out.Type = "tool_use"
		out.ID = c.ToolUseID
		out.Name = c.ToolName
		out.Input = c.ToolInput

	case canonical.BlockToolResult:
		out.Type = "tool_result"
		out.ToolUseID = c.ToolResultID
		out.IsError = c.ToolResultError
		out.Content = canonicalToToolResultContent(c.ToolResult)
	}

	return out
}

func imageSourceToCanonical(s *imageSource) *canonical.ImageSource {
	if s == nil {
		return nil
	}
	return &canonical.ImageSource{
		Type:      s.Type,
		MediaType: s.MediaType,
		Data:      s.Data,
		URL:       s.URL,
	}
}

func imageSourceToWire(s *canonical.ImageSource) *imageSource {
	if s == nil {
		return nil
	}
	return &imageSource{
		Type:      s.Type,
		MediaType: s.MediaType,
		Data:      s.Data,
		URL:       s.URL,
	}
}

// toolResultContentToCanonical 把 tool_result 的 content（字符串或 block 数组）
// 转为规范 block 列表。
func toolResultContentToCanonical(content any) []canonical.ContentBlock {
	switch v := content.(type) {
	case string:
		return []canonical.ContentBlock{{Type: canonical.BlockText, Text: v}}
	case []any:
		var blocks []canonical.ContentBlock
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			// 仅处理 text / image 两类常见结果内容。
			switch m["type"] {
			case "text":
				if t, ok := m["text"].(string); ok {
					blocks = append(blocks, canonical.ContentBlock{Type: canonical.BlockText, Text: t})
				}
			case "image":
				blocks = append(blocks, canonical.ContentBlock{Type: canonical.BlockImage})
			}
		}
		return blocks
	}
	return nil
}

// canonicalToToolResultContent 把规范 block 列表还原为 tool_result content。
// 纯文本时用字符串（Claude 更常见），否则用 block 数组。
func canonicalToToolResultContent(blocks []canonical.ContentBlock) any {
	if len(blocks) == 1 && blocks[0].Type == canonical.BlockText {
		return blocks[0].Text
	}
	var out []block
	for _, b := range blocks {
		out = append(out, canonicalToBlock(b))
	}
	return out
}
