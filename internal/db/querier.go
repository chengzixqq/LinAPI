package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

// Querier 是 Queries 实现的接口（sqlc emit_interface 产物）。
// 上层可依赖此接口而非具体类型，便于测试替换。
type Querier interface {
	// users
	GetUserByExternalID(ctx context.Context, externalID string) (User, error)
	GetBalance(ctx context.Context, externalID string) (int64, error)
	AddBalance(ctx context.Context, arg AddBalanceParams) (int64, error)
	CreateUser(ctx context.Context, arg CreateUserParams) (User, error)
	ListUsers(ctx context.Context, arg ListUsersParams) ([]User, error)
	SetUserEnabled(ctx context.Context, arg SetUserEnabledParams) (User, error)
	// api_keys
	ResolveAPIKey(ctx context.Context, keyHash string) (ResolveAPIKeyRow, error)
	CreateAPIKey(ctx context.Context, arg CreateAPIKeyParams) (ApiKey, error)
	CreateAPIKeyLimited(ctx context.Context, arg CreateAPIKeyLimitedParams) (ApiKey, error)
	ListAPIKeysByUser(ctx context.Context, userExternalID string) ([]ListAPIKeysByUserRow, error)
	SetAPIKeyEnabled(ctx context.Context, arg SetAPIKeyEnabledParams) (ListAPIKeysByUserRow, error)
	DeleteAPIKey(ctx context.Context, keyID string) (int64, error)
	// channels
	ListEnabledChannels(ctx context.Context) ([]ListEnabledChannelsRow, error)
	ListChannelKeyMaterialsForUpdate(ctx context.Context) ([]ListChannelKeyMaterialsForUpdateRow, error)
	UpdateChannelKeyMaterial(ctx context.Context, arg UpdateChannelKeyMaterialParams) (int64, error)
	CreateChannel(ctx context.Context, arg CreateChannelParams) (Channel, error)
	ListAllChannels(ctx context.Context) ([]Channel, error)
	GetChannel(ctx context.Context, channelID string) (Channel, error)
	UpdateChannel(ctx context.Context, arg UpdateChannelParams) (Channel, error)
	SetChannelEnabled(ctx context.Context, arg SetChannelEnabledParams) (Channel, error)
	DeleteChannel(ctx context.Context, channelID string) (int64, error)
	// accounts
	CreateAccount(ctx context.Context, arg CreateAccountParams) (Account, error)
	GetAccountByUsername(ctx context.Context, username string) (Account, error)
	GetAccountByID(ctx context.Context, id int64) (Account, error)
	ListAccounts(ctx context.Context, arg ListAccountsParams) ([]Account, error)
	CountAccounts(ctx context.Context) (int64, error)
	SetAccountEnabled(ctx context.Context, arg SetAccountEnabledParams) (Account, error)
	UpdateAccountPassword(ctx context.Context, arg UpdateAccountPasswordParams) error
	// settings
	GetSetting(ctx context.Context, key string) (Setting, error)
	UpsertSetting(ctx context.Context, arg UpsertSettingParams) error
	GetSettingsSnapshot(ctx context.Context) (GetSettingsSnapshotRow, error)
	UpsertSettingsSnapshot(ctx context.Context, arg UpsertSettingsSnapshotParams) error
	// usage_logs
	SumCostByUser(ctx context.Context, arg SumCostByUserParams) (int64, error)
	// billing ledger
	InsertBillingReservation(ctx context.Context, arg InsertBillingReservationParams) (BillingReservation, error)
	GetBillingReservation(ctx context.Context, reservationID string) (BillingReservation, error)
	GetBillingReservationForUpdate(ctx context.Context, reservationID string) (BillingReservation, error)
	DebitBalanceForReservation(ctx context.Context, arg DebitBalanceForReservationParams) (DebitBalanceForReservationRow, error)
	AdjustBalanceForBilling(ctx context.Context, arg AdjustBalanceForBillingParams) (AdjustBalanceForBillingRow, error)
	RecordBillingConsumption(ctx context.Context, arg RecordBillingConsumptionParams) (int64, error)
	MarkBillingReservationInFlight(ctx context.Context, arg MarkBillingReservationInFlightParams) (int64, error)
	ReleaseBillingAttempt(ctx context.Context, arg ReleaseBillingAttemptParams) (int64, error)
	MarkBillingReservationSettled(ctx context.Context, arg MarkBillingReservationSettledParams) (int64, error)
	MarkBillingReservationRefunded(ctx context.Context, arg MarkBillingReservationRefundedParams) (int64, error)
	InsertBillingLedgerEntry(ctx context.Context, arg InsertBillingLedgerEntryParams) error
	InsertFinalizedUsageLog(ctx context.Context, arg InsertFinalizedUsageLogParams) error
	ListConsumedUnsettledReservations(ctx context.Context) ([]string, error)
	ListStaleReservedReservations(ctx context.Context, cutoff pgtype.Timestamptz) ([]string, error)
	ListStaleInFlightReservations(ctx context.Context, cutoff pgtype.Timestamptz) ([]string, error)
}

// 编译期断言：Queries 必须实现 Querier。
var _ Querier = (*Queries)(nil)
