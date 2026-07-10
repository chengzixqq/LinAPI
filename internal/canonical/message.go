// Package canonical 定义网关内部的规范数据模型（Canonical Model）。
//
// 设计原则：规范模型是各供应商格式的“超集”，采用 content-block 结构
// （表达力最强，接近 Claude Messages），但字段命名保持中立。
// 任何供应商格式都能无损映射进来，避免以 OpenAI 格式为内部标准时
// 丢失 thinking、结构化工具调用、多模态 block 等信息。
package canonical

import (
	"bytes"
	"encoding/json"
)

// Request 是一次对话补全请求的规范表示。
type Request struct {
	// Model 是客户端请求的模型名（如 "gpt-4o"、"claude-3-5-sonnet"）。
	// 路由层据此选择渠道，适配器再映射为上游实际模型名。
	Model string

	// System 是没有独立消息顺序语义的顶层系统提示（如 Anthropic system）。
	// OpenAI 的 system/developer 消息保存在 Messages 中，避免提升后改变顺序。
	System []ContentBlock

	// Messages 是对话消息序列。
	Messages []Message

	// Tools 是可供模型调用的工具定义。
	Tools []Tool

	// ToolChoice 控制工具调用行为：auto / any / none / 指定工具名。
	ToolChoice *ToolChoice

	// 采样参数。指针类型以区分“未设置”与“显式设为零值”。
	MaxTokens   *int
	Temperature *float64
	TopP        *float64
	Stop        []string

	// Stream 表示是否流式返回。
	Stream bool

	// Metadata 保留供应商特有、无法归一化的原始字段，
	// 直通场景下可原样带回，避免信息丢失。
	Metadata map[string]any
}

// Role 表示消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// 顶层 Request.System 与有序 RoleSystem 并存：前者表示供应商原生顶层
	// 指令，后者保留 OpenAI messages 中的真实位置。
	// Tool 结果作为 user 消息里的 ToolResult block 承载（对齐 Claude 结构）。
)

// Message 是一条对话消息，内容由若干 content block 组成。
type Message struct {
	Role    Role
	Content []ContentBlock
}

// BlockType 标识 content block 的类型。
type BlockType string

const (
	BlockText       BlockType = "text"        // 纯文本
	BlockImage      BlockType = "image"       // 图片（多模态）
	BlockThinking   BlockType = "thinking"    // 思维链（Claude extended thinking 等）
	BlockToolUse    BlockType = "tool_use"    // 模型发起的工具调用
	BlockToolResult BlockType = "tool_result" // 工具执行结果回传
)

// ContentBlock 是消息内容的最小单元。
// 采用“带类型的联合”结构：Type 决定哪些字段有效。
type ContentBlock struct {
	Type BlockType

	// BlockText：文本内容
	Text string

	// BlockImage：图片来源（URL 或 base64）
	Image *ImageSource

	// BlockThinking：思维链文本及其签名（部分供应商用于校验完整性）
	Thinking          string
	ThinkingSignature string

	// BlockToolUse：模型请求调用工具
	ToolUseID     string          // 本次调用的唯一 ID
	ToolName      string          // 被调用的工具名
	ToolInput     map[string]any  // 调用参数的对象视图（数字使用 json.Number）
	ToolInputJSON json.RawMessage // 调用参数原始 JSON；优先用于跨格式重编码

	// BlockToolResult：工具执行结果
	ToolResultID    string         // 对应的 ToolUseID
	ToolResult      []ContentBlock // 结果内容（可为文本或图片等）
	ToolResultError bool           // 该结果是否表示错误

	// CacheControl 标记该 block 参与提示缓存（如 Claude prompt caching）。
	CacheControl bool
}

// SetToolInputJSON 保存工具参数的原始 JSON，并在它是完整 JSON 对象时生成
// 向后兼容的 ToolInput 视图。UseNumber 避免大整数先转成 float64 后丢失精度。
// 不完整 JSON 仍会原样保留，供 OpenAI arguments 字符串无损转发。
func (b *ContentBlock) SetToolInputJSON(raw []byte) {
	if raw == nil {
		b.ToolInputJSON = nil
		b.ToolInput = nil
		return
	}
	b.ToolInputJSON = make(json.RawMessage, len(raw))
	copy(b.ToolInputJSON, raw)
	b.ToolInput = nil
	if !json.Valid(raw) {
		return
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return
	}
	if object, ok := value.(map[string]any); ok {
		b.ToolInput = object
	}
}

// ToolInputBytes 返回用于线格式编码的工具参数。存在原始 JSON 时始终优先，
// 否则兼容只填充旧 ToolInput 字段的调用方；两者都为空时沿用空对象语义。
func (b ContentBlock) ToolInputBytes() ([]byte, error) {
	if b.ToolInputJSON != nil {
		out := make([]byte, len(b.ToolInputJSON))
		copy(out, b.ToolInputJSON)
		return out, nil
	}
	if b.ToolInput == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(b.ToolInput)
}

// ImageSource 描述一张图片的来源。
type ImageSource struct {
	// Type 为 "url" 或 "base64"。
	Type      string
	URL       string // Type == "url"
	MediaType string // Type == "base64"，如 "image/png"
	Data      string // Type == "base64"，base64 编码数据
}

// Tool 是一个可调用工具的定义。
type Tool struct {
	Name        string
	Description string
	// InputSchema 是 JSON Schema（用 map 保留完整结构，不做强类型约束）。
	InputSchema map[string]any
}

// ToolChoiceType 表示工具选择策略。
type ToolChoiceType string

const (
	ToolChoiceAuto ToolChoiceType = "auto" // 由模型自行决定
	ToolChoiceAny  ToolChoiceType = "any"  // 必须调用某个工具
	ToolChoiceNone ToolChoiceType = "none" // 禁止调用工具
	ToolChoiceTool ToolChoiceType = "tool" // 强制调用指定工具
)

// ToolChoice 描述工具选择策略；Type == ToolChoiceTool 时 Name 有效。
type ToolChoice struct {
	Type ToolChoiceType
	Name string
}
