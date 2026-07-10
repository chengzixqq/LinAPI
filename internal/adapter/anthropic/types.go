// Package anthropic 实现 Anthropic Messages 格式（Claude）的适配器。
//
// Claude 采用 content-block 结构，与内部规范模型（canonical）高度同构，
// 因此转换比 OpenAI 更直接：多数 block 一一对应，无需展开/折叠。
package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ---- Anthropic Messages 线格式结构 ----

// messagesRequest 对应 POST /v1/messages 的请求体。
type messagesRequest struct {
	Model       string         `json:"model"`
	System      any            `json:"system,omitempty"` // 字符串或 content-block 数组
	Messages    []message      `json:"messages"`
	Tools       []tool         `json:"tools,omitempty"`
	ToolChoice  *toolChoice    `json:"tool_choice,omitempty"`
	MaxTokens   int            `json:"max_tokens"` // Claude 必填
	Temperature *float64       `json:"temperature,omitempty"`
	TopP        *float64       `json:"top_p,omitempty"`
	StopSeqs    []string       `json:"stop_sequences,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// message 是一条 Claude 消息，content 为 block 数组。
type message struct {
	Role    string         `json:"role"` // user | assistant
	Content messageContent `json:"content"`
}

// messageContent 兼容 Anthropic message.content 的字符串与 block 数组联合格式。
// 入向统一归一成 block 切片，出向沿用数组表示以保留现有适配器行为。
type messageContent []block

func (c *messageContent) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case bytes.Equal(trimmed, []byte("null")):
		*c = nil
		return nil
	case len(trimmed) > 0 && trimmed[0] == '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return fmt.Errorf("message.content 字符串无效: %w", err)
		}
		*c = messageContent{{Type: "text", Text: text}}
		return nil
	default:
		var blocks []block
		if err := json.Unmarshal(trimmed, &blocks); err != nil {
			return fmt.Errorf("message.content 必须是字符串或 block 数组: %w", err)
		}
		*c = messageContent(blocks)
		return nil
	}
}

// block 是一个 content block，涵盖各类型。用一个结构承载所有字段，
// 按 Type 取用（Anthropic 线格式本身也是这种带 type 的联合结构）。
type block struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	// image / tool_result 中的 image
	Source *imageSource `json:"source,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Content 可为字符串或 block 数组，用 any 承载。
	Content any  `json:"content,omitempty"`
	IsError bool `json:"is_error,omitempty"`

	// cache_control：{"type":"ephemeral"}
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// imageSource 描述图片来源。
type imageSource struct {
	Type      string `json:"type"`                 // "base64" | "url"
	MediaType string `json:"media_type,omitempty"` // base64 时
	Data      string `json:"data,omitempty"`       // base64 时
	URL       string `json:"url,omitempty"`        // url 时
}

// tool 是工具定义。
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// toolChoice 控制工具选择。
type toolChoice struct {
	Type string `json:"type"`           // auto | any | tool | none
	Name string `json:"name,omitempty"` // type==tool 时
}

// ---- Anthropic 响应结构 ----

// messagesResponse 对应非流式响应体。
type messagesResponse struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"` // "message"
	Role       string  `json:"role"`
	Model      string  `json:"model"`
	Content    []block `json:"content"`
	StopReason string  `json:"stop_reason,omitempty"`
	Usage      *usage  `json:"usage,omitempty"`
}

type usage struct {
	// 指针用于区分字段缺失与上游明确返回 0。
	InputTokens              *int `json:"input_tokens,omitempty"`
	OutputTokens             *int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int  `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int  `json:"cache_read_input_tokens,omitempty"`
}
