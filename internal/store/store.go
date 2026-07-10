// Package store 定义网关运行所需的数据访问接口及其实现。
//
// 设计意图：中间件（鉴权/额度）只依赖 Store 接口，不关心数据从哪来。
// 当前提供一个配置驱动的内存实现（MemoryStore），第 7 步接入 sqlc 后
// 用 PostgreSQL 实现同一接口替换即可，中间件层零改动——对应「架构干净可改」。
package store

import (
	"context"
	"errors"
)

// ErrKeyNotFound 表示 API Key 不存在或已禁用。
var ErrKeyNotFound = errors.New("store: API Key 不存在或已禁用")

// 管理操作的 sentinel error（供 MemoryStore 的 Admin* 方法使用，
// 由 internal/admin 内存实现映射为其领域错误）。
var (
	errUserExists   = errors.New("store: 用户已存在")
	errUserNotFound = errors.New("store: 用户不存在")
	errKeyExists    = errors.New("store: 密钥已存在")
)

// Identity 是一个 API Key 解析出的调用方身份。
type Identity struct {
	// KeyID 是密钥的稳定标识（用于限流维度、日志、计费归因）。
	KeyID string

	// UserID 是密钥所属用户。
	UserID string

	// RateLimitPerMin 是该密钥每分钟允许的请求数（<=0 表示不限流）。
	RateLimitPerMin int

	// AllowedModels 限定该密钥可访问的对外模型名；为空表示不限制。
	AllowedModels []string

	// Enabled 为 false 时密钥不可用。
	Enabled bool
}

// Allows 返回该身份是否允许访问指定对外模型名。
// AllowedModels 为空表示不做模型级限制。
func (id *Identity) Allows(model string) bool {
	if len(id.AllowedModels) == 0 {
		return true
	}
	for _, m := range id.AllowedModels {
		if m == model {
			return true
		}
	}
	return false
}

// Store 是网关的数据访问接口。
//
// 约定：实现必须并发安全（多请求 goroutine 并发调用）。
// 额度相关方法此处只做「读」，真正的原子扣费/退差在计费模块（第 6 步）
// 用 Redis 完成，Store 负责与持久层对账。
type Store interface {
	// ResolveKey 按明文 API Key 解析调用方身份。
	// 不存在或已禁用返回 ErrKeyNotFound。
	ResolveKey(ctx context.Context, apiKey string) (*Identity, error)

	// Balance 返回某用户的当前额度余额（最小计费单位，如厘 / microcents）。
	Balance(ctx context.Context, userID string) (int64, error)
}
