package admin

import (
	"strings"
	"testing"
)

// TestGenerateKeyFormat 验证生成的密钥前缀与非空。
func TestGenerateKeyFormat(t *testing.T) {
	g, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey 失败: %v", err)
	}
	if !strings.HasPrefix(g.APIKey, "sk-") {
		t.Errorf("APIKey 应以 sk- 开头: %q", g.APIKey)
	}
	if !strings.HasPrefix(g.KeyID, "key-") {
		t.Errorf("KeyID 应以 key- 开头: %q", g.KeyID)
	}
	// 32 字节 hex = 64 字符 + "sk-" 前缀。
	if len(g.APIKey) != len("sk-")+64 {
		t.Errorf("APIKey 长度不符: %d", len(g.APIKey))
	}
}

// TestGenerateKeyUnique 验证多次生成不重复（熵足够）。
func TestGenerateKeyUnique(t *testing.T) {
	seen := make(map[string]bool)
	seenID := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		g, err := GenerateKey()
		if err != nil {
			t.Fatalf("第 %d 次 GenerateKey 失败: %v", i, err)
		}
		if seen[g.APIKey] {
			t.Fatalf("APIKey 重复: %q", g.APIKey)
		}
		if seenID[g.KeyID] {
			t.Fatalf("KeyID 重复: %q", g.KeyID)
		}
		seen[g.APIKey] = true
		seenID[g.KeyID] = true
	}
}
