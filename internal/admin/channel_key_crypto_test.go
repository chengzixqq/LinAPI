package admin

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func testChannelKeyCipher(t *testing.T, keyID string, fill byte) *ChannelKeyCipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill
	}
	c, err := NewChannelKeyCipher(keyID, base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestChannelKeyCipherRoundTripUsesRandomNonceAndAAD(t *testing.T) {
	c := testChannelKeyCipher(t, "primary-1", 0x42)
	first, err := c.Encrypt("channel-a", "sk-upstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Encrypt("channel-a", "sk-upstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("相同明文必须因随机 nonce 产生不同密文")
	}
	if strings.Contains(first, "sk-upstream-secret") || !strings.HasPrefix(first, channelKeyEnvelopeV1) {
		t.Fatalf("envelope 格式或保密性不正确: %q", first)
	}
	got, err := c.Decrypt("channel-a", first)
	if err != nil || got != "sk-upstream-secret" {
		t.Fatalf("解密结果不符: got=%q err=%v", got, err)
	}
	if _, err := c.Decrypt("channel-b", first); !errors.Is(err, ErrInvalidChannelKeyEnvelope) {
		t.Fatalf("跨 channel_id 复制密文必须因 AAD 校验失败: %v", err)
	}
}

func TestChannelKeyCipherRejectsInvalidConfigAndTampering(t *testing.T) {
	if _, err := NewChannelKeyCipher("", ""); err == nil {
		t.Fatal("缺失 key id/key 必须失败")
	}
	if _, err := NewChannelKeyCipher("bad:id", base64.StdEncoding.EncodeToString(make([]byte, 32))); err == nil {
		t.Fatal("含分隔符的 key id 必须失败")
	}
	if _, err := NewChannelKeyCipher("valid", base64.StdEncoding.EncodeToString(make([]byte, 31))); err == nil {
		t.Fatal("非 32 字节主密钥必须失败")
	}

	c := testChannelKeyCipher(t, "primary-1", 0x11)
	envelope, err := c.Encrypt("channel-a", "secret")
	if err != nil {
		t.Fatal(err)
	}
	separator := strings.LastIndexByte(envelope, ':')
	payload, err := base64.RawURLEncoding.DecodeString(envelope[separator+1:])
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)/2] ^= 0x01
	tampered := envelope[:separator+1] + base64.RawURLEncoding.EncodeToString(payload)
	if _, err := c.Decrypt("channel-a", tampered); !errors.Is(err, ErrInvalidChannelKeyEnvelope) {
		t.Fatalf("篡改密文必须失败: %v", err)
	}
	other := testChannelKeyCipher(t, "primary-2", 0x11)
	if _, err := other.Decrypt("channel-a", envelope); !errors.Is(err, ErrInvalidChannelKeyEnvelope) {
		t.Fatalf("未配置的 key id 必须失败: %v", err)
	}
}
