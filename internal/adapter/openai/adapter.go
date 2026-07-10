package openai

import "linapi/internal/adapter"

// Adapter 实现 OpenAI Chat Completions 格式的转换。
// 无状态、并发安全。
type Adapter struct{}

// Name 返回供应商标识。
func (a *Adapter) Name() string { return "openai" }

// 在包被导入时自动注册到全局适配器注册表。
func init() {
	adapter.Register(&Adapter{})
}
