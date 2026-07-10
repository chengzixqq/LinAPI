package anthropic

import "linapi/internal/adapter"

// Adapter 实现 Anthropic Messages 格式（Claude）的转换。
// 无状态、并发安全。
type Adapter struct{}

// Name 返回供应商标识。
func (a *Adapter) Name() string { return "anthropic" }

// 在包被导入时自动注册到全局适配器注册表。
func init() {
	adapter.Register(&Adapter{})
}
