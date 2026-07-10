package main

import (
	"encoding/base64"
	"testing"

	"linapi/internal/config"
)

func TestChannelKeyCipherForConfigFailsClosedWhenDatabaseEnabled(t *testing.T) {
	if cipher, err := channelKeyCipherForConfig(&config.Config{}); err != nil || cipher != nil {
		t.Fatalf("内存模式不应要求 PostgreSQL 渠道主密钥: cipher=%v err=%v", cipher, err)
	}

	cfg := &config.Config{Database: config.DatabaseConfig{Enabled: true}}
	if _, err := channelKeyCipherForConfig(cfg); err == nil {
		t.Fatal("database.enabled=true 缺少渠道主密钥必须阻止启动")
	}
	cfg.Database.ChannelKeyEncryption = config.ChannelKeyEncryptionConfig{
		KeyID: "primary",
		Key:   base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	if _, err := channelKeyCipherForConfig(cfg); err != nil {
		t.Fatalf("合法 AES-256 主密钥应通过启动校验: %v", err)
	}
}
