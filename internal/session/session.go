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
	// CSRFToken 与会话绑定，用于双重提交 CSRF 防护（审查 AUD-P1-26）：登录时由
	// handler 生成并存入，写请求校验 X-CSRF-Token header 是否等于此值，登出删会话即失效。
	CSRFToken string `json:"csrf_token"`
	// SessionVersion 是登录时刻的账户会话代次快照，用于会话撤销（审查 AUD-P1-17）：
	// 账户被禁用或改密时代次在账户库递增，鉴权时若此快照与账户当前代次不一致，
	// 则判定为已撤销的陈旧会话并拒绝。默认 0（新账户初始代次），旧会话反序列化亦得 0，
	// 与初始代次一致，故升级部署不会误踢既有登录态。
	SessionVersion int `json:"session_version"`
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
	token, err := randomHex(32)
	if err != nil {
		return "", fmt.Errorf("生成会话 token 失败: %w", err)
	}

	payload, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	if err := m.rdb.Set(ctx, keyPrefix+token, payload, ttl).Err(); err != nil {
		return "", fmt.Errorf("写入会话失败: %w", err)
	}
	return token, nil
}

// NewCSRFToken 生成一枚随机 CSRF token（32 字节 hex），供登录时存入会话并下发给前端。
// 与会话 token 同强度的密码学随机源（审查 AUD-P1-26）。
func NewCSRFToken() (string, error) {
	return randomHex(32)
}

// randomHex 返回 n 字节密码学随机数的 hex 编码（长度 2n）。
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
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
