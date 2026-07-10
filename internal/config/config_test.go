package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingExplicitConfigUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("缺失的可选配置不应报错: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Fatalf("server.port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.MetricsMaxRequestsInFlight != 2 || cfg.Server.MetricsTimeoutSeconds != 10 {
		t.Fatalf("metrics 抓取预算默认值异常: %+v", cfg.Server)
	}
	if cfg.Admin.AuthRateLimitPerMin != 30 || cfg.Admin.AuthIdentifierRateLimitPerMin != 20 {
		t.Fatalf("认证双维限流默认值异常: %+v", cfg.Admin)
	}
}

func TestLoadChannelKeyEncryptionFromEnvironment(t *testing.T) {
	t.Setenv("LINAPI_DATABASE_CHANNEL_KEY_ENCRYPTION_KEY_ID", "primary-2026")
	t.Setenv("LINAPI_DATABASE_CHANNEL_KEY_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("LINAPI_DATABASE_CHANNEL_KEY_ENCRYPTION_MIGRATE_PLAINTEXT", "true")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	enc := cfg.Database.ChannelKeyEncryption
	if enc.KeyID != "primary-2026" || enc.Key == "" || !enc.MigratePlaintext {
		t.Fatalf("渠道密钥环境变量未完整覆盖配置: %+v", enc)
	}
}

func TestLoadMalformedConfigStillFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(path, []byte("server: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("语法错误的配置必须失败")
	}
}
