package admin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	channelKeyEnvelopePrefix = "linapi:channel-key:"
	channelKeyEnvelopeV1     = channelKeyEnvelopePrefix + "v1:"
	maxChannelAPIKeyBytes    = 64 * 1024
)

var (
	// ErrChannelKeyEncryptionRequired 表示 PostgreSQL 渠道操作没有配置密钥加密器。
	ErrChannelKeyEncryptionRequired = errors.New("admin: PostgreSQL 渠道密钥加密器未配置")
	// ErrInvalidChannelKeyEnvelope 表示密文损坏、版本未知、key id 不匹配或 AAD 校验失败。
	ErrInvalidChannelKeyEnvelope = errors.New("admin: 渠道密钥密文无效")
)

// ChannelKeyCipher 使用 AES-256-GCM 加密上游渠道密钥。
//
// envelope 格式为：linapi:channel-key:v1:<base64url(key-id)>:<base64url(nonce+ciphertext)>。
// channel_id 作为 AAD 参与认证，数据库中的密文不能在渠道之间复制复用。
type ChannelKeyCipher struct {
	keyID string
	aead  cipher.AEAD
	rand  io.Reader
}

// NewChannelKeyCipher 从外部注入的 base64 主密钥构造加密器。主密钥必须恰好 32 字节。
func NewChannelKeyCipher(keyID, encodedKey string) (*ChannelKeyCipher, error) {
	if !validChannelKeyID(keyID) {
		return nil, fmt.Errorf("渠道密钥 key_id 必须为 1-64 位字母、数字、点、下划线或短横线")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("渠道密钥主密钥必须是 base64 编码的 32 字节随机值")
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化渠道密钥 AES-256 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化渠道密钥 GCM 失败: %w", err)
	}
	return &ChannelKeyCipher{keyID: keyID, aead: aead, rand: rand.Reader}, nil
}

// Encrypt 生成带随机 nonce 的 v1 envelope。相同明文每次都会得到不同密文。
func (c *ChannelKeyCipher) Encrypt(channelID, plaintext string) (string, error) {
	if c == nil || c.aead == nil {
		return "", ErrChannelKeyEncryptionRequired
	}
	if channelID == "" || len(plaintext) > maxChannelAPIKeyBytes {
		return "", ErrInvalidInput
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(c.rand, nonce); err != nil {
		return "", fmt.Errorf("生成渠道密钥 nonce 失败: %w", err)
	}
	sealed := c.aead.Seal(nil, nonce, []byte(plaintext), channelKeyAAD(channelID))
	payload := append(nonce, sealed...)
	return channelKeyEnvelopeV1 +
		base64.RawURLEncoding.EncodeToString([]byte(c.keyID)) + ":" +
		base64.RawURLEncoding.EncodeToString(payload), nil
}

// Decrypt 验证 envelope、key id 与 channel_id AAD 后返回内存中的上游密钥。
func (c *ChannelKeyCipher) Decrypt(channelID, envelope string) (string, error) {
	if c == nil || c.aead == nil {
		return "", ErrChannelKeyEncryptionRequired
	}
	if channelID == "" || len(envelope) > 2*maxChannelAPIKeyBytes {
		return "", ErrInvalidChannelKeyEnvelope
	}
	parts := strings.Split(envelope, ":")
	if len(parts) != 5 || strings.Join(parts[:3], ":")+":" != channelKeyEnvelopeV1 {
		return "", ErrInvalidChannelKeyEnvelope
	}
	keyID, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || string(keyID) != c.keyID {
		return "", ErrInvalidChannelKeyEnvelope
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil || len(payload) < c.aead.NonceSize()+c.aead.Overhead() {
		return "", ErrInvalidChannelKeyEnvelope
	}
	nonce := payload[:c.aead.NonceSize()]
	ciphertext := payload[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, channelKeyAAD(channelID))
	if err != nil || len(plaintext) > maxChannelAPIKeyBytes {
		return "", ErrInvalidChannelKeyEnvelope
	}
	return string(plaintext), nil
}

func isChannelKeyEnvelope(value string) bool {
	return strings.HasPrefix(value, channelKeyEnvelopePrefix)
}

func channelKeyAAD(channelID string) []byte {
	return []byte("linapi/channel-key/v1\x00" + channelID)
}

func validChannelKeyID(keyID string) bool {
	if len(keyID) == 0 || len(keyID) > 64 {
		return false
	}
	for _, r := range keyID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
