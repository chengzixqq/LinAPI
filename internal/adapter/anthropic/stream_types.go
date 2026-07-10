package anthropic

// ---- Anthropic 流式 SSE 事件结构 ----
//
// Claude 流式事件类型：
//   message_start        -> 含 message 骨架与初始 usage
//   content_block_start  -> 某 index 的 block 开始（text / tool_use / thinking）
//   content_block_delta  -> 该 block 的增量（text_delta / input_json_delta / thinking_delta）
//   content_block_stop   -> 该 block 结束
//   message_delta        -> 顶层增量（stop_reason、累计 output usage）
//   message_stop         -> 消息结束
//   ping                 -> 心跳
//   error                -> 错误
//
// 这些与 canonical 事件几乎一一对应，转换非常直接。

// streamEvent 是解析 SSE data 行得到的通用事件结构。
type streamEvent struct {
	Type string `json:"type"`

	// message_start
	Message *messagesResponse `json:"message,omitempty"`

	// content_block_start / content_block_delta / content_block_stop
	Index        int          `json:"index,omitempty"`
	ContentBlock *block       `json:"content_block,omitempty"`
	Delta        *streamDelta `json:"delta,omitempty"`

	// message_delta 顶层 usage
	Usage *usage `json:"usage,omitempty"`

	// error
	Error *streamError `json:"error,omitempty"`
}

// streamDelta 承载各类 delta 的字段。
type streamDelta struct {
	// text_delta
	Text string `json:"text,omitempty"`
	// input_json_delta（工具参数分片，纯文本 JSON 片段）
	PartialJSON string `json:"partial_json,omitempty"`
	// thinking_delta
	Thinking string `json:"thinking,omitempty"`
	// signature_delta
	Signature string `json:"signature,omitempty"`

	// message_delta 里的顶层字段
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
}

type streamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
