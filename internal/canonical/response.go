package canonical

// Response 是一次非流式补全响应的规范表示。
type Response struct {
	// ID 是本次响应的唯一标识（供应商返回的，或网关生成的）。
	ID string

	// Model 是实际产出响应的上游模型名。
	Model string

	// Role 通常为 assistant。
	Role Role

	// Content 是助手回复的内容块（文本 / 思维链 / 工具调用等）。
	Content []ContentBlock

	// StopReason 是归一化后的停止原因。
	StopReason StopReason

	// Usage 是本次请求的 token 用量，计费依赖它。
	Usage Usage
}

// StopReason 是归一化的停止原因。
// 各供应商命名不同（OpenAI: stop/length/tool_calls；Claude: end_turn/max_tokens/tool_use），
// 统一到这一组，适配器负责双向映射。
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"   // 正常结束
	StopMaxTokens StopReason = "max_tokens" // 达到长度上限
	StopToolUse   StopReason = "tool_use"   // 为调用工具而停止
	StopStop      StopReason = "stop"       // 命中停止序列
	StopError     StopReason = "error"      // 异常终止
)

// Usage 记录 token 用量。
type Usage struct {
	InputTokens  int
	OutputTokens int

	// *Known 区分“上游明确返回 0”与“字段完全缺失”。计费只有在字段存在性
	// 可证明时才使用精确 token 结算；缺失或冲突时必须走保守结算，不能把 Go
	// 零值误当成零成本（审查 AUD-P0-02 / AUD-P0-06）。
	InputTokensKnown  bool
	OutputTokensKnown bool

	// ReportedTotalTokens 保存上游显式返回的 total_tokens。OpenAI 某些兼容
	// 上游只返回 total，或只返回 total + 单边 token；保留它才能安全推导缺失边，
	// 或按较高单价做保守结算。
	ReportedTotalTokens int
	TotalTokensKnown    bool

	// 缓存相关（Claude prompt caching 等），无则为 0。
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// TotalTokens 返回普通输入、缓存创建、缓存读取与输出 token 之和。
func (u Usage) TotalTokens() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens + u.OutputTokens
}

// Complete 返回输入与输出 token 是否都由上游明确给出或可可靠推导。
func (u Usage) Complete() bool {
	return u.InputTokensKnown && u.OutputTokensKnown
}

// ---- 流式事件 ----

// EventType 标识流式事件的类型。
// 设计为语义化的增量事件，而非绑定某家 SSE 格式，
// 适配器负责在“供应商 SSE 分块 <-> 规范事件”之间双向转换。
type EventType string

const (
	EventMessageStart EventType = "message_start" // 消息开始（携带初始元信息）
	EventBlockStart   EventType = "block_start"   // 一个 content block 开始
	EventBlockDelta   EventType = "block_delta"   // content block 的增量内容
	EventBlockStop    EventType = "block_stop"    // 一个 content block 结束
	EventMessageDelta EventType = "message_delta" // 消息级增量（如 stop_reason、usage）
	EventMessageStop  EventType = "message_stop"  // 消息结束
	EventPing         EventType = "ping"          // 心跳（可忽略）
	EventError        EventType = "error"         // 流中错误
)

// Event 是一个规范流式事件。
// 不同 Type 使用不同字段，未用字段留零值。
type Event struct {
	Type EventType

	// BlockIndex 标识事件作用于第几个 content block（block_* 事件）。
	BlockIndex int

	// BlockType 在 EventBlockStart 时说明新块的类型。
	BlockType BlockType

	// Delta 承载增量内容（EventBlockDelta）。
	Delta *Delta

	// StopReason 在 EventMessageDelta 时可能出现。
	StopReason StopReason

	// Usage 在 EventMessageStart / EventMessageDelta 时可能携带（增量或最终）。
	Usage *Usage
	// UsageFinal 表示该 usage 是供应商声明的最终用量。流式结算必须同时看到
	// message_stop 与最终用量；仅有 message_start 的输入 token 不足以精确结算。
	UsageFinal bool

	// ID / Model 在 EventMessageStart 时携带。
	ID    string
	Model string

	// Err 在 EventError 时描述错误信息。
	Err string
}

// Delta 是流式增量的具体内容，按块类型区分。
type Delta struct {
	// 文本增量（BlockText）
	Text string

	// 思维链增量（BlockThinking）
	Thinking          string
	ThinkingSignature string

	// 工具调用参数增量（BlockToolUse）：
	// 多数供应商以 JSON 字符串分片的形式流式下发工具参数。
	ToolUseID        string
	ToolName         string
	PartialInputJSON string
}
