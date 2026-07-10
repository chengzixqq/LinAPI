package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

const resolveAPIKey = `-- name: ResolveAPIKey :one
SELECT
    k.key_id,
    k.user_external_id,
    k.rate_limit_per_min,
    k.allowed_models,
    k.enabled AS key_enabled,
    u.enabled AS user_enabled
FROM api_keys k
JOIN users u ON u.external_id = k.user_external_id
WHERE k.key_hash = $1 AND k.enabled = TRUE AND u.enabled = TRUE
`

// ResolveAPIKeyRow 是 ResolveAPIKey 的返回行（联表投影，非单表模型）。
type ResolveAPIKeyRow struct {
	KeyID           string   `json:"key_id"`
	UserExternalID  string   `json:"user_external_id"`
	RateLimitPerMin int32    `json:"rate_limit_per_min"`
	AllowedModels   []string `json:"allowed_models"`
	KeyEnabled      bool     `json:"key_enabled"`
	UserEnabled     bool     `json:"user_enabled"`
}

// ResolveAPIKey 按密钥摘要解析调用方身份。
// 仅返回密钥与用户都启用的记录；任一禁用或不存在则返回 pgx.ErrNoRows。
func (q *Queries) ResolveAPIKey(ctx context.Context, keyHash string) (ResolveAPIKeyRow, error) {
	row := q.db.QueryRow(ctx, resolveAPIKey, keyHash)
	var i ResolveAPIKeyRow
	err := row.Scan(
		&i.KeyID,
		&i.UserExternalID,
		&i.RateLimitPerMin,
		&i.AllowedModels,
		&i.KeyEnabled,
		&i.UserEnabled,
	)
	return i, err
}

const createAPIKey = `-- name: CreateAPIKey :one
INSERT INTO api_keys (
    key_hash, key_id, user_external_id, rate_limit_per_min, allowed_models, enabled
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, key_hash, key_id, user_external_id, rate_limit_per_min, allowed_models, enabled, created_at
`

// CreateAPIKeyParams 是 CreateAPIKey 的入参。
type CreateAPIKeyParams struct {
	KeyHash         string   `json:"key_hash"`
	KeyID           string   `json:"key_id"`
	UserExternalID  string   `json:"user_external_id"`
	RateLimitPerMin int32    `json:"rate_limit_per_min"`
	AllowedModels   []string `json:"allowed_models"`
	Enabled         bool     `json:"enabled"`
}

// CreateAPIKey 新建 API 密钥。
func (q *Queries) CreateAPIKey(ctx context.Context, arg CreateAPIKeyParams) (ApiKey, error) {
	row := q.db.QueryRow(ctx, createAPIKey,
		arg.KeyHash,
		arg.KeyID,
		arg.UserExternalID,
		arg.RateLimitPerMin,
		arg.AllowedModels,
		arg.Enabled,
	)
	var i ApiKey
	err := row.Scan(
		&i.ID,
		&i.KeyHash,
		&i.KeyID,
		&i.UserExternalID,
		&i.RateLimitPerMin,
		&i.AllowedModels,
		&i.Enabled,
		&i.CreatedAt,
	)
	return i, err
}

const listAPIKeysByUser = `-- name: ListAPIKeysByUser :many
SELECT id, key_id, user_external_id, rate_limit_per_min, allowed_models, enabled, created_at
FROM api_keys
WHERE user_external_id = $1
ORDER BY created_at DESC, id DESC
`

// ListAPIKeysByUserRow 是 ListAPIKeysByUser 的返回行（刻意不含 key_hash，摘要不外泄）。
type ListAPIKeysByUserRow struct {
	ID              int64              `json:"id"`
	KeyID           string             `json:"key_id"`
	UserExternalID  string             `json:"user_external_id"`
	RateLimitPerMin int32              `json:"rate_limit_per_min"`
	AllowedModels   []string           `json:"allowed_models"`
	Enabled         bool               `json:"enabled"`
	CreatedAt       pgtype.Timestamptz `json:"created_at"`
}

// ListAPIKeysByUser 列出某用户的全部密钥，供管理面展示。
func (q *Queries) ListAPIKeysByUser(ctx context.Context, userExternalID string) ([]ListAPIKeysByUserRow, error) {
	rows, err := q.db.Query(ctx, listAPIKeysByUser, userExternalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []ListAPIKeysByUserRow{}
	for rows.Next() {
		var i ListAPIKeysByUserRow
		if err := rows.Scan(
			&i.ID,
			&i.KeyID,
			&i.UserExternalID,
			&i.RateLimitPerMin,
			&i.AllowedModels,
			&i.Enabled,
			&i.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const setAPIKeyEnabled = `-- name: SetAPIKeyEnabled :one
UPDATE api_keys
SET enabled = $2
WHERE key_id = $1
RETURNING id, key_id, user_external_id, rate_limit_per_min, allowed_models, enabled, created_at
`

// SetAPIKeyEnabledParams 是 SetAPIKeyEnabled 的入参。
type SetAPIKeyEnabledParams struct {
	KeyID   string `json:"key_id"`
	Enabled bool   `json:"enabled"`
}

// SetAPIKeyEnabled 启用/禁用密钥（软删除），返回更新后的行（不含 key_hash）。
func (q *Queries) SetAPIKeyEnabled(ctx context.Context, arg SetAPIKeyEnabledParams) (ListAPIKeysByUserRow, error) {
	row := q.db.QueryRow(ctx, setAPIKeyEnabled, arg.KeyID, arg.Enabled)
	var i ListAPIKeysByUserRow
	err := row.Scan(
		&i.ID,
		&i.KeyID,
		&i.UserExternalID,
		&i.RateLimitPerMin,
		&i.AllowedModels,
		&i.Enabled,
		&i.CreatedAt,
	)
	return i, err
}

const deleteAPIKey = `-- name: DeleteAPIKey :execrows
DELETE FROM api_keys WHERE key_id = $1
`

// DeleteAPIKey 按 key_id 物理删除密钥，返回受影响行数（0 表示不存在）。
func (q *Queries) DeleteAPIKey(ctx context.Context, keyID string) (int64, error) {
	ct, err := q.db.Exec(ctx, deleteAPIKey, keyID)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}
