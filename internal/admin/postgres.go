package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"linapi/internal/db"
	"linapi/internal/store"
)

// PGStore 是 AdminStore 的 PostgreSQL 实现，通过 sqlc 查询器访问库。
// 并发安全（底层 pgxpool 并发安全）。
type PGStore struct {
	q           db.Querier
	channelKeys *ChannelKeyCipher
}

// NewPGStore 用一个 sqlc 查询器构造 PGStore。PostgreSQL 渠道操作必须同时传入
// ChannelKeyCipher；保留可选参数只为不涉及渠道的旧测试/调用方兼容。
func NewPGStore(q db.Querier, channelKeys ...*ChannelKeyCipher) *PGStore {
	var keys *ChannelKeyCipher
	if len(channelKeys) > 0 {
		keys = channelKeys[0]
	}
	return &PGStore{q: q, channelKeys: keys}
}

// mapWriteErr 把 pgx 写错误归一：唯一约束冲突 -> ErrConflict，无行 -> ErrNotFound。
func mapWriteErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrConflict
		case "23503": // foreign_key_violation：引用的用户不存在
			return ErrNotFound
		}
	}
	return err
}

// ---- 用户 ----

func (s *PGStore) CreateUser(ctx context.Context, in CreateUserInput) (User, error) {
	u, err := s.q.CreateUser(ctx, db.CreateUserParams{
		ExternalID: in.ExternalID,
		Balance:    in.Balance,
		Enabled:    in.Enabled,
	})
	if err != nil {
		return User{}, mapWriteErr(err)
	}
	return userFromDB(u), nil
}

func (s *PGStore) ListUsers(ctx context.Context, limit, offset int) ([]User, error) {
	rows, err := s.q.ListUsers(ctx, db.ListUsersParams{
		Limit:  int32(limit),
		Offset: int32(offset),
	})
	if err != nil {
		return nil, err
	}
	users := make([]User, 0, len(rows))
	for _, r := range rows {
		users = append(users, userFromDB(r))
	}
	return users, nil
}

func (s *PGStore) GetUser(ctx context.Context, externalID string) (User, error) {
	u, err := s.q.GetUserByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	return userFromDB(u), nil
}

func (s *PGStore) SetUserEnabled(ctx context.Context, externalID string, enabled bool) (User, error) {
	u, err := s.q.SetUserEnabled(ctx, db.SetUserEnabledParams{
		ExternalID: externalID,
		Enabled:    enabled,
	})
	if err != nil {
		return User{}, mapWriteErr(err)
	}
	return userFromDB(u), nil
}

func (s *PGStore) AddBalance(ctx context.Context, externalID string, delta int64) (int64, error) {
	// AddBalance 的 SQL 无 RETURNING 行不存在的区分，这里先确认用户存在再增减。
	if _, err := s.q.GetUserByExternalID(ctx, externalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	bal, err := s.q.AddBalance(ctx, db.AddBalanceParams{
		ExternalID: externalID,
		Delta:      delta,
	})
	if err != nil {
		return 0, mapWriteErr(err)
	}
	return bal, nil
}

// ---- 密钥 ----

func (s *PGStore) CreateAPIKey(ctx context.Context, in CreateAPIKeyInput) (APIKey, error) {
	if err := validateCreateAPIKeyInput(in); err != nil {
		return APIKey{}, err
	}
	k, err := s.q.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		KeyHash:         store.HashAPIKey(in.APIKey),
		KeyID:           in.KeyID,
		UserExternalID:  in.UserID,
		RateLimitPerMin: int32(in.RateLimitPerMin),
		AllowedModels:   normalizeModels(in.AllowedModels),
		Enabled:         in.Enabled,
	})
	if err != nil {
		return APIKey{}, mapWriteErr(err)
	}
	return APIKey{
		KeyID:           k.KeyID,
		UserID:          k.UserExternalID,
		RateLimitPerMin: int(k.RateLimitPerMin),
		AllowedModels:   k.AllowedModels,
		Enabled:         k.Enabled,
		CreatedAt:       k.CreatedAt.Time,
	}, nil
}

func (s *PGStore) CreateAPIKeyLimited(ctx context.Context, in CreateAPIKeyInput, maxKeys int) (APIKey, error) {
	if err := validateCreateAPIKeyInput(in); err != nil || maxKeys <= 0 {
		if err != nil {
			return APIKey{}, err
		}
		return APIKey{}, ErrInvalidInput
	}
	k, err := s.q.CreateAPIKeyLimited(ctx, db.CreateAPIKeyLimitedParams{
		KeyHash:         store.HashAPIKey(in.APIKey),
		KeyID:           in.KeyID,
		UserExternalID:  in.UserID,
		RateLimitPerMin: int32(in.RateLimitPerMin),
		AllowedModels:   normalizeModels(in.AllowedModels),
		Enabled:         in.Enabled,
		MaxKeys:         int32(maxKeys),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrLimitReached
	}
	if err != nil {
		return APIKey{}, mapWriteErr(err)
	}
	return APIKey{
		KeyID: k.KeyID, UserID: k.UserExternalID, RateLimitPerMin: int(k.RateLimitPerMin),
		AllowedModels: k.AllowedModels, Enabled: k.Enabled, CreatedAt: k.CreatedAt.Time,
	}, nil
}

func (s *PGStore) ListAPIKeysByUser(ctx context.Context, userID string) ([]APIKey, error) {
	rows, err := s.q.ListAPIKeysByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	keys := make([]APIKey, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, APIKey{
			KeyID:           r.KeyID,
			UserID:          r.UserExternalID,
			RateLimitPerMin: int(r.RateLimitPerMin),
			AllowedModels:   r.AllowedModels,
			Enabled:         r.Enabled,
			CreatedAt:       r.CreatedAt.Time,
		})
	}
	return keys, nil
}

func (s *PGStore) SetAPIKeyEnabled(ctx context.Context, keyID string, enabled bool) (APIKey, error) {
	r, err := s.q.SetAPIKeyEnabled(ctx, db.SetAPIKeyEnabledParams{
		KeyID:   keyID,
		Enabled: enabled,
	})
	if err != nil {
		return APIKey{}, mapWriteErr(err)
	}
	return APIKey{
		KeyID:           r.KeyID,
		UserID:          r.UserExternalID,
		RateLimitPerMin: int(r.RateLimitPerMin),
		AllowedModels:   r.AllowedModels,
		Enabled:         r.Enabled,
		CreatedAt:       r.CreatedAt.Time,
	}, nil
}

func (s *PGStore) DeleteAPIKey(ctx context.Context, keyID string) error {
	n, err := s.q.DeleteAPIKey(ctx, keyID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 渠道 ----

func (s *PGStore) CreateChannel(ctx context.Context, in ChannelInput) (Channel, error) {
	var err error
	in, err = normalizeChannelInput(in)
	if err != nil {
		return Channel{}, err
	}
	models, err := marshalModels(in.Models)
	if err != nil {
		return Channel{}, err
	}
	encryptedKey, err := s.encryptChannelKey(in.ChannelID, in.APIKey)
	if err != nil {
		return Channel{}, err
	}
	ch, err := s.q.CreateChannel(ctx, db.CreateChannelParams{
		ChannelID: in.ChannelID,
		Name:      in.Name,
		Format:    in.Format,
		BaseURL:   in.BaseURL,
		ApiKey:    encryptedKey,
		Models:    models,
		Priority:  int32(in.Priority),
		Weight:    int32(in.Weight),
		Enabled:   in.Enabled,
	})
	if err != nil {
		return Channel{}, mapWriteErr(err)
	}
	return s.channelFromDB(ch)
}

func (s *PGStore) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.q.ListAllChannels(ctx)
	if err != nil {
		return nil, err
	}
	channels := make([]Channel, 0, len(rows))
	for _, r := range rows {
		c, err := s.channelFromDB(r)
		if err != nil {
			return nil, err
		}
		channels = append(channels, c)
	}
	return channels, nil
}

func (s *PGStore) GetChannel(ctx context.Context, channelID string) (Channel, error) {
	ch, err := s.q.GetChannel(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, ErrNotFound
		}
		return Channel{}, err
	}
	return s.channelFromDB(ch)
}

func (s *PGStore) UpdateChannel(ctx context.Context, in ChannelInput) (Channel, error) {
	var err error
	in, err = normalizeChannelInput(in)
	if err != nil {
		return Channel{}, err
	}
	models, err := marshalModels(in.Models)
	if err != nil {
		return Channel{}, err
	}
	apiKeySet := in.APIKeySet
	encryptedKey := ""
	if apiKeySet {
		encryptedKey, err = s.encryptChannelKey(in.ChannelID, in.APIKey)
		if err != nil {
			return Channel{}, err
		}
	}
	ch, err := s.q.UpdateChannel(ctx, db.UpdateChannelParams{
		ChannelID: in.ChannelID,
		Name:      in.Name,
		Format:    in.Format,
		BaseURL:   in.BaseURL,
		ApiKey:    encryptedKey,
		Models:    models,
		Priority:  int32(in.Priority),
		Weight:    int32(in.Weight),
		Enabled:   in.Enabled,
		ApiKeySet: apiKeySet,
	})
	if err != nil {
		return Channel{}, mapWriteErr(err)
	}
	return s.channelFromDB(ch)
}

func (s *PGStore) SetChannelEnabled(ctx context.Context, channelID string, enabled bool) (Channel, error) {
	ch, err := s.q.SetChannelEnabled(ctx, db.SetChannelEnabledParams{
		ChannelID: channelID,
		Enabled:   enabled,
	})
	if err != nil {
		return Channel{}, mapWriteErr(err)
	}
	return s.channelFromDB(ch)
}

func (s *PGStore) DeleteChannel(ctx context.Context, channelID string) error {
	n, err := s.q.DeleteChannel(ctx, channelID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 转换辅助 ----

func userFromDB(u db.User) User {
	return User{
		ExternalID: u.ExternalID,
		Balance:    u.Balance,
		Enabled:    u.Enabled,
		CreatedAt:  u.CreatedAt.Time,
		UpdatedAt:  u.UpdatedAt.Time,
	}
}

func (s *PGStore) channelFromDB(c db.Channel) (Channel, error) {
	apiKey, err := s.decryptChannelKey(c.ChannelID, c.ApiKey)
	if err != nil {
		return Channel{}, err
	}
	models := map[string]string{}
	if len(c.Models) > 0 {
		if err := json.Unmarshal(c.Models, &models); err != nil {
			return Channel{}, err
		}
	}
	return Channel{
		ChannelID: c.ChannelID,
		Name:      c.Name,
		Format:    c.Format,
		BaseURL:   c.BaseURL,
		APIKey:    apiKey,
		Models:    models,
		Priority:  int(c.Priority),
		Weight:    int(c.Weight),
		Enabled:   c.Enabled,
		CreatedAt: c.CreatedAt.Time,
		UpdatedAt: c.UpdatedAt.Time,
	}, nil
}

func (s *PGStore) encryptChannelKey(channelID, plaintext string) (string, error) {
	if s.channelKeys == nil {
		return "", ErrChannelKeyEncryptionRequired
	}
	return s.channelKeys.Encrypt(channelID, plaintext)
}

func (s *PGStore) decryptChannelKey(channelID, envelope string) (string, error) {
	if s.channelKeys == nil {
		return "", ErrChannelKeyEncryptionRequired
	}
	plaintext, err := s.channelKeys.Decrypt(channelID, envelope)
	if err != nil {
		return "", fmt.Errorf("渠道 %q 的密钥密文无法解密: %w", channelID, ErrInvalidChannelKeyEnvelope)
	}
	return plaintext, nil
}

// marshalModels 把「对外名->上游名」映射编码为 JSONB 字节；nil/空映射编码为 "{}"。
func marshalModels(m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// normalizeModels 保证 allowed_models 落库为非 nil 切片（对齐 schema 的 NOT NULL DEFAULT '{}'）。
func normalizeModels(models []string) []string {
	if models == nil {
		return []string{}
	}
	return models
}
