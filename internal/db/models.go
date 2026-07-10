package db

import (
	"github.com/jackc/pgx/v5/pgtype"
)

// User 对应 users 表。balance 按 sqlc.yaml 的 override 映射为 int64（最小计费单位）。
type User struct {
	ID         int64  `json:"id"`
	ExternalID string `json:"external_id"`
	Balance    int64  `json:"balance"`
	// RateMultiplier 是预留的单用户定价倍率（百分比，100=1.00x）；本期存而不用，
	// 现有查询不 select 该列，故此字段暂不参与任何 Scan。
	RateMultiplier int32              `json:"rate_multiplier"`
	Enabled        bool               `json:"enabled"`
	CreatedAt      pgtype.Timestamptz `json:"created_at"`
	UpdatedAt      pgtype.Timestamptz `json:"updated_at"`
}

// ApiKey 对应 api_keys 表。key_hash 存 SHA-256 摘要，绝不落明文。
type ApiKey struct {
	ID              int64              `json:"id"`
	KeyHash         string             `json:"key_hash"`
	KeyID           string             `json:"key_id"`
	UserExternalID  string             `json:"user_external_id"`
	RateLimitPerMin int32              `json:"rate_limit_per_min"`
	AllowedModels   []string           `json:"allowed_models"`
	Enabled         bool               `json:"enabled"`
	CreatedAt       pgtype.Timestamptz `json:"created_at"`
}

// Channel 对应 channels 表。Models 是「对外模型名 -> 上游实际模型名」映射，
// 在 DB 中以 JSONB 存储，这里用 []byte 承载原始 JSON，由查询层解组。
type Channel struct {
	ID        int64              `json:"id"`
	ChannelID string             `json:"channel_id"`
	Name      string             `json:"name"`
	Format    string             `json:"format"`
	BaseURL   string             `json:"base_url"`
	ApiKey    string             `json:"api_key"`
	Models    []byte             `json:"models"`
	Priority  int32              `json:"priority"`
	Weight    int32              `json:"weight"`
	Enabled   bool               `json:"enabled"`
	CreatedAt pgtype.Timestamptz `json:"created_at"`
	UpdatedAt pgtype.Timestamptz `json:"updated_at"`
}

// UsageLog 对应 usage_logs 表。cost 按 override 映射为 int64（最小计费单位）。
type UsageLog struct {
	ID           int64              `json:"id"`
	RequestID    string             `json:"request_id"`
	UserID       string             `json:"user_id"`
	KeyID        string             `json:"key_id"`
	Model        string             `json:"model"`
	Channel      string             `json:"channel"`
	InputTokens  int32              `json:"input_tokens"`
	OutputTokens int32              `json:"output_tokens"`
	Cost         int64              `json:"cost"`
	CreatedAt    pgtype.Timestamptz `json:"created_at"`
}

// Account 对应 accounts 表：控制台登录账户（与计费实体 users 分离）。
// PasswordHash 存 bcrypt 哈希，绝不落明文。ExternalID 对 admin 角色可空。
type Account struct {
	ID           int64              `json:"id"`
	Username     string             `json:"username"`
	PasswordHash string             `json:"password_hash"`
	Role         string             `json:"role"`
	ExternalID   pgtype.Text        `json:"external_id"`
	GroupName    string             `json:"group_name"`
	Enabled      bool               `json:"enabled"`
	CreatedAt    pgtype.Timestamptz `json:"created_at"`
	UpdatedAt    pgtype.Timestamptz `json:"updated_at"`
}

// Setting 对应 settings 表：运行时可变的 KV 系统设置。
type Setting struct {
	Key       string             `json:"key"`
	Value     string             `json:"value"`
	UpdatedAt pgtype.Timestamptz `json:"updated_at"`
}
