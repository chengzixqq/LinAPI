package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

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
