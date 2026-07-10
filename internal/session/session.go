// Package session 提供基于 Redis 的会话管理：不透明 token -> 会话数据。
//
// 会话是控制台鉴权的凭据载体：登录成功后 Create 生成随机 token 存入 Redis，
// 通过 HttpOnly Cookie 下发；后续请求由中间件用 token 反查会话数据。
// Redis 不可用时 Create/Get 直接返回错误（fail-closed，绝不降级为无鉴权）。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// TTL 常量。
const (
	DefaultTTL  = 24 * time.Hour
	RememberTTL = 7 * 24 * time.Hour
)

// keyPrefix 是会话在 Redis 的键前缀。
const keyPrefix = "session:"

// ErrNotFound 表示会话不存在或已过期。
var ErrNotFound = errors.New("session: 会话不存在或已过期")

// SessionData 是一份会话承载的身份信息（登录时写入，鉴权时读出）。
type SessionData struct {
	AccountID  int64  `json:"account_id"`
	Username   string `json:"username"`
	Role       string `json:"role"`
	ExternalID string `json:"external_id"`
}

// Manager 管理会话的生命周期。
type Manager struct {
	rdb *redis.Client
}

// NewManager 构造会话管理器。
func NewManager(rdb *redis.Client) *Manager {
	return &Manager{rdb: rdb}
}

// Create 生成一个随机 token，把会话数据以给定 TTL 存入 Redis。
func (m *Manager) Create(ctx context.Context, data SessionData, ttl time.Duration) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成会话 token 失败: %w", err)
	}
	token := hex.EncodeToString(buf)

	payload, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	if err := m.rdb.Set(ctx, keyPrefix+token, payload, ttl).Err(); err != nil {
		return "", fmt.Errorf("写入会话失败: %w", err)
	}
	return token, nil
}

// Get 按 token 反查会话数据；不存在或过期返回 ErrNotFound。
func (m *Manager) Get(ctx context.Context, token string) (SessionData, error) {
	raw, err := m.rdb.Get(ctx, keyPrefix+token).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return SessionData{}, ErrNotFound
		}
		return SessionData{}, err
	}
	var data SessionData
	if err := json.Unmarshal(raw, &data); err != nil {
		return SessionData{}, err
	}
	return data, nil
}

// Delete 删除会话（登出）。删除不存在的 token 不视为错误。
func (m *Manager) Delete(ctx context.Context, token string) error {
	return m.rdb.Del(ctx, keyPrefix+token).Err()
}
