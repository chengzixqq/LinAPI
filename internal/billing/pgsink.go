package billing

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"linapi/internal/db"
)

// PGSink 是 Sink 的 PostgreSQL 实现：把用量日志批量写入 usage_logs 表。
// 替换 NopSink 后，Recorder 的后台批量落库真正持久化账单。
//
// 幂等：底层 SQL 用 ON CONFLICT (request_id) DO NOTHING，进程崩溃重放不会重复记账。
type PGSink struct {
	q db.Querier
}

// NewPGSink 用一个 sqlc 查询器构造 PGSink。
func NewPGSink(q db.Querier) *PGSink {
	return &PGSink{q: q}
}

// Write 实现 Sink：逐条幂等写入。
//
// 说明：Recorder 已在上层做了「攒批」，这里对批内每条执行幂等 INSERT。
// sqlc 的 :exec 查询本身是单条；若底层是 *pgxpool.Pool，pgx 会在池连接上顺序执行。
// 追求极致吞吐可改用 pgx.Batch 或 COPY，但会牺牲逐条 ON CONFLICT 的幂等语义，
// 故此处保持简单可靠——用量日志容忍毫秒级延迟，一致性优先。
func (s *PGSink) Write(ctx context.Context, records []UsageRecord) error {
	for _, r := range records {
		err := s.q.InsertUsageLog(ctx, db.InsertUsageLogParams{
			RequestID:    r.RequestID,
			UserID:       r.UserID,
			KeyID:        r.KeyID,
			Model:        r.Model,
			Channel:      r.Channel,
			InputTokens:  int32(r.InputTokens),
			OutputTokens: int32(r.OutputTokens),
			Cost:         r.Cost,
			CreatedAt:    pgtype.Timestamptz{Time: r.CreatedAt, Valid: true},
		})
		if err != nil {
			// 返回错误，交由 Recorder 记日志（用量日志失败不阻断主流程）。
			return err
		}
	}
	return nil
}
