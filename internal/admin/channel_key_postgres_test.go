package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"linapi/internal/db"
)

type channelStoreQuerier struct {
	db.Querier
	createFn func(context.Context, db.CreateChannelParams) (db.Channel, error)
	getFn    func(context.Context, string) (db.Channel, error)
	updateFn func(context.Context, db.UpdateChannelParams) (db.Channel, error)
}

func (q *channelStoreQuerier) CreateChannel(ctx context.Context, arg db.CreateChannelParams) (db.Channel, error) {
	return q.createFn(ctx, arg)
}

func (q *channelStoreQuerier) GetChannel(ctx context.Context, channelID string) (db.Channel, error) {
	return q.getFn(ctx, channelID)
}

func (q *channelStoreQuerier) UpdateChannel(ctx context.Context, arg db.UpdateChannelParams) (db.Channel, error) {
	return q.updateFn(ctx, arg)
}

func dbChannelFromCreate(arg db.CreateChannelParams) db.Channel {
	return db.Channel{
		ChannelID: arg.ChannelID,
		Name:      arg.Name,
		Format:    arg.Format,
		BaseURL:   arg.BaseURL,
		ApiKey:    arg.ApiKey,
		Models:    arg.Models,
		Priority:  arg.Priority,
		Weight:    arg.Weight,
		Enabled:   arg.Enabled,
	}
}

func TestPGStoreEncryptsChannelKeyBeforeWriteAndDecryptsRead(t *testing.T) {
	cipher := testChannelKeyCipher(t, "primary", 0x55)
	var stored string
	q := &channelStoreQuerier{
		createFn: func(_ context.Context, arg db.CreateChannelParams) (db.Channel, error) {
			stored = arg.ApiKey
			return dbChannelFromCreate(arg), nil
		},
	}
	s := NewPGStore(q, cipher)
	created, err := s.CreateChannel(context.Background(), ChannelInput{
		ChannelID: "c1", Format: "openai", BaseURL: "https://up.example",
		APIKey: "sk-upstream-secret", Models: map[string]string{"gpt-4o": ""},
		Weight: 1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored == "sk-upstream-secret" || strings.Contains(stored, "sk-upstream-secret") {
		t.Fatal("PG 写参数不得包含渠道明文密钥")
	}
	if !strings.HasPrefix(stored, channelKeyEnvelopeV1) {
		t.Fatalf("PG 写参数不是受支持的 envelope: %q", stored)
	}
	if created.APIKey != "sk-upstream-secret" {
		t.Fatalf("存储内部读取应解密供路由使用: %q", created.APIKey)
	}
}

func TestPGStoreChannelUpdateCanPreserveOrReplaceSecret(t *testing.T) {
	cipher := testChannelKeyCipher(t, "primary", 0x66)
	oldEnvelope, err := cipher.Encrypt("c1", "old-secret")
	if err != nil {
		t.Fatal(err)
	}
	q := &channelStoreQuerier{
		updateFn: func(_ context.Context, arg db.UpdateChannelParams) (db.Channel, error) {
			if !arg.ApiKeySet {
				arg.ApiKey = oldEnvelope
			}
			return db.Channel{
				ChannelID: arg.ChannelID, Format: arg.Format, BaseURL: arg.BaseURL,
				ApiKey: arg.ApiKey, Models: arg.Models, Weight: arg.Weight, Enabled: arg.Enabled,
			}, nil
		},
	}
	s := NewPGStore(q, cipher)
	preserved, err := s.UpdateChannel(context.Background(), ChannelInput{
		ChannelID: "c1", Format: "openai", BaseURL: "https://up.example", Weight: 1,
	})
	if err != nil || preserved.APIKey != "old-secret" {
		t.Fatalf("省略 api_key 应保留旧值: got=%q err=%v", preserved.APIKey, err)
	}
	replaced, err := s.UpdateChannel(context.Background(), ChannelInput{
		ChannelID: "c1", Format: "openai", BaseURL: "https://up.example",
		APIKey: "new-secret", APIKeySet: true, Weight: 1,
	})
	if err != nil || replaced.APIKey != "new-secret" {
		t.Fatalf("显式 api_key 应写入新密文: got=%q err=%v", replaced.APIKey, err)
	}
}

func TestPGStoreChannelOperationsFailClosedWithoutCipherOrWithWrongAAD(t *testing.T) {
	q := &channelStoreQuerier{
		createFn: func(_ context.Context, arg db.CreateChannelParams) (db.Channel, error) {
			return dbChannelFromCreate(arg), nil
		},
	}
	if _, err := NewPGStore(q).CreateChannel(context.Background(), ChannelInput{ChannelID: "c1"}); !errors.Is(err, ErrChannelKeyEncryptionRequired) {
		t.Fatalf("无加密器时 PostgreSQL 渠道写入必须失败: %v", err)
	}

	cipher := testChannelKeyCipher(t, "primary", 0x77)
	envelope, err := cipher.Encrypt("c1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	q.getFn = func(_ context.Context, _ string) (db.Channel, error) {
		return db.Channel{ChannelID: "c2", ApiKey: envelope, Models: []byte("{}")}, nil
	}
	if _, err := NewPGStore(q, cipher).GetChannel(context.Background(), "c2"); !errors.Is(err, ErrInvalidChannelKeyEnvelope) {
		t.Fatalf("被复制到其他 channel_id 的数据库密文必须失败: %v", err)
	}
}
