package db

import (
	"context"
)

const listEnabledChannels = `-- name: ListEnabledChannels :many
SELECT
    channel_id, name, format, base_url, api_key, models, priority, weight, enabled
FROM channels
WHERE enabled = TRUE
ORDER BY priority DESC, channel_id
`

// ListEnabledChannelsRow 是 ListEnabledChannels 的返回行（不含内部主键/时间列）。
type ListEnabledChannelsRow struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
	Format    string `json:"format"`
	BaseURL   string `json:"base_url"`
	ApiKey    string `json:"api_key"`
	Models    []byte `json:"models"`
	Priority  int32  `json:"priority"`
	Weight    int32  `json:"weight"`
	Enabled   bool   `json:"enabled"`
}

// ListEnabledChannels 拉取全部启用渠道，供路由引擎加载与热更新。
func (q *Queries) ListEnabledChannels(ctx context.Context) ([]ListEnabledChannelsRow, error) {
	rows, err := q.db.Query(ctx, listEnabledChannels)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []ListEnabledChannelsRow{}
	for rows.Next() {
		var i ListEnabledChannelsRow
		if err := rows.Scan(
			&i.ChannelID,
			&i.Name,
			&i.Format,
			&i.BaseURL,
			&i.ApiKey,
			&i.Models,
			&i.Priority,
			&i.Weight,
			&i.Enabled,
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

const createChannel = `-- name: CreateChannel :one
INSERT INTO channels (
    channel_id, name, format, base_url, api_key, models, priority, weight, enabled
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, channel_id, name, format, base_url, api_key, models, priority, weight, enabled, created_at, updated_at
`

// CreateChannelParams 是 CreateChannel 的入参。
type CreateChannelParams struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
	Format    string `json:"format"`
	BaseURL   string `json:"base_url"`
	ApiKey    string `json:"api_key"`
	Models    []byte `json:"models"`
	Priority  int32  `json:"priority"`
	Weight    int32  `json:"weight"`
	Enabled   bool   `json:"enabled"`
}

// CreateChannel 新建渠道。
func (q *Queries) CreateChannel(ctx context.Context, arg CreateChannelParams) (Channel, error) {
	row := q.db.QueryRow(ctx, createChannel,
		arg.ChannelID,
		arg.Name,
		arg.Format,
		arg.BaseURL,
		arg.ApiKey,
		arg.Models,
		arg.Priority,
		arg.Weight,
		arg.Enabled,
	)
	var i Channel
	err := row.Scan(
		&i.ID,
		&i.ChannelID,
		&i.Name,
		&i.Format,
		&i.BaseURL,
		&i.ApiKey,
		&i.Models,
		&i.Priority,
		&i.Weight,
		&i.Enabled,
		&i.CreatedAt,
		&i.UpdatedAt,
	)
	return i, err
}

const listAllChannels = `-- name: ListAllChannels :many
SELECT id, channel_id, name, format, base_url, api_key, models, priority, weight, enabled, created_at, updated_at
FROM channels
ORDER BY priority DESC, channel_id
`

// ListAllChannels 列出全部渠道（含禁用），供管理面展示。
func (q *Queries) ListAllChannels(ctx context.Context) ([]Channel, error) {
	rows, err := q.db.Query(ctx, listAllChannels)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []Channel{}
	for rows.Next() {
		var i Channel
		if err := rows.Scan(
			&i.ID,
			&i.ChannelID,
			&i.Name,
			&i.Format,
			&i.BaseURL,
			&i.ApiKey,
			&i.Models,
			&i.Priority,
			&i.Weight,
			&i.Enabled,
			&i.CreatedAt,
			&i.UpdatedAt,
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

const getChannel = `-- name: GetChannel :one
SELECT id, channel_id, name, format, base_url, api_key, models, priority, weight, enabled, created_at, updated_at
FROM channels
WHERE channel_id = $1
`

// GetChannel 按 channel_id 取单个渠道。
func (q *Queries) GetChannel(ctx context.Context, channelID string) (Channel, error) {
	row := q.db.QueryRow(ctx, getChannel, channelID)
	var i Channel
	err := row.Scan(
		&i.ID,
		&i.ChannelID,
		&i.Name,
		&i.Format,
		&i.BaseURL,
		&i.ApiKey,
		&i.Models,
		&i.Priority,
		&i.Weight,
		&i.Enabled,
		&i.CreatedAt,
		&i.UpdatedAt,
	)
	return i, err
}

const updateChannel = `-- name: UpdateChannel :one
UPDATE channels
SET name = $2,
    format = $3,
    base_url = $4,
    api_key = $5,
    models = $6,
    priority = $7,
    weight = $8,
    enabled = $9,
    updated_at = now()
WHERE channel_id = $1
RETURNING id, channel_id, name, format, base_url, api_key, models, priority, weight, enabled, created_at, updated_at
`

// UpdateChannelParams 是 UpdateChannel 的入参（全量更新可变字段）。
type UpdateChannelParams struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
	Format    string `json:"format"`
	BaseURL   string `json:"base_url"`
	ApiKey    string `json:"api_key"`
	Models    []byte `json:"models"`
	Priority  int32  `json:"priority"`
	Weight    int32  `json:"weight"`
	Enabled   bool   `json:"enabled"`
}

// UpdateChannel 全量更新渠道，返回更新后的行。
func (q *Queries) UpdateChannel(ctx context.Context, arg UpdateChannelParams) (Channel, error) {
	row := q.db.QueryRow(ctx, updateChannel,
		arg.ChannelID,
		arg.Name,
		arg.Format,
		arg.BaseURL,
		arg.ApiKey,
		arg.Models,
		arg.Priority,
		arg.Weight,
		arg.Enabled,
	)
	var i Channel
	err := row.Scan(
		&i.ID,
		&i.ChannelID,
		&i.Name,
		&i.Format,
		&i.BaseURL,
		&i.ApiKey,
		&i.Models,
		&i.Priority,
		&i.Weight,
		&i.Enabled,
		&i.CreatedAt,
		&i.UpdatedAt,
	)
	return i, err
}

const setChannelEnabled = `-- name: SetChannelEnabled :one
UPDATE channels
SET enabled = $2,
    updated_at = now()
WHERE channel_id = $1
RETURNING id, channel_id, name, format, base_url, api_key, models, priority, weight, enabled, created_at, updated_at
`

// SetChannelEnabledParams 是 SetChannelEnabled 的入参。
type SetChannelEnabledParams struct {
	ChannelID string `json:"channel_id"`
	Enabled   bool   `json:"enabled"`
}

// SetChannelEnabled 启用/禁用渠道，返回更新后的行。
func (q *Queries) SetChannelEnabled(ctx context.Context, arg SetChannelEnabledParams) (Channel, error) {
	row := q.db.QueryRow(ctx, setChannelEnabled, arg.ChannelID, arg.Enabled)
	var i Channel
	err := row.Scan(
		&i.ID,
		&i.ChannelID,
		&i.Name,
		&i.Format,
		&i.BaseURL,
		&i.ApiKey,
		&i.Models,
		&i.Priority,
		&i.Weight,
		&i.Enabled,
		&i.CreatedAt,
		&i.UpdatedAt,
	)
	return i, err
}

const deleteChannel = `-- name: DeleteChannel :execrows
DELETE FROM channels
WHERE channel_id = $1
`

// DeleteChannel 物理删除渠道，返回受影响行数（0 表示不存在）。
func (q *Queries) DeleteChannel(ctx context.Context, channelID string) (int64, error) {
	res, err := q.db.Exec(ctx, deleteChannel, channelID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}
