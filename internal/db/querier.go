package db

import (
	"context"
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
	ListAPIKeysByUser(ctx context.Context, userExternalID string) ([]ListAPIKeysByUserRow, error)
	SetAPIKeyEnabled(ctx context.Context, arg SetAPIKeyEnabledParams) (ListAPIKeysByUserRow, error)
	// channels
	ListEnabledChannels(ctx context.Context) ([]ListEnabledChannelsRow, error)
	CreateChannel(ctx context.Context, arg CreateChannelParams) (Channel, error)
	ListAllChannels(ctx context.Context) ([]Channel, error)
	GetChannel(ctx context.Context, channelID string) (Channel, error)
	UpdateChannel(ctx context.Context, arg UpdateChannelParams) (Channel, error)
	SetChannelEnabled(ctx context.Context, arg SetChannelEnabledParams) (Channel, error)
	DeleteChannel(ctx context.Context, channelID string) (int64, error)
	// usage_logs
	InsertUsageLog(ctx context.Context, arg InsertUsageLogParams) error
	SumCostByUser(ctx context.Context, arg SumCostByUserParams) (int64, error)
}

// 编译期断言：Queries 必须实现 Querier。
var _ Querier = (*Queries)(nil)
