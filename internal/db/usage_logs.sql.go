package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

const insertUsageLog = `-- name: InsertUsageLog :exec
INSERT INTO usage_logs (
    request_id, user_id, key_id, model, channel, input_tokens, output_tokens, cost, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (request_id) DO NOTHING
`

// InsertUsageLogParams 是 InsertUsageLog 的入参。
type InsertUsageLogParams struct {
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

// InsertUsageLog 落库一条用量日志。ON CONFLICT (request_id) 保证按请求幂等。
func (q *Queries) InsertUsageLog(ctx context.Context, arg InsertUsageLogParams) error {
	_, err := q.db.Exec(ctx, insertUsageLog,
		arg.RequestID,
		arg.UserID,
		arg.KeyID,
		arg.Model,
		arg.Channel,
		arg.InputTokens,
		arg.OutputTokens,
		arg.Cost,
		arg.CreatedAt,
	)
	return err
}

const sumCostByUser = `-- name: SumCostByUser :one
SELECT COALESCE(SUM(cost), 0)::BIGINT AS total_cost
FROM usage_logs
WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
`

// SumCostByUserParams 是 SumCostByUser 的入参（半开时间窗 [Start, End)）。
type SumCostByUserParams struct {
	UserID string             `json:"user_id"`
	Start  pgtype.Timestamptz `json:"start"`
	End    pgtype.Timestamptz `json:"end"`
}

// SumCostByUser 统计某用户在时间窗内的总扣费，供对账。
func (q *Queries) SumCostByUser(ctx context.Context, arg SumCostByUserParams) (int64, error) {
	row := q.db.QueryRow(ctx, sumCostByUser, arg.UserID, arg.Start, arg.End)
	var totalCost int64
	err := row.Scan(&totalCost)
	return totalCost, err
}
