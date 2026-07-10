package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"

	"linapi/internal/db"
)

// HashAPIKey 计算 API Key 的 SHA-256 十六进制摘要。
// 库中只存摘要不存明文；鉴权时对客户端传入的明文 Key 做同样哈希再比对。
// 导出以便建密钥（写库）与解析（读库）两侧共用同一算法。
func HashAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

// PGStore 是 Store 接口的 PostgreSQL 实现，通过 sqlc 生成的类型安全查询访问库。
// 替换 MemoryStore 后中间件层零改动（依赖的是 Store 接口）。并发安全（底层 pgxpool 并发安全）。
type PGStore struct {
	q db.Querier
}

// NewPGStore 用一个 sqlc 查询器构造 PGStore。
func NewPGStore(q db.Querier) *PGStore {
	return &PGStore{q: q}
}

// ResolveKey 实现 Store：对明文 Key 取摘要后联表解析身份。
// 密钥或用户任一禁用/不存在都查不到，统一返回 ErrKeyNotFound。
func (s *PGStore) ResolveKey(ctx context.Context, apiKey string) (*Identity, error) {
	row, err := s.q.ResolveAPIKey(ctx, HashAPIKey(apiKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrKeyNotFound
		}
		return nil, err
	}
	return &Identity{
		KeyID:           row.KeyID,
		UserID:          row.UserExternalID,
		RateLimitPerMin: int(row.RateLimitPerMin),
		AllowedModels:   row.AllowedModels,
		Enabled:         true, // 查询已过滤 enabled=TRUE，能查到即启用。
	}, nil
}

// Balance 实现 Store：读用户权威余额（冷源）。
// 用户不存在或禁用时查不到，按 0 余额返回（额度闸门自然拦截），不视作错误。
func (s *PGStore) Balance(ctx context.Context, userID string) (int64, error) {
	bal, err := s.q.GetBalance(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return bal, nil
}
