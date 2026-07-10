package store

import (
	"context"
	"errors"
	"testing"
)

func newTestStore() *MemoryStore {
	return NewMemoryStore([]KeySeed{
		{
			APIKey: "sk-alice", KeyID: "key-a", UserID: "alice",
			RateLimitPerMin: 60, AllowedModels: []string{"gpt-4o"},
			Enabled: true, InitialBalance: 1000,
		},
		{
			APIKey: "sk-bob", KeyID: "key-b", UserID: "bob",
			Enabled: true, InitialBalance: 0,
		},
		{
			APIKey: "sk-disabled", KeyID: "key-d", UserID: "dan",
			Enabled: false, InitialBalance: 500,
		},
	})
}

func TestResolveKey(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()

	id, err := s.ResolveKey(ctx, "sk-alice")
	if err != nil {
		t.Fatalf("解析有效密钥应成功, 得到 %v", err)
	}
	if id.UserID != "alice" || id.KeyID != "key-a" {
		t.Errorf("身份字段不符: %+v", id)
	}
	if id.RateLimitPerMin != 60 {
		t.Errorf("限流值应为 60, 得到 %d", id.RateLimitPerMin)
	}
}

func TestResolveKeyNotFound(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()

	if _, err := s.ResolveKey(ctx, "sk-nope"); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("未知密钥应返回 ErrKeyNotFound, 得到 %v", err)
	}
	// 已禁用的密钥同样视为不可用。
	if _, err := s.ResolveKey(ctx, "sk-disabled"); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("已禁用密钥应返回 ErrKeyNotFound, 得到 %v", err)
	}
}

func TestResolveKeyReturnsCopy(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()

	id1, _ := s.ResolveKey(ctx, "sk-alice")
	id1.UserID = "mutated"

	id2, _ := s.ResolveKey(ctx, "sk-alice")
	if id2.UserID != "alice" {
		t.Errorf("返回的身份应为副本，内部状态不应被外部修改; 得到 %q", id2.UserID)
	}
}

func TestIdentityAllows(t *testing.T) {
	limited := &Identity{AllowedModels: []string{"gpt-4o", "gpt-4o-mini"}}
	if !limited.Allows("gpt-4o") {
		t.Error("允许列表内的模型应放行")
	}
	if limited.Allows("claude-3") {
		t.Error("允许列表外的模型应拒绝")
	}

	// 空列表表示不限制。
	open := &Identity{}
	if !open.Allows("any-model") {
		t.Error("空 AllowedModels 应放行任意模型")
	}
}

func TestBalance(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()

	if b, _ := s.Balance(ctx, "alice"); b != 1000 {
		t.Errorf("alice 余额应为 1000, 得到 %d", b)
	}
	if b, _ := s.Balance(ctx, "bob"); b != 0 {
		t.Errorf("bob 余额应为 0, 得到 %d", b)
	}
	// 未知用户余额为 0，不报错。
	if b, _ := s.Balance(ctx, "ghost"); b != 0 {
		t.Errorf("未知用户余额应为 0, 得到 %d", b)
	}
}

func TestAddBalance(t *testing.T) {
	s := newTestStore()

	if got := s.AddBalance("alice", -300); got != 700 {
		t.Errorf("扣费后余额应为 700, 得到 %d", got)
	}
	if got := s.AddBalance("alice", 50); got != 750 {
		t.Errorf("充值后余额应为 750, 得到 %d", got)
	}
}
