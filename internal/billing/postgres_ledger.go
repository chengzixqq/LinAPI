package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"linapi/internal/db"
)

const (
	ledgerKindReserve  = "reserve"
	ledgerKindSettle   = "settle"
	ledgerKindRefund   = "refund"
	staleReservedAfter = 5 * time.Minute
	staleInFlightAfter = 24 * time.Hour
)

type pgLedgerPool interface {
	Begin(context.Context) (pgx.Tx, error)
}

// PostgresLedger 是生产账本实现。每个资金操作都在 PostgreSQL 事务内同时更新
// users.balance、reservation 状态、只追加流水及最终 usage，Redis 不参与资金正确性。
type PostgresLedger struct {
	pool pgLedgerPool
}

// NewPostgresLedger 用连接池构建生产账本。
func NewPostgresLedger(pool *pgxpool.Pool) *PostgresLedger {
	return &PostgresLedger{pool: pool}
}

var _ Ledger = (*PostgresLedger)(nil)

// Reserve 幂等创建 reservation，并用条件 UPDATE 原子阻止同一用户并发超卖。
func (l *PostgresLedger) Reserve(ctx context.Context, r Reservation) (bool, error) {
	maxInput, maxOutput, err := reservationTokenLimits(r)
	if err != nil {
		return false, err
	}
	if r.ID == "" || r.UserID == "" || r.Amount <= 0 {
		return false, fmt.Errorf("%w: reservation 必填字段或金额非法", ErrReservationConflict)
	}

	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	createdAt := normalizedTime(r.CreatedAt)
	_, err = q.InsertBillingReservation(ctx, db.InsertBillingReservationParams{
		ReservationID:   r.ID,
		TraceID:         r.TraceID,
		UserID:          r.UserID,
		KeyID:           r.KeyID,
		Model:           r.Model,
		Amount:          r.Amount,
		MaxInputTokens:  maxInput,
		MaxOutputTokens: maxOutput,
		CreatedAt:       timestamp(createdAt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		old, getErr := q.GetBillingReservationForUpdate(ctx, r.ID)
		if getErr != nil {
			return false, getErr
		}
		if !samePostgresReservation(old, r) {
			return false, ErrReservationConflict
		}
		if ReservationStatus(old.Status) != ReservationReserved {
			return false, ErrInvalidTransition
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}

	balance, err := q.DebitBalanceForReservation(ctx, db.DebitBalanceForReservationParams{
		ExternalID: r.UserID,
		Amount:     r.Amount,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 回滚同时删除刚插入的 reservation，余额不足/用户禁用均不留下半成品。
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := q.InsertBillingLedgerEntry(ctx, db.InsertBillingLedgerEntryParams{
		OperationID:    operationID(r.ID, ledgerKindReserve),
		ReservationID:  r.ID,
		UserID:         r.UserID,
		Kind:           ledgerKindReserve,
		Amount:         -r.Amount,
		BalanceAfter:   balance.Balance,
		BalanceVersion: balance.BalanceVersion,
		CreatedAt:      timestamp(createdAt),
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (l *PostgresLedger) MarkInFlight(ctx context.Context, reservationID, channel string) error {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	r, err := q.GetBillingReservationForUpdate(ctx, reservationID)
	if err != nil {
		return err
	}
	if ReservationStatus(r.Status) == ReservationInFlight {
		if r.Channel != channel {
			return ErrReservationConflict
		}
		return tx.Commit(ctx)
	}
	if ReservationStatus(r.Status) != ReservationReserved {
		return ErrInvalidTransition
	}
	n, err := q.MarkBillingReservationInFlight(ctx, db.MarkBillingReservationInFlightParams{
		ReservationID: reservationID, Channel: channel, UpdatedAt: timestamp(time.Now().UTC()),
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalidTransition
	}
	return tx.Commit(ctx)
}

// ReleaseAttempt 仅用于上游明确拒绝、可证明未产生模型消费的响应。网络错误、
// 超时和 5xx 不得调用，否则会重新打开跨渠道重放/错误退款窗口。
func (l *PostgresLedger) ReleaseAttempt(ctx context.Context, reservationID string) error {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	r, err := q.GetBillingReservationForUpdate(ctx, reservationID)
	if err != nil {
		return err
	}
	if ReservationStatus(r.Status) == ReservationReserved {
		return tx.Commit(ctx)
	}
	if ReservationStatus(r.Status) != ReservationInFlight {
		return ErrInvalidTransition
	}
	n, err := q.ReleaseBillingAttempt(ctx, db.ReleaseBillingAttemptParams{
		ReservationID: reservationID, UpdatedAt: timestamp(time.Now().UTC()),
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalidTransition
	}
	return tx.Commit(ctx)
}

// RecordConsumption 持久化上游已消费的事实。相同事实可重放；参数变化视为幂等冲突。
func (l *PostgresLedger) RecordConsumption(ctx context.Context, c Consumption) error {
	params, err := consumptionParams(c)
	if err != nil {
		return err
	}

	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	r, err := q.GetBillingReservationForUpdate(ctx, c.ReservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: reservation 不存在", ErrInvalidTransition)
	}
	if err != nil {
		return err
	}

	switch ReservationStatus(r.Status) {
	case ReservationInFlight:
		if r.Channel != c.Channel {
			return ErrReservationConflict
		}
		n, err := q.RecordBillingConsumption(ctx, params)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrInvalidTransition
		}
	case ReservationConsumedUnsettled, ReservationSettled:
		if !samePostgresConsumption(r, c) {
			return ErrReservationConflict
		}
	case ReservationRefunded:
		return ErrInvalidTransition
	default:
		return fmt.Errorf("%w: 未知状态 %q", ErrInvalidTransition, r.Status)
	}

	return tx.Commit(ctx)
}

// Finalize 把已记录的 consumption 结算为终态；余额退差、流水、usage 和状态
// 在同一事务提交。事务提交结果不确定时，可用同一 reservation ID 安全重放。
func (l *PostgresLedger) Finalize(ctx context.Context, reservationID string) error {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	r, err := q.GetBillingReservationForUpdate(ctx, reservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: reservation 不存在", ErrInvalidTransition)
	}
	if err != nil {
		return err
	}
	if ReservationStatus(r.Status) == ReservationSettled {
		return tx.Commit(ctx)
	}
	if ReservationStatus(r.Status) != ReservationConsumedUnsettled {
		return ErrInvalidTransition
	}
	if r.Cost < 0 {
		return ErrReservationConflict
	}
	if r.Cost > r.Amount {
		return ErrReservationExceeded
	}

	delta := r.Amount - r.Cost
	balance, err := q.AdjustBalanceForBilling(ctx, db.AdjustBalanceForBillingParams{
		ExternalID: r.UserID,
		Delta:      delta,
	})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := q.InsertBillingLedgerEntry(ctx, db.InsertBillingLedgerEntryParams{
		OperationID:    operationID(r.ReservationID, ledgerKindSettle),
		ReservationID:  r.ReservationID,
		UserID:         r.UserID,
		Kind:           ledgerKindSettle,
		Amount:         delta,
		BalanceAfter:   balance.Balance,
		BalanceVersion: balance.BalanceVersion,
		CreatedAt:      timestamp(now),
	}); err != nil {
		return err
	}
	usageAt := now
	if r.ConsumedAt.Valid {
		usageAt = r.ConsumedAt.Time
	}
	if err := q.InsertFinalizedUsageLog(ctx, db.InsertFinalizedUsageLogParams{
		RequestID:                r.ReservationID,
		UserID:                   r.UserID,
		KeyID:                    r.KeyID,
		Model:                    r.Model,
		Channel:                  r.Channel,
		InputTokens:              r.InputTokens,
		OutputTokens:             r.OutputTokens,
		CacheCreationInputTokens: r.CacheCreationInputTokens,
		CacheReadInputTokens:     r.CacheReadInputTokens,
		ReportedTotalTokens:      r.ReportedTotalTokens,
		Cost:                     r.Cost,
		UsageComplete:            r.UsageComplete,
		Estimated:                r.Estimated,
		CreatedAt:                timestamp(usageAt),
	}); err != nil {
		return err
	}
	n, err := q.MarkBillingReservationSettled(ctx, db.MarkBillingReservationSettledParams{
		ReservationID: r.ReservationID,
		SettledAt:     timestamp(now),
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalidTransition
	}
	return tx.Commit(ctx)
}

// Refund 仅允许 reserved 退款；已记录消费的 reservation 无论后续错误如何都不能退款。
func (l *PostgresLedger) Refund(ctx context.Context, reservationID string) error {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	r, err := q.GetBillingReservationForUpdate(ctx, reservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: reservation 不存在", ErrInvalidTransition)
	}
	if err != nil {
		return err
	}
	if ReservationStatus(r.Status) == ReservationRefunded {
		return tx.Commit(ctx)
	}
	if ReservationStatus(r.Status) != ReservationReserved {
		return ErrInvalidTransition
	}

	balance, err := q.AdjustBalanceForBilling(ctx, db.AdjustBalanceForBillingParams{
		ExternalID: r.UserID,
		Delta:      r.Amount,
	})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := q.InsertBillingLedgerEntry(ctx, db.InsertBillingLedgerEntryParams{
		OperationID:    operationID(r.ReservationID, ledgerKindRefund),
		ReservationID:  r.ReservationID,
		UserID:         r.UserID,
		Kind:           ledgerKindRefund,
		Amount:         r.Amount,
		BalanceAfter:   balance.Balance,
		BalanceVersion: balance.BalanceVersion,
		CreatedAt:      timestamp(now),
	}); err != nil {
		return err
	}
	n, err := q.MarkBillingReservationRefunded(ctx, db.MarkBillingReservationRefundedParams{
		ReservationID: r.ReservationID,
		RefundedAt:    timestamp(now),
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalidTransition
	}
	return tx.Commit(ctx)
}

// Recover 启动时重试所有已持久化 consumption；Finalize 自身幂等，可与在线结算并发。
func (l *PostgresLedger) Recover(ctx context.Context) error {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return err
	}
	q := db.New(tx)
	ids, err := q.ListConsumedUnsettledReservations(ctx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	now := time.Now().UTC()
	staleReserved, err := q.ListStaleReservedReservations(ctx, timestamp(now.Add(-staleReservedAfter)))
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	staleInFlight, err := q.ListStaleInFlightReservations(ctx, timestamp(now.Add(-staleInFlightAfter)))
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	var joined error
	for _, id := range ids {
		if err := l.Finalize(ctx, id); err != nil {
			joined = errors.Join(joined, fmt.Errorf("恢复 reservation %s: %w", id, err))
		}
	}
	for _, id := range staleReserved {
		if err := l.Refund(ctx, id); err != nil {
			joined = errors.Join(joined, fmt.Errorf("释放过期 reservation %s: %w", id, err))
		}
	}
	if joined != nil {
		return joined
	}
	if len(staleInFlight) > 0 {
		shown := staleInFlight
		if len(shown) > 20 {
			shown = shown[:20]
		}
		return fmt.Errorf("%w: count=%d reservation_ids=%v", ErrAmbiguousReservations, len(staleInFlight), shown)
	}
	return nil
}

func reservationTokenLimits(r Reservation) (int32, int32, error) {
	if r.MaxInputTokens < 0 || r.MaxInputTokens > math.MaxInt32 ||
		r.MaxOutputTokens <= 0 || r.MaxOutputTokens > math.MaxInt32 {
		return 0, 0, fmt.Errorf("%w: token 上限超出 PostgreSQL INT 范围", ErrReservationConflict)
	}
	return int32(r.MaxInputTokens), int32(r.MaxOutputTokens), nil
}

func consumptionParams(c Consumption) (db.RecordBillingConsumptionParams, error) {
	if c.ReservationID == "" || c.Cost < 0 {
		return db.RecordBillingConsumptionParams{}, fmt.Errorf("%w: consumption 必填字段或成本非法", ErrReservationConflict)
	}
	values := []int{c.InputTokens, c.OutputTokens, c.CacheCreationInputTokens, c.CacheReadInputTokens, c.ReportedTotalTokens}
	for _, value := range values {
		if value < math.MinInt32 || value > math.MaxInt32 {
			return db.RecordBillingConsumptionParams{}, fmt.Errorf("%w: token 用量超出 PostgreSQL INT 范围", ErrReservationConflict)
		}
	}
	return db.RecordBillingConsumptionParams{
		ReservationID:            c.ReservationID,
		Channel:                  c.Channel,
		InputTokens:              int32(c.InputTokens),
		OutputTokens:             int32(c.OutputTokens),
		CacheCreationInputTokens: int32(c.CacheCreationInputTokens),
		CacheReadInputTokens:     int32(c.CacheReadInputTokens),
		ReportedTotalTokens:      int32(c.ReportedTotalTokens),
		Cost:                     c.Cost,
		UsageComplete:            c.UsageComplete,
		Estimated:                c.Estimated,
		RecordedAt:               timestamp(normalizedTime(c.RecordedAt)),
	}, nil
}

func samePostgresReservation(old db.BillingReservation, r Reservation) bool {
	return old.ReservationID == r.ID && old.TraceID == r.TraceID && old.UserID == r.UserID &&
		old.KeyID == r.KeyID && old.Model == r.Model && old.Amount == r.Amount &&
		int(old.MaxInputTokens) == r.MaxInputTokens && int(old.MaxOutputTokens) == r.MaxOutputTokens
}

func samePostgresConsumption(old db.BillingReservation, c Consumption) bool {
	return old.ReservationID == c.ReservationID && old.Channel == c.Channel &&
		int(old.InputTokens) == c.InputTokens && int(old.OutputTokens) == c.OutputTokens &&
		int(old.CacheCreationInputTokens) == c.CacheCreationInputTokens &&
		int(old.CacheReadInputTokens) == c.CacheReadInputTokens &&
		int(old.ReportedTotalTokens) == c.ReportedTotalTokens && old.Cost == c.Cost &&
		old.UsageComplete == c.UsageComplete && old.Estimated == c.Estimated
}

func operationID(reservationID, kind string) string {
	return reservationID + ":" + kind
}

func normalizedTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}
