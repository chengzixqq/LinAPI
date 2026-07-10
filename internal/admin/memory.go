package admin

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"linapi/internal/store"
)

// MemoryStore 是 AdminStore 的内存实现（database.enabled=false 时用）。
//
// 用户/密钥/余额复用 store.MemoryStore 的数据，使管理面写入能被热路径
// 鉴权/额度中间件即时读到；渠道则由本类型自持一份 map 管理。
// 仅供开发/测试；生产走 PGStore。
type MemoryStore struct {
	base *store.MemoryStore

	mu       sync.RWMutex
	channels map[string]*Channel // channel_id -> 渠道
}

// NewMemoryStore 包装一个 store.MemoryStore，并以给定渠道初始化渠道表。
func NewMemoryStore(base *store.MemoryStore, seed []Channel) *MemoryStore {
	m := &MemoryStore{
		base:     base,
		channels: make(map[string]*Channel, len(seed)),
	}
	for i := range seed {
		c := seed[i]
		m.channels[c.ChannelID] = &c
	}
	return m
}

// mapUserErr 把 store 层 sentinel 映射为 admin 领域错误。
func mapUserErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrUserExists), errors.Is(err, store.ErrKeyExists):
		return ErrConflict
	case errors.Is(err, store.ErrUserNotFound), errors.Is(err, store.ErrKeyNotFound):
		return ErrNotFound
	default:
		return err
	}
}

// ---- 用户 ----

func (m *MemoryStore) CreateUser(_ context.Context, in CreateUserInput) (User, error) {
	v, err := m.base.AdminCreateUser(in.ExternalID, in.Balance, in.Enabled)
	if err != nil {
		return User{}, mapUserErr(err)
	}
	return userFromView(v), nil
}

func (m *MemoryStore) ListUsers(_ context.Context, limit, offset int) ([]User, error) {
	views := m.base.AdminListUsers(limit, offset)
	users := make([]User, 0, len(views))
	for _, v := range views {
		users = append(users, userFromView(v))
	}
	return users, nil
}

func (m *MemoryStore) GetUser(_ context.Context, externalID string) (User, error) {
	v, err := m.base.AdminGetUser(externalID)
	if err != nil {
		return User{}, mapUserErr(err)
	}
	return userFromView(v), nil
}

func (m *MemoryStore) SetUserEnabled(_ context.Context, externalID string, enabled bool) (User, error) {
	v, err := m.base.AdminSetUserEnabled(externalID, enabled)
	if err != nil {
		return User{}, mapUserErr(err)
	}
	return userFromView(v), nil
}

func (m *MemoryStore) AddBalance(_ context.Context, externalID string, delta int64) (int64, error) {
	bal, err := m.base.AdminAddBalance(externalID, delta)
	return bal, mapUserErr(err)
}

// ---- 密钥 ----

func (m *MemoryStore) CreateAPIKey(_ context.Context, in CreateAPIKeyInput) (APIKey, error) {
	v, err := m.base.AdminCreateKey(in.APIKey, in.KeyID, in.UserID, in.RateLimitPerMin, in.AllowedModels, in.Enabled)
	if err != nil {
		return APIKey{}, mapUserErr(err)
	}
	return keyFromView(v), nil
}

func (m *MemoryStore) ListAPIKeysByUser(_ context.Context, userID string) ([]APIKey, error) {
	views := m.base.AdminListKeysByUser(userID)
	keys := make([]APIKey, 0, len(views))
	for _, v := range views {
		keys = append(keys, keyFromView(v))
	}
	return keys, nil
}

func (m *MemoryStore) SetAPIKeyEnabled(_ context.Context, keyID string, enabled bool) (APIKey, error) {
	v, err := m.base.AdminSetKeyEnabled(keyID, enabled)
	if err != nil {
		return APIKey{}, mapUserErr(err)
	}
	return keyFromView(v), nil
}

func (m *MemoryStore) DeleteAPIKey(_ context.Context, keyID string) error {
	return mapUserErr(m.base.AdminDeleteKey(keyID))
}

// ---- 渠道 ----

func (m *MemoryStore) CreateChannel(_ context.Context, in ChannelInput) (Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.channels[in.ChannelID]; ok {
		return Channel{}, ErrConflict
	}
	now := time.Now()
	c := channelFromInput(in, now, now)
	m.channels[in.ChannelID] = &c
	return c, nil
}

func (m *MemoryStore) ListChannels(_ context.Context) ([]Channel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Channel, 0, len(m.channels))
	for _, c := range m.channels {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ChannelID < out[j].ChannelID
	})
	return out, nil
}

func (m *MemoryStore) GetChannel(_ context.Context, channelID string) (Channel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.channels[channelID]
	if !ok {
		return Channel{}, ErrNotFound
	}
	return *c, nil
}

func (m *MemoryStore) UpdateChannel(_ context.Context, in ChannelInput) (Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.channels[in.ChannelID]
	if !ok {
		return Channel{}, ErrNotFound
	}
	c := channelFromInput(in, old.CreatedAt, time.Now())
	m.channels[in.ChannelID] = &c
	return c, nil
}

func (m *MemoryStore) SetChannelEnabled(_ context.Context, channelID string, enabled bool) (Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.channels[channelID]
	if !ok {
		return Channel{}, ErrNotFound
	}
	c.Enabled = enabled
	c.UpdatedAt = time.Now()
	return *c, nil
}

func (m *MemoryStore) DeleteChannel(_ context.Context, channelID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.channels[channelID]; !ok {
		return ErrNotFound
	}
	delete(m.channels, channelID)
	return nil
}

// ---- 转换辅助 ----

func userFromView(v store.MemUserView) User {
	return User{
		ExternalID: v.ExternalID,
		Balance:    v.Balance,
		Enabled:    v.Enabled,
		CreatedAt:  v.CreatedAt,
		UpdatedAt:  v.CreatedAt,
	}
}

func keyFromView(v store.MemKeyView) APIKey {
	return APIKey{
		KeyID:           v.KeyID,
		UserID:          v.UserID,
		RateLimitPerMin: v.RateLimitPerMin,
		AllowedModels:   v.AllowedModels,
		Enabled:         v.Enabled,
		CreatedAt:       v.CreatedAt,
	}
}

func channelFromInput(in ChannelInput, createdAt, updatedAt time.Time) Channel {
	models := in.Models
	if models == nil {
		models = map[string]string{}
	}
	return Channel{
		ChannelID: in.ChannelID,
		Name:      in.Name,
		Format:    in.Format,
		BaseURL:   in.BaseURL,
		APIKey:    in.APIKey,
		Models:    models,
		Priority:  in.Priority,
		Weight:    in.Weight,
		Enabled:   in.Enabled,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}
