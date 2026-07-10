// Package canonical 定义网关内部的规范数据模型（Canonical Model）。
//
// 设计原则：规范模型是各供应商格式的“超集”，采用 content-block 结构
// （表达力最强，接近 Claude Messages），但字段命名保持中立。
// 任何供应商格式都能无损映射进来，避免以 OpenAI 格式为内部标准时
// 丢失 thinking、结构化工具调用、多模态 block 等信息。
package canonical

// Request 是一次对话补全请求的规范表示。
type Request struct {
	// Model 是客户端请求的模型名（如 "gpt-4o"、"claude-3-5-sonnet"）。
	// 路由层据此选择渠道，适配器再映射为上游实际模型名。
	Model string

	// System 是系统提示。各家表达不同（OpenAI 放在 messages 里，
	// Claude 是顶层字段），规范模型统一提到顶层。
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
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// 注意：System 不作为消息角色，统一提升到 Request.System。
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
	ToolUseID string         // 本次调用的唯一 ID
	ToolName  string         // 被调用的工具名
	ToolInput map[string]any // 调用参数

	// BlockToolResult：工具执行结果
	ToolResultID    string         // 对应的 ToolUseID
	ToolResult      []ContentBlock // 结果内容（可为文本或图片等）
	ToolResultError bool           // 该结果是否表示错误

	// CacheControl 标记该 block 参与提示缓存（如 Claude prompt caching）。
	CacheControl bool
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
