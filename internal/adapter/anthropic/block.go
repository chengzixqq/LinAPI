package anthropic

import (
	"encoding/json"

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
		out.SetToolInputJSON(b.Input)

	case "tool_result":
		out.Type = canonical.BlockToolResult
		out.ToolResultID = b.ToolUseID
		out.ToolResultError = b.IsError
		out.ToolResult = toolResultContentToCanonical(b.Content)
	}

	return out
}

// canonicalToBlock 把一个规范 content block 转为 Anthropic block。
func canonicalToBlock(c canonical.ContentBlock) (block, error) {
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
		input, err := c.ToolInputBytes()
		if err != nil {
			return block{}, err
		}
		out.Input = input

	case canonical.BlockToolResult:
		out.Type = "tool_result"
		out.ToolUseID = c.ToolResultID
		out.IsError = c.ToolResultError
		content, err := canonicalToToolResultContent(c.ToolResult)
		if err != nil {
			return block{}, err
		}
		out.Content = content
	}

	return out, nil
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
			raw, err := json.Marshal(item)
			if err != nil {
				continue
			}
			var wire block
			if err := json.Unmarshal(raw, &wire); err != nil {
				continue
			}
			// tool_result 当前只支持 text/image；走统一 block 转换以复用完整
			// image source 映射，避免 URL/base64 内容被丢成空图片块。
			switch wire.Type {
			case "text", "image":
				blocks = append(blocks, blockToCanonical(wire))
			}
		}
		return blocks
	}
	return nil
}

// canonicalToToolResultContent 把规范 block 列表还原为 tool_result content。
// 纯文本时用字符串（Claude 更常见），否则用 block 数组。
func canonicalToToolResultContent(blocks []canonical.ContentBlock) (any, error) {
	if len(blocks) == 1 && blocks[0].Type == canonical.BlockText {
		return blocks[0].Text, nil
	}
	var out []block
	for _, b := range blocks {
		wire, err := canonicalToBlock(b)
		if err != nil {
			return nil, err
		}
		out = append(out, wire)
	}
	return out, nil
}
