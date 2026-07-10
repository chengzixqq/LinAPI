package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

const insertBillingReservation = `-- name: InsertBillingReservation :one
INSERT INTO billing_reservations (
    reservation_id, trace_id, user_id, key_id, model, amount,
    max_input_tokens, max_output_tokens, status, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'reserved', $9, $9)
ON CONFLICT (reservation_id) DO NOTHING
RETURNING reservation_id, trace_id, user_id, key_id, model, amount,
          max_input_tokens, max_output_tokens, status, channel,
          input_tokens, output_tokens, cache_creation_input_tokens,
          cache_read_input_tokens, reported_total_tokens, cost,
          usage_complete, estimated, created_at, consumed_at,
          settled_at, refunded_at, updated_at
`

type InsertBillingReservationParams struct {
	ReservationID   string             `json:"reservation_id"`
	TraceID         string             `json:"trace_id"`
	UserID          string             `json:"user_id"`
	KeyID           string             `json:"key_id"`
	Model           string             `json:"model"`
	Amount          int64              `json:"amount"`
	MaxInputTokens  int32              `json:"max_input_tokens"`
	MaxOutputTokens int32              `json:"max_output_tokens"`
	CreatedAt       pgtype.Timestamptz `json:"created_at"`
}

func (q *Queries) InsertBillingReservation(ctx context.Context, arg InsertBillingReservationParams) (BillingReservation, error) {
	row := q.db.QueryRow(ctx, insertBillingReservation,
		arg.ReservationID,
		arg.TraceID,
		arg.UserID,
		arg.KeyID,
		arg.Model,
		arg.Amount,
		arg.MaxInputTokens,
		arg.MaxOutputTokens,
		arg.CreatedAt,
	)
	return scanBillingReservation(row)
}

const getBillingReservation = `-- name: GetBillingReservation :one
SELECT reservation_id, trace_id, user_id, key_id, model, amount,
       max_input_tokens, max_output_tokens, status, channel,
       input_tokens, output_tokens, cache_creation_input_tokens,
       cache_read_input_tokens, reported_total_tokens, cost,
       usage_complete, estimated, created_at, consumed_at,
       settled_at, refunded_at, updated_at
FROM billing_reservations
WHERE reservation_id = $1
`

func (q *Queries) GetBillingReservation(ctx context.Context, reservationID string) (BillingReservation, error) {
	return scanBillingReservation(q.db.QueryRow(ctx, getBillingReservation, reservationID))
}

const getBillingReservationForUpdate = `-- name: GetBillingReservationForUpdate :one
SELECT reservation_id, trace_id, user_id, key_id, model, amount,
       max_input_tokens, max_output_tokens, status, channel,
       input_tokens, output_tokens, cache_creation_input_tokens,
       cache_read_input_tokens, reported_total_tokens, cost,
       usage_complete, estimated, created_at, consumed_at,
       settled_at, refunded_at, updated_at
FROM billing_reservations
WHERE reservation_id = $1
FOR UPDATE
`

func (q *Queries) GetBillingReservationForUpdate(ctx context.Context, reservationID string) (BillingReservation, error) {
	return scanBillingReservation(q.db.QueryRow(ctx, getBillingReservationForUpdate, reservationID))
}

type billingReservationScanner interface {
	Scan(dest ...any) error
}

func scanBillingReservation(row billingReservationScanner) (BillingReservation, error) {
	var r BillingReservation
	err := row.Scan(
		&r.ReservationID,
		&r.TraceID,
		&r.UserID,
		&r.KeyID,
		&r.Model,
		&r.Amount,
		&r.MaxInputTokens,
		&r.MaxOutputTokens,
		&r.Status,
		&r.Channel,
		&r.InputTokens,
		&r.OutputTokens,
		&r.CacheCreationInputTokens,
		&r.CacheReadInputTokens,
		&r.ReportedTotalTokens,
		&r.Cost,
		&r.UsageComplete,
		&r.Estimated,
		&r.CreatedAt,
		&r.ConsumedAt,
		&r.SettledAt,
		&r.RefundedAt,
		&r.UpdatedAt,
	)
	return r, err
}

const debitBalanceForReservation = `-- name: DebitBalanceForReservation :one
UPDATE users
SET balance = balance - $2,
    balance_version = balance_version + 1,
    updated_at = now()
WHERE external_id = $1 AND enabled = TRUE AND $2 > 0 AND balance >= $2
RETURNING balance, balance_version
`

type DebitBalanceForReservationParams struct {
	ExternalID string `json:"external_id"`
	Amount     int64  `json:"amount"`
}

type DebitBalanceForReservationRow struct {
	Balance        int64 `json:"balance"`
	BalanceVersion int64 `json:"balance_version"`
}

func (q *Queries) DebitBalanceForReservation(ctx context.Context, arg DebitBalanceForReservationParams) (DebitBalanceForReservationRow, error) {
	row := q.db.QueryRow(ctx, debitBalanceForReservation, arg.ExternalID, arg.Amount)
	var result DebitBalanceForReservationRow
	err := row.Scan(&result.Balance, &result.BalanceVersion)
	return result, err
}

const adjustBalanceForBilling = `-- name: AdjustBalanceForBilling :one
UPDATE users
SET balance = balance + $2,
    balance_version = balance_version + 1,
    updated_at = now()
WHERE external_id = $1
RETURNING balance, balance_version
`

type AdjustBalanceForBillingParams struct {
	ExternalID string `json:"external_id"`
	Delta      int64  `json:"delta"`
}

type AdjustBalanceForBillingRow struct {
	Balance        int64 `json:"balance"`
	BalanceVersion int64 `json:"balance_version"`
}

func (q *Queries) AdjustBalanceForBilling(ctx context.Context, arg AdjustBalanceForBillingParams) (AdjustBalanceForBillingRow, error) {
	row := q.db.QueryRow(ctx, adjustBalanceForBilling, arg.ExternalID, arg.Delta)
	var result AdjustBalanceForBillingRow
	err := row.Scan(&result.Balance, &result.BalanceVersion)
	return result, err
}

const recordBillingConsumption = `-- name: RecordBillingConsumption :execrows
UPDATE billing_reservations
SET status = 'consumed_unsettled',
    channel = $2,
    input_tokens = $3,
    output_tokens = $4,
    cache_creation_input_tokens = $5,
    cache_read_input_tokens = $6,
    reported_total_tokens = $7,
    cost = $8,
    usage_complete = $9,
    estimated = $10,
    consumed_at = $11,
    updated_at = $11
WHERE reservation_id = $1 AND status = 'in_flight'
`

type RecordBillingConsumptionParams struct {
	ReservationID            string             `json:"reservation_id"`
	Channel                  string             `json:"channel"`
	InputTokens              int32              `json:"input_tokens"`
	OutputTokens             int32              `json:"output_tokens"`
	CacheCreationInputTokens int32              `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int32              `json:"cache_read_input_tokens"`
	ReportedTotalTokens      int32              `json:"reported_total_tokens"`
	Cost                     int64              `json:"cost"`
	UsageComplete            bool               `json:"usage_complete"`
	Estimated                bool               `json:"estimated"`
	RecordedAt               pgtype.Timestamptz `json:"recorded_at"`
}

func (q *Queries) RecordBillingConsumption(ctx context.Context, arg RecordBillingConsumptionParams) (int64, error) {
	result, err := q.db.Exec(ctx, recordBillingConsumption,
		arg.ReservationID,
		arg.Channel,
		arg.InputTokens,
		arg.OutputTokens,
		arg.CacheCreationInputTokens,
		arg.CacheReadInputTokens,
		arg.ReportedTotalTokens,
		arg.Cost,
		arg.UsageComplete,
		arg.Estimated,
		arg.RecordedAt,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

const markBillingReservationInFlight = `-- name: MarkBillingReservationInFlight :execrows
UPDATE billing_reservations
SET status = 'in_flight', channel = $2, updated_at = $3
WHERE reservation_id = $1 AND status = 'reserved'
`

type MarkBillingReservationInFlightParams struct {
	ReservationID string             `json:"reservation_id"`
	Channel       string             `json:"channel"`
	UpdatedAt     pgtype.Timestamptz `json:"updated_at"`
}

func (q *Queries) MarkBillingReservationInFlight(ctx context.Context, arg MarkBillingReservationInFlightParams) (int64, error) {
	result, err := q.db.Exec(ctx, markBillingReservationInFlight, arg.ReservationID, arg.Channel, arg.UpdatedAt)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

const releaseBillingAttempt = `-- name: ReleaseBillingAttempt :execrows
UPDATE billing_reservations
SET status = 'reserved', updated_at = $2
WHERE reservation_id = $1 AND status = 'in_flight'
`

type ReleaseBillingAttemptParams struct {
	ReservationID string             `json:"reservation_id"`
	UpdatedAt     pgtype.Timestamptz `json:"updated_at"`
}

func (q *Queries) ReleaseBillingAttempt(ctx context.Context, arg ReleaseBillingAttemptParams) (int64, error) {
	result, err := q.db.Exec(ctx, releaseBillingAttempt, arg.ReservationID, arg.UpdatedAt)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

const markBillingReservationSettled = `-- name: MarkBillingReservationSettled :execrows
UPDATE billing_reservations
SET status = 'settled', settled_at = $2, updated_at = $2
WHERE reservation_id = $1 AND status = 'consumed_unsettled'
`

type MarkBillingReservationSettledParams struct {
	ReservationID string             `json:"reservation_id"`
	SettledAt     pgtype.Timestamptz `json:"settled_at"`
}

func (q *Queries) MarkBillingReservationSettled(ctx context.Context, arg MarkBillingReservationSettledParams) (int64, error) {
	result, err := q.db.Exec(ctx, markBillingReservationSettled, arg.ReservationID, arg.SettledAt)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

const markBillingReservationRefunded = `-- name: MarkBillingReservationRefunded :execrows
UPDATE billing_reservations
SET status = 'refunded', refunded_at = $2, updated_at = $2
WHERE reservation_id = $1 AND status = 'reserved'
`

type MarkBillingReservationRefundedParams struct {
	ReservationID string             `json:"reservation_id"`
	RefundedAt    pgtype.Timestamptz `json:"refunded_at"`
}

func (q *Queries) MarkBillingReservationRefunded(ctx context.Context, arg MarkBillingReservationRefundedParams) (int64, error) {
	result, err := q.db.Exec(ctx, markBillingReservationRefunded, arg.ReservationID, arg.RefundedAt)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

const insertBillingLedgerEntry = `-- name: InsertBillingLedgerEntry :exec
INSERT INTO billing_ledger (
    operation_id, reservation_id, user_id, kind, amount,
    balance_after, balance_version, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
`

type InsertBillingLedgerEntryParams struct {
	OperationID    string             `json:"operation_id"`
	ReservationID  string             `json:"reservation_id"`
	UserID         string             `json:"user_id"`
	Kind           string             `json:"kind"`
	Amount         int64              `json:"amount"`
	BalanceAfter   int64              `json:"balance_after"`
	BalanceVersion int64              `json:"balance_version"`
	CreatedAt      pgtype.Timestamptz `json:"created_at"`
}

func (q *Queries) InsertBillingLedgerEntry(ctx context.Context, arg InsertBillingLedgerEntryParams) error {
	_, err := q.db.Exec(ctx, insertBillingLedgerEntry,
		arg.OperationID,
		arg.ReservationID,
		arg.UserID,
		arg.Kind,
		arg.Amount,
		arg.BalanceAfter,
		arg.BalanceVersion,
		arg.CreatedAt,
	)
	return err
}

const insertFinalizedUsageLog = `-- name: InsertFinalizedUsageLog :exec
INSERT INTO usage_logs (
    request_id, user_id, key_id, model, channel,
    input_tokens, output_tokens, cache_creation_input_tokens,
    cache_read_input_tokens, reported_total_tokens, cost,
    usage_complete, estimated, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
`

type InsertFinalizedUsageLogParams struct {
	RequestID                string             `json:"request_id"`
	UserID                   string             `json:"user_id"`
	KeyID                    string             `json:"key_id"`
	Model                    string             `json:"model"`
	Channel                  string             `json:"channel"`
	InputTokens              int32              `json:"input_tokens"`
	OutputTokens             int32              `json:"output_tokens"`
	CacheCreationInputTokens int32              `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int32              `json:"cache_read_input_tokens"`
	ReportedTotalTokens      int32              `json:"reported_total_tokens"`
	Cost                     int64              `json:"cost"`
	UsageComplete            bool               `json:"usage_complete"`
	Estimated                bool               `json:"estimated"`
	CreatedAt                pgtype.Timestamptz `json:"created_at"`
}

func (q *Queries) InsertFinalizedUsageLog(ctx context.Context, arg InsertFinalizedUsageLogParams) error {
	_, err := q.db.Exec(ctx, insertFinalizedUsageLog,
		arg.RequestID,
		arg.UserID,
		arg.KeyID,
		arg.Model,
		arg.Channel,
		arg.InputTokens,
		arg.OutputTokens,
		arg.CacheCreationInputTokens,
		arg.CacheReadInputTokens,
		arg.ReportedTotalTokens,
		arg.Cost,
		arg.UsageComplete,
		arg.Estimated,
		arg.CreatedAt,
	)
	return err
}

const listConsumedUnsettledReservations = `-- name: ListConsumedUnsettledReservations :many
SELECT reservation_id
FROM billing_reservations
WHERE status = 'consumed_unsettled'
ORDER BY consumed_at, reservation_id
`

func (q *Queries) ListConsumedUnsettledReservations(ctx context.Context) ([]string, error) {
	rows, err := q.db.Query(ctx, listConsumedUnsettledReservations)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

const listStaleReservedReservations = `-- name: ListStaleReservedReservations :many
SELECT reservation_id
FROM billing_reservations
WHERE status = 'reserved' AND updated_at < $1
ORDER BY updated_at, reservation_id
`

func (q *Queries) ListStaleReservedReservations(ctx context.Context, cutoff pgtype.Timestamptz) ([]string, error) {
	rows, err := q.db.Query(ctx, listStaleReservedReservations, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

const listStaleInFlightReservations = `-- name: ListStaleInFlightReservations :many
SELECT reservation_id
FROM billing_reservations
WHERE status = 'in_flight' AND updated_at < $1
ORDER BY updated_at, reservation_id
`

func (q *Queries) ListStaleInFlightReservations(ctx context.Context, cutoff pgtype.Timestamptz) ([]string, error) {
	rows, err := q.db.Query(ctx, listStaleInFlightReservations, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}
