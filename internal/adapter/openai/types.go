// Package openai 实现 OpenAI Chat Completions 格式的适配器。
//
// 负责在 OpenAI 线格式与内部规范模型（canonical）之间双向转换。
// OpenAI 采用“扁平”结构：一条 assistant 消息把文本放在 content、
// 工具调用放在独立的 tool_calls 数组；而 canonical 采用 content-block
// 结构。因此入向需要“展开”，出向需要“折叠”。
package openai

// ---- OpenAI 线格式结构 ----

// chatRequest 对应 POST /v1/chat/completions 的请求体。
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Tools       []tool        `json:"tools,omitempty"`
	ToolChoice  any           `json:"tool_choice,omitempty"` // "auto"|"none"|"required"|{type,function}
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	TopP        *float64      `json:"top_p,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	// StreamOptions.IncludeUsage 为 true 时流式末尾会带 usage。
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// chatMessage 是一条 OpenAI 消息。
// content 既可能是字符串，也可能是多模态数组，故用 json.RawMessage 延迟解析。
type chatMessage struct {
	Role       string     `json:"role"` // system|user|assistant|tool
	Content    any        `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`   // role==assistant 时可能出现
	ToolCallID string     `json:"tool_call_id,omitempty"` // role==tool 时，指向被回应的调用
	Name       string     `json:"name,omitempty"`
}

// toolCall 是 assistant 发起的一次工具调用。
type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // 目前恒为 "function"
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name string `json:"name"`
	// Arguments 是 JSON 字符串（注意：是字符串，不是对象）。
	Arguments string `json:"arguments"`
}

// tool 是一个工具定义。
type tool struct {
	Type     string       `json:"type"` // "function"
	Function functionDefn `json:"function"`
}

type functionDefn struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"` // JSON Schema
}

// contentPart 表示多模态 content 数组中的一项。
type contentPart struct {
	Type     string    `json:"type"` // "text" | "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"` // 可为 http(s) 链接或 data:image/...;base64, 前缀的内联数据
}

// ---- OpenAI 响应结构 ----

// chatResponse 对应非流式响应体。
type chatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage,omitempty"`
}

type choice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message,omitempty"`
	FinishReason *string     `json:"finish_reason,omitempty"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// streamChunk 对应流式响应中每个 data: 块的结构。
type streamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *usage         `json:"usage,omitempty"`
}

// streamChoice 是流式响应中的一个 choice，内容在 delta 里增量下发。
type streamChoice struct {
	Index        int          `json:"index"`
	Delta        *streamDelta `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason,omitempty"`
}

// streamDelta 是流式增量内容。
type streamDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   any              `json:"content,omitempty"`
	ToolCalls []streamToolCall `json:"tool_calls,omitempty"`
}

// streamToolCall 是流式工具调用分片。首片带 id+name，后续片仅带 arguments 片段；
// index 标识这是第几个工具调用（用于把分片归并到同一调用）。
type streamToolCall struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function functionCall `json:"function"`
}
