package admin

import (
	"context"
	"errors"
	"testing"

	"linapi/internal/store"
)

// newMemStore 构建一个空的 admin.MemoryStore（不预置渠道）。
func newMemStore() *MemoryStore {
	return NewMemoryStore(store.NewMemoryStore(nil), nil)
}

// ---- 用户 ----

func TestMemoryUserCRUD(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()

	u, err := m.CreateUser(ctx, CreateUserInput{ExternalID: "u1", Balance: 500, Enabled: true})
	if err != nil {
		t.Fatalf("CreateUser 失败: %v", err)
	}
	if u.ExternalID != "u1" || u.Balance != 500 || !u.Enabled {
		t.Fatalf("创建用户字段不符: %+v", u)
	}

	// 重复 external_id -> ErrConflict。
	if _, err := m.CreateUser(ctx, CreateUserInput{ExternalID: "u1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复用户应 ErrConflict, 得到 %v", err)
	}

	got, err := m.GetUser(ctx, "u1")
	if err != nil || got.ExternalID != "u1" {
		t.Fatalf("GetUser 失败: %+v err=%v", got, err)
	}

	// 不存在 -> ErrNotFound。
	if _, err := m.GetUser(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("查不存在用户应 ErrNotFound, 得到 %v", err)
	}

	// 启停。
	off, err := m.SetUserEnabled(ctx, "u1", false)
	if err != nil || off.Enabled {
		t.Fatalf("SetUserEnabled(false) 失败: %+v err=%v", off, err)
	}

	// 充值/扣减。
	bal, err := m.AddBalance(ctx, "u1", 250)
	if err != nil || bal != 750 {
		t.Fatalf("AddBalance +250 期望 750, 得到 %d err=%v", bal, err)
	}
	bal, err = m.AddBalance(ctx, "u1", -100)
	if err != nil || bal != 650 {
		t.Fatalf("AddBalance -100 期望 650, 得到 %d err=%v", bal, err)
	}
	if _, err := m.AddBalance(ctx, "ghost", 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("给不存在用户充值应 ErrNotFound, 得到 %v", err)
	}
}

func TestMemoryListUsers(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if _, err := m.CreateUser(ctx, CreateUserInput{ExternalID: id, Enabled: true}); err != nil {
			t.Fatalf("准备用户 %s 失败: %v", id, err)
		}
	}
	users, err := m.ListUsers(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListUsers 失败: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("期望 3 个用户, 得到 %d", len(users))
	}

	// 分页：limit=2 应截断。
	page, err := m.ListUsers(ctx, 2, 0)
	if err != nil || len(page) != 2 {
		t.Fatalf("limit=2 期望 2 条, 得到 %d err=%v", len(page), err)
	}
}

// ---- 密钥 ----

func TestMemoryAPIKeyCRUD(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()

	// 给不存在的用户建密钥 -> ErrNotFound。
	_, err := m.CreateAPIKey(ctx, CreateAPIKeyInput{APIKey: "sk-x", KeyID: "k1", UserID: "u1"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("无用户建密钥应 ErrNotFound, 得到 %v", err)
	}

	if _, err := m.CreateUser(ctx, CreateUserInput{ExternalID: "u1", Enabled: true}); err != nil {
		t.Fatalf("准备用户失败: %v", err)
	}

	k, err := m.CreateAPIKey(ctx, CreateAPIKeyInput{
		APIKey: "sk-x", KeyID: "k1", UserID: "u1",
		RateLimitPerMin: 60, AllowedModels: []string{"gpt-4o"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateAPIKey 失败: %v", err)
	}
	if k.KeyID != "k1" || k.UserID != "u1" || k.RateLimitPerMin != 60 {
		t.Fatalf("密钥字段不符: %+v", k)
	}

	// 重复 key_id -> ErrConflict。
	_, err = m.CreateAPIKey(ctx, CreateAPIKeyInput{APIKey: "sk-y", KeyID: "k1", UserID: "u1"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("重复 key_id 应 ErrConflict, 得到 %v", err)
	}

	keys, err := m.ListAPIKeysByUser(ctx, "u1")
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListAPIKeysByUser 期望 1 条, 得到 %d err=%v", len(keys), err)
	}

	// 启停。
	off, err := m.SetAPIKeyEnabled(ctx, "k1", false)
	if err != nil || off.Enabled {
		t.Fatalf("SetAPIKeyEnabled(false) 失败: %+v err=%v", off, err)
	}
	if _, err := m.SetAPIKeyEnabled(ctx, "ghost", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("启停不存在密钥应 ErrNotFound, 得到 %v", err)
	}
}

// TestMemoryKeyVisibleToHotPath 验证管理面创建的密钥能被热路径 store.Store 即时读到
// （共享同一份底层数据，这是内存模式的关键契约）。
func TestMemoryKeyVisibleToHotPath(t *testing.T) {
	base := store.NewMemoryStore(nil)
	m := NewMemoryStore(base, nil)
	ctx := context.Background()

	if _, err := m.CreateUser(ctx, CreateUserInput{ExternalID: "u1", Balance: 1000, Enabled: true}); err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	if _, err := m.CreateAPIKey(ctx, CreateAPIKeyInput{
		APIKey: "sk-live", KeyID: "k1", UserID: "u1", Enabled: true,
	}); err != nil {
		t.Fatalf("建密钥失败: %v", err)
	}

	// 热路径应能用明文 key 解析出身份。
	id, err := base.ResolveKey(ctx, "sk-live")
	if err != nil {
		t.Fatalf("热路径 ResolveKey 失败: %v", err)
	}
	if id.KeyID != "k1" || id.UserID != "u1" {
		t.Fatalf("热路径身份不符: %+v", id)
	}

	// 管理面禁用密钥后，热路径应立即拒绝。
	if _, err := m.SetAPIKeyEnabled(ctx, "k1", false); err != nil {
		t.Fatalf("禁用密钥失败: %v", err)
	}
	if _, err := base.ResolveKey(ctx, "sk-live"); !errors.Is(err, store.ErrKeyNotFound) {
		t.Fatalf("禁用后热路径应 ErrKeyNotFound, 得到 %v", err)
	}
}
