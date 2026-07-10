package billing

import (
	"context"
	"errors"
	"time"
)

// ReservationStatus 是持久预授权的状态机。只有 reserved 可以退款；上游一旦
// 确认产生成功响应，先记录为 consumed_unsettled，再原子结算为 settled。
type ReservationStatus string

const (
	ReservationReserved          ReservationStatus = "reserved"
	ReservationInFlight          ReservationStatus = "in_flight"
	ReservationConsumedUnsettled ReservationStatus = "consumed_unsettled"
	ReservationSettled           ReservationStatus = "settled"
	ReservationRefunded          ReservationStatus = "refunded"
)

var (
	ErrReservationConflict   = errors.New("billing: reservation 幂等参数冲突")
	ErrInvalidTransition     = errors.New("billing: reservation 状态转换非法")
	ErrReservationExceeded   = errors.New("billing: 实际成本超过预授权上限")
	ErrAmbiguousReservations = errors.New("billing: 存在发送结果不确定的 in_flight reservation")
)

// Reservation 是一次持久预授权。ID 是网关内部随机标识，绝不复用客户端提供的
// X-Request-Id，避免外部 ID 冲突影响账单幂等性。
type Reservation struct {
	ID              string
	TraceID         string
	UserID          string
	KeyID           string
	Model           string
	Amount          int64
	MaxInputTokens  int
	MaxOutputTokens int
	CreatedAt       time.Time
}

// Consumption 是一次已发生上游消费的结算事实。Estimated=true 表示上游 usage
// 缺失/冲突，Cost 使用安全上界而不是把缺失字段当成零。
type Consumption struct {
	ReservationID            string
	Channel                  string
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	ReportedTotalTokens      int
	Cost                     int64
	UsageComplete            bool
	Estimated                bool
	RecordedAt               time.Time
}

// Ledger 是资金正确性的唯一落点。生产实现必须在同一 PostgreSQL 事务内更新
// users.balance、reservation、append-only ledger 和 usage_logs。
type Ledger interface {
	Reserve(ctx context.Context, r Reservation) (bool, error)
	MarkInFlight(ctx context.Context, reservationID, channel string) error
	ReleaseAttempt(ctx context.Context, reservationID string) error
	RecordConsumption(ctx context.Context, c Consumption) error
	Finalize(ctx context.Context, reservationID string) error
	Refund(ctx context.Context, reservationID string) error
	Recover(ctx context.Context) error
}
