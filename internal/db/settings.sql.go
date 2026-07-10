package db

import "context"

const getSetting = `-- name: GetSetting :one
SELECT key, value, updated_at FROM settings WHERE key = $1
`

// GetSetting 取单个设置项。
func (q *Queries) GetSetting(ctx context.Context, key string) (Setting, error) {
	row := q.db.QueryRow(ctx, getSetting, key)
	var i Setting
	err := row.Scan(&i.Key, &i.Value, &i.UpdatedAt)
	return i, err
}

const upsertSetting = `-- name: UpsertSetting :exec
INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
`

// UpsertSettingParams 是 UpsertSetting 的入参。
type UpsertSettingParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// UpsertSetting 写入/更新设置项（幂等）。
func (q *Queries) UpsertSetting(ctx context.Context, arg UpsertSettingParams) error {
	_, err := q.db.Exec(ctx, upsertSetting, arg.Key, arg.Value)
	return err
}
