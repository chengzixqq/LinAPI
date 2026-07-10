// Package admin 提供管理面的数据模型与数据访问抽象。
//
// 设计意图：管理面（用户/密钥/渠道的增删改查）是低频写操作，与热路径的
// store.Store（鉴权/余额，读多写频繁）刻意分离——AdminStore 单列一个接口，
// 内存与 PostgreSQL 各实现一套，由 database.enabled 决定装配哪套。
//
// 领域模型（User / APIKey / Channel）用纯 Go 类型，不暴露 pgtype 等持久层细节，
// 使 handler 与具体存储解耦；PG 实现负责 db 行 <-> 领域模型的转换。
package admin

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound 表示目标资源不存在。
var ErrNotFound = errors.New("admin: 资源不存在")

// ErrConflict 表示唯一约束冲突（如重复的 external_id / key_id / channel_id）。
var ErrConflict = errors.New("admin: 资源已存在")

// User 是管理面的用户视图。
type User struct {
	ExternalID string    `json:"external_id"`
	Balance    int64     `json:"balance"` // 最小计费单位
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// APIKey 是管理面的密钥视图（刻意不含明文与摘要）。
type APIKey struct {
	KeyID           string    `json:"key_id"`
	UserID          string    `json:"user_id"`
	RateLimitPerMin int       `json:"rate_limit_per_min"`
	AllowedModels   []string  `json:"allowed_models"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
}

// Channel 是管理面的渠道视图。api_key 出于安全默认不回显（见 handler）。
type Channel struct {
	ChannelID string            `json:"channel_id"`
	Name      string            `json:"name"`
	Format    string            `json:"format"`
	BaseURL   string            `json:"base_url"`
	APIKey    string            `json:"api_key,omitempty"`
	Models    map[string]string `json:"models"`
	Priority  int               `json:"priority"`
	Weight    int               `json:"weight"`
	Enabled   bool              `json:"enabled"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// CreateUserInput 是新建用户的入参。
type CreateUserInput struct {
	ExternalID string
	Balance    int64
	Enabled    bool
}

// CreateAPIKeyInput 是新建密钥的入参。明文 Key 由 handler 生成并传入，
// 存储实现只落库其 SHA-256 摘要，绝不持久化明文。
type CreateAPIKeyInput struct {
	APIKey          string // 明文，仅用于计算摘要落库，不持久化
	KeyID           string
	UserID          string
	RateLimitPerMin int
	AllowedModels   []string
	Enabled         bool
}

// ChannelInput 是新建/更新渠道的入参（可变字段全集）。
type ChannelInput struct {
	ChannelID string
	Name      string
	Format    string
	BaseURL   string
	APIKey    string
	Models    map[string]string
	Priority  int
	Weight    int
	Enabled   bool
}

// AdminStore 是管理面的数据访问接口。实现须并发安全。
//
// 约定：唯一约束冲突映射为 ErrConflict；目标不存在映射为 ErrNotFound。
type AdminStore interface {
	// ---- 用户 ----
	CreateUser(ctx context.Context, in CreateUserInput) (User, error)
	ListUsers(ctx context.Context, limit, offset int) ([]User, error)
	GetUser(ctx context.Context, externalID string) (User, error)
	SetUserEnabled(ctx context.Context, externalID string, enabled bool) (User, error)
	// AddBalance 充值/扣减，返回新余额。delta 为负表示扣减。
	AddBalance(ctx context.Context, externalID string, delta int64) (int64, error)

	// ---- 密钥 ----
	CreateAPIKey(ctx context.Context, in CreateAPIKeyInput) (APIKey, error)
	ListAPIKeysByUser(ctx context.Context, userID string) ([]APIKey, error)
	SetAPIKeyEnabled(ctx context.Context, keyID string, enabled bool) (APIKey, error)
	// DeleteAPIKey 物理删除密钥；不存在返回 ErrNotFound。
	DeleteAPIKey(ctx context.Context, keyID string) error

	// ---- 渠道 ----
	CreateChannel(ctx context.Context, in ChannelInput) (Channel, error)
	ListChannels(ctx context.Context) ([]Channel, error)
	GetChannel(ctx context.Context, channelID string) (Channel, error)
	UpdateChannel(ctx context.Context, in ChannelInput) (Channel, error)
	SetChannelEnabled(ctx context.Context, channelID string, enabled bool) (Channel, error)
	DeleteChannel(ctx context.Context, channelID string) error
}

// 编译期断言：两套实现都满足 AdminStore。
var (
	_ AdminStore = (*PGStore)(nil)
	_ AdminStore = (*MemoryStore)(nil)
)
