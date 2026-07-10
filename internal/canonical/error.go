package canonical

// ErrorResponse 是供应商无关的 HTTP 错误响应。Type/Message 是各协议共有的
// 稳定字段；Param/Code 仅在目标协议可表达时输出。
type ErrorResponse struct {
	Type      string
	Message   string
	Param     any
	Code      any
	RequestID string
}
