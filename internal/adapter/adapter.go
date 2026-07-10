// Package adapter 定义供应商适配器接口及其注册表。
//
// 每个供应商（OpenAI、Claude、Gemini……）实现一个 Adapter，
// 负责在“供应商线格式”与“内部规范格式（canonical）”之间双向转换。
//
// 请求处理的两端：
//   - 入向 by 客户端格式：用对应 Adapter 的 ParseRequest 把客户端请求解析为 canonical
//   - 出向 by 上游渠道格式：用对应 Adapter 的 BuildRequest 把 canonical 构造成上游请求
//
// 当入向与出向是同一 Adapter 时，路由层可短路为“直通”，实现零损耗保真。
package adapter

import "linapi/internal/canonical"

// Adapter 在某供应商线格式与内部规范格式之间双向转换。
//
// 约定：Adapter 的方法都是无状态、并发安全的——同一 Adapter 实例会被
// 多个请求 goroutine 并发调用。流式转换需要跨块状态，因此不放在这里，
// 而是通过 NewStreamDecoder / NewStreamEncoder 工厂方法创建每请求独立的
// 有状态编解码器。
type Adapter interface {
	// Name 返回供应商标识（如 "openai"、"anthropic"）。
	Name() string

	// ParseRequest 把供应商线格式的请求体解析为规范请求。
	ParseRequest(raw []byte) (*canonical.Request, error)

	// BuildRequest 把规范请求构造为供应商线格式的请求体。
	BuildRequest(req *canonical.Request) ([]byte, error)

	// ParseResponse 把供应商线格式的非流式响应解析为规范响应。
	ParseResponse(raw []byte) (*canonical.Response, error)

	// BuildResponse 把规范响应构造为供应商线格式的非流式响应体。
	BuildResponse(resp *canonical.Response) ([]byte, error)

	// NewStreamDecoder 创建一个有状态解码器，用于把该供应商的 SSE 流
	// 逐块解析为规范事件。每个流式请求应独立创建一个。
	NewStreamDecoder() StreamDecoder

	// NewStreamEncoder 创建一个有状态编码器，用于把规范事件流逐个
	// 编码为该供应商的 SSE 输出。每个流式请求应独立创建一个。
	NewStreamEncoder() StreamEncoder
}

// StreamDecoder 把供应商 SSE 的原始数据块流解析为规范事件。
// 有状态、非并发安全：每个流式请求独占一个实例。
type StreamDecoder interface {
	// Decode 处理一个 SSE 原始数据块（形如 "data: {...}" 的一行/一段），
	// 返回它产生的规范事件。一个块可能产生 0 个（心跳/空行）或多个事件。
	Decode(raw []byte) ([]canonical.Event, error)
}

// StreamEncoder 把规范事件逐个编码为供应商 SSE 输出字节。
// 有状态、非并发安全：每个流式请求独占一个实例。
type StreamEncoder interface {
	// Encode 把一个规范事件编码为供应商 SSE 输出字节
	// （通常形如 "event: ...\ndata: ...\n\n"）。
	// 返回 nil 表示该事件在目标格式下无需输出。
	Encode(event canonical.Event) ([]byte, error)
}
