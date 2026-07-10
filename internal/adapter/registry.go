package adapter

import (
	"fmt"
	"sort"
	"sync"
)

// registry 是全局适配器注册表。
//
// 加入一个新供应商只需在其包的 init() 中调用 Register，
// 无需改动路由、服务器等任何其它代码——这是保持架构可扩展、
// 不臃肿的关键。
var registry = struct {
	sync.RWMutex
	adapters map[string]Adapter
}{
	adapters: make(map[string]Adapter),
}

// Register 注册一个适配器。重复注册同名适配器会 panic，
// 以便在启动阶段就暴露配置错误（通常在 init 中调用）。
func Register(a Adapter) {
	registry.Lock()
	defer registry.Unlock()

	name := a.Name()
	if name == "" {
		panic("adapter: 注册的适配器 Name() 不能为空")
	}
	if _, exists := registry.adapters[name]; exists {
		panic(fmt.Sprintf("adapter: 重复注册适配器 %q", name))
	}
	registry.adapters[name] = a
}

// Get 按名称返回适配器；不存在时返回 (nil, false)。
func Get(name string) (Adapter, bool) {
	registry.RLock()
	defer registry.RUnlock()

	a, ok := registry.adapters[name]
	return a, ok
}

// MustGet 按名称返回适配器；不存在时返回错误。
func MustGet(name string) (Adapter, error) {
	if a, ok := Get(name); ok {
		return a, nil
	}
	return nil, fmt.Errorf("adapter: 未找到适配器 %q", name)
}

// Names 返回已注册的所有适配器名称（有序，便于日志与测试）。
func Names() []string {
	registry.RLock()
	defer registry.RUnlock()

	names := make([]string, 0, len(registry.adapters))
	for name := range registry.adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
