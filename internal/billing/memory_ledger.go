package billing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"linapi/internal/store"
)

// MemoryLedger 是开发/测试用账本。它与 MemoryStore 共享同一余额 map，因此管理面
// 充值和请求结算即时一致；进程重启后不持久化，release 模式不得使用。
type MemoryLedger struct {
	mu      sync.Mutex
	store   *store.MemoryStore
	records map[string]*memoryLedgerRecord
}

type memoryLedgerRecord struct {
	reservation    Reservation
	status         ReservationStatus
	attemptChannel string
	attemptedAt    time.Time
	consumption    Consumption
}

// MemoryLedgerSnapshot 用于测试和诊断，不暴露内部可变指针。
type MemoryLedgerSnapshot struct {
	Reservation Reservation
	Status      ReservationStatus
	Consumption Consumption
}

func NewMemoryLedger(s *store.MemoryStore) *MemoryLedger {
	return &MemoryLedger{store: s, records: make(map[string]*memoryLedgerRecord)}
}

func (l *MemoryLedger) Reserve(_ context.Context, r Reservation) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if old, ok := l.records[r.ID]; ok {
		if !sameReservation(old.reservation, r) {
			return false, ErrReservationConflict
		}
		if old.status != ReservationReserved {
			return false, ErrInvalidTransition
		}
		return true, nil
	}
	ok, _, err := l.store.BillingReserve(r.UserID, r.Amount)
	if err != nil || !ok {
		return ok, err
	}
	l.records[r.ID] = &memoryLedgerRecord{reservation: r, status: ReservationReserved}
	return true, nil
}

func (l *MemoryLedger) RecordConsumption(_ context.Context, c Consumption) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.records[c.ReservationID]
	if !ok {
		return fmt.Errorf("%w: reservation 不存在", ErrInvalidTransition)
	}
	switch rec.status {
	case ReservationInFlight:
		if rec.attemptChannel != "" && rec.attemptChannel != c.Channel {
			return ErrReservationConflict
		}
		rec.consumption = c
		rec.status = ReservationConsumedUnsettled
		return nil
	case ReservationConsumedUnsettled, ReservationSettled:
		if !sameConsumption(rec.consumption, c) {
			return ErrReservationConflict
		}
		return nil
	default:
		return ErrInvalidTransition
	}
}

func (l *MemoryLedger) MarkInFlight(_ context.Context, reservationID, channel string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.records[reservationID]
	if !ok {
		return ErrInvalidTransition
	}
	if rec.status == ReservationInFlight {
		if rec.attemptChannel != channel {
			return ErrReservationConflict
		}
		return nil
	}
	if rec.status != ReservationReserved {
		return ErrInvalidTransition
	}
	rec.status = ReservationInFlight
	rec.attemptChannel = channel
	rec.attemptedAt = time.Now().UTC()
	return nil
}

func (l *MemoryLedger) ReleaseAttempt(_ context.Context, reservationID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.records[reservationID]
	if !ok {
		return ErrInvalidTransition
	}
	if rec.status == ReservationReserved {
		return nil
	}
	if rec.status != ReservationInFlight {
		return ErrInvalidTransition
	}
	rec.status = ReservationReserved
	rec.attemptedAt = time.Time{}
	return nil
}

func (l *MemoryLedger) Finalize(_ context.Context, reservationID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.records[reservationID]
	if !ok {
		return fmt.Errorf("%w: reservation 不存在", ErrInvalidTransition)
	}
	if rec.status == ReservationSettled {
		return nil
	}
	if rec.status != ReservationConsumedUnsettled {
		return ErrInvalidTransition
	}
	if rec.consumption.Cost > rec.reservation.Amount {
		return ErrReservationExceeded
	}
	if rec.consumption.Cost < 0 {
		return ErrReservationConflict
	}
	if _, err := l.store.BillingAdjust(rec.reservation.UserID, rec.reservation.Amount-rec.consumption.Cost); err != nil {
		return err
	}
	rec.status = ReservationSettled
	return nil
}

func (l *MemoryLedger) Refund(_ context.Context, reservationID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.records[reservationID]
	if !ok {
		return fmt.Errorf("%w: reservation 不存在", ErrInvalidTransition)
	}
	if rec.status == ReservationRefunded {
		return nil
	}
	if rec.status != ReservationReserved {
		return ErrInvalidTransition
	}
	if _, err := l.store.BillingAdjust(rec.reservation.UserID, rec.reservation.Amount); err != nil {
		return err
	}
	rec.status = ReservationRefunded
	return nil
}

func (l *MemoryLedger) Recover(ctx context.Context) error {
	l.mu.Lock()
	ids := make([]string, 0)
	staleReserved := make([]string, 0)
	var ambiguous bool
	now := time.Now().UTC()
	for id, rec := range l.records {
		if rec.status == ReservationConsumedUnsettled {
			ids = append(ids, id)
		} else if rec.status == ReservationReserved && !rec.reservation.CreatedAt.IsZero() &&
			rec.reservation.CreatedAt.Before(now.Add(-staleReservedAfter)) {
			staleReserved = append(staleReserved, id)
		} else if rec.status == ReservationInFlight && !rec.attemptedAt.IsZero() &&
			rec.attemptedAt.Before(now.Add(-staleInFlightAfter)) {
			ambiguous = true
		}
	}
	l.mu.Unlock()
	for _, id := range ids {
		if err := l.Finalize(ctx, id); err != nil {
			return err
		}
	}
	for _, id := range staleReserved {
		if err := l.Refund(ctx, id); err != nil {
			return err
		}
	}
	if ambiguous {
		return ErrAmbiguousReservations
	}
	return nil
}

func (l *MemoryLedger) Snapshot(id string) (MemoryLedgerSnapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.records[id]
	if !ok {
		return MemoryLedgerSnapshot{}, false
	}
	return MemoryLedgerSnapshot{Reservation: rec.reservation, Status: rec.status, Consumption: rec.consumption}, true
}

func sameReservation(a, b Reservation) bool {
	return a.ID == b.ID && a.TraceID == b.TraceID && a.UserID == b.UserID &&
		a.KeyID == b.KeyID && a.Model == b.Model && a.Amount == b.Amount &&
		a.MaxInputTokens == b.MaxInputTokens && a.MaxOutputTokens == b.MaxOutputTokens
}

func sameConsumption(a, b Consumption) bool {
	return a.ReservationID == b.ReservationID && a.Channel == b.Channel &&
		a.InputTokens == b.InputTokens && a.OutputTokens == b.OutputTokens &&
		a.CacheCreationInputTokens == b.CacheCreationInputTokens &&
		a.CacheReadInputTokens == b.CacheReadInputTokens &&
		a.ReportedTotalTokens == b.ReportedTotalTokens && a.Cost == b.Cost &&
		a.UsageComplete == b.UsageComplete && a.Estimated == b.Estimated
}
