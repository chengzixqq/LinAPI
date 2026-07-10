package db

import "context"

const listChannelKeyMaterialsForUpdate = `-- name: ListChannelKeyMaterialsForUpdate :many
SELECT channel_id, api_key
FROM channels
ORDER BY channel_id
FOR UPDATE
`

type ListChannelKeyMaterialsForUpdateRow struct {
	ChannelID string `json:"channel_id"`
	ApiKey    string `json:"api_key"`
}

func (q *Queries) ListChannelKeyMaterialsForUpdate(ctx context.Context) ([]ListChannelKeyMaterialsForUpdateRow, error) {
	rows, err := q.db.Query(ctx, listChannelKeyMaterialsForUpdate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []ListChannelKeyMaterialsForUpdateRow{}
	for rows.Next() {
		var i ListChannelKeyMaterialsForUpdateRow
		if err := rows.Scan(&i.ChannelID, &i.ApiKey); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const updateChannelKeyMaterial = `-- name: UpdateChannelKeyMaterial :execrows
UPDATE channels
SET api_key = $2,
    updated_at = now()
WHERE channel_id = $1
  AND api_key = $3
`

type UpdateChannelKeyMaterialParams struct {
	ChannelID string `json:"channel_id"`
	ApiKey    string `json:"api_key"`
	OldApiKey string `json:"old_api_key"`
}

func (q *Queries) UpdateChannelKeyMaterial(ctx context.Context, arg UpdateChannelKeyMaterialParams) (int64, error) {
	res, err := q.db.Exec(ctx, updateChannelKeyMaterial, arg.ChannelID, arg.ApiKey, arg.OldApiKey)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}
