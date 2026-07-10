// Package all 通过空导入触发各供应商适配器包的 init() 注册。
//
// 适配器包在自己的 init() 里调用 adapter.Register，但只有被导入时才会执行。
// 主程序/转发层导入本包一处，即可让 adapter.Get("openai") / adapter.Get("anthropic")
// 全部可用；新增供应商时在此加一行空导入即可，无需改动调用方。
package all

import (
	_ "linapi/internal/adapter/anthropic"
	_ "linapi/internal/adapter/openai"
)
