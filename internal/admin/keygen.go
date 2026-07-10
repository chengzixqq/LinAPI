package admin

import (
	"crypto/rand"
	"encoding/hex"
)

// GeneratedKey 是新生成的密钥材料：明文只在创建响应里回显一次，绝不落库。
type GeneratedKey struct {
	APIKey string // 明文，形如 "sk-<hex>"，仅本次响应可见
	KeyID  string // 稳定标识，用作限流维度与计费归因
}

// GenerateKey 生成一对（明文 API Key, KeyID）。
// 明文 32 字节熵，KeyID 8 字节熵，均以 hex 编码；失败返回 error。
func GenerateKey() (GeneratedKey, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return GeneratedKey{}, err
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return GeneratedKey{}, err
	}
	return GeneratedKey{
		APIKey: "sk-" + hex.EncodeToString(secret),
		KeyID:  "key-" + hex.EncodeToString(idBytes),
	}, nil
}
