package account

import (
	"context"
	"errors"
	"testing"

	"linapi/internal/store"
)

func newMemStore() *MemoryStore {
	return NewMemoryStore(store.NewMemoryStore(nil))
}

func TestMemoryCreateUserAccountBindsBilling(t *testing.T) {
	base := store.NewMemoryStore(nil)
	m := NewMemoryStore(base)
	ctx := context.Background()

	acc, err := m.CreateUserAccount(ctx, "alice", "hash1", 500)
	if err != nil {
		t.Fatalf("CreateUserAccount 失败: %v", err)
	}
	if acc.Role != RoleUser || acc.ExternalID == "" {
		t.Fatalf("user 账户应有角色与 external_id: %+v", acc)
	}
	// 计费实体应已建：底层 store 能查到余额 500。
	bal, _ := base.Balance(ctx, acc.ExternalID)
	if bal != 500 {
		t.Errorf("计费实体余额应为 500, 得到 %d", bal)
	}

	// 用户名重复应 ErrConflict。
	if _, err := m.CreateUserAccount(ctx, "alice", "hash2", 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复用户名应 ErrConflict, 得到 %v", err)
	}
}

func TestMemoryGetCredentials(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	if _, err := m.CreateUserAccount(ctx, "bob", "bcrypt-hash", 0); err != nil {
		t.Fatalf("建账户失败: %v", err)
	}
	cred, err := m.GetCredentials(ctx, "bob")
	if err != nil {
		t.Fatalf("GetCredentials 失败: %v", err)
	}
	if cred.PasswordHash != "bcrypt-hash" {
		t.Errorf("密码哈希不符: %q", cred.PasswordHash)
	}
	if _, err := m.GetCredentials(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("查不存在账户应 ErrNotFound, 得到 %v", err)
	}
}

func TestMemoryCreateAdminAccount(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	acc, err := m.CreateAccount(ctx, CreateAccountInput{
		Username: "root", PasswordHash: "h", Role: RoleAdmin,
	})
	if err != nil {
		t.Fatalf("建 admin 账户失败: %v", err)
	}
	if acc.Role != RoleAdmin || acc.ExternalID != "" {
		t.Fatalf("admin 账户不应有 external_id: %+v", acc)
	}
	if _, err := m.CreateAccount(ctx, CreateAccountInput{Username: "x", Role: "bogus"}); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("非法角色应 ErrInvalidRole, 得到 %v", err)
	}
}

func TestMemorySetEnabledAndPassword(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	acc, _ := m.CreateUserAccount(ctx, "carol", "h", 0)

	off, err := m.SetEnabled(ctx, acc.ID, false)
	if err != nil || off.Enabled {
		t.Fatalf("SetEnabled(false) 失败: %+v err=%v", off, err)
	}
	if err := m.UpdatePassword(ctx, acc.ID, "newhash"); err != nil {
		t.Fatalf("UpdatePassword 失败: %v", err)
	}
	cred, _ := m.GetCredentials(ctx, "carol")
	if cred.PasswordHash != "newhash" {
		t.Errorf("改密未生效: %q", cred.PasswordHash)
	}
	if _, err := m.SetEnabled(ctx, 99999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("启停不存在账户应 ErrNotFound, 得到 %v", err)
	}
}

// TestMemorySessionVersionBumpsOnDisableAndPassword 验证禁用账户与重置密码都会递增
// session_version（审查 AUD-P1-17）：登录会话把 version 快照进 token，鉴权时比对，
// version 一变旧会话即失效。重新启用（SetEnabled(true)）不应递增——无需踢已在线会话。
func TestMemorySessionVersionBumpsOnDisableAndPassword(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	acc, _ := m.CreateUserAccount(ctx, "ivan", "h", 0)
	if acc.SessionVersion != 0 {
		t.Fatalf("新账户 session_version 应为 0, 得到 %d", acc.SessionVersion)
	}

	// 禁用 → 递增。
	off, err := m.SetEnabled(ctx, acc.ID, false)
	if err != nil {
		t.Fatalf("SetEnabled(false) 失败: %v", err)
	}
	if off.SessionVersion != 1 {
		t.Fatalf("禁用后 session_version 应为 1, 得到 %d", off.SessionVersion)
	}

	// 改密 → 再递增。
	if err := m.UpdatePassword(ctx, acc.ID, "newhash"); err != nil {
		t.Fatalf("UpdatePassword 失败: %v", err)
	}
	cur, _ := m.GetByID(ctx, acc.ID)
	if cur.SessionVersion != 2 {
		t.Fatalf("改密后 session_version 应为 2, 得到 %d", cur.SessionVersion)
	}

	// 重新启用 → 不递增（无需踢已在线会话）。
	on, err := m.SetEnabled(ctx, acc.ID, true)
	if err != nil {
		t.Fatalf("SetEnabled(true) 失败: %v", err)
	}
	if on.SessionVersion != 2 {
		t.Fatalf("重新启用不应递增 session_version, 应为 2, 得到 %d", on.SessionVersion)
	}
}

func TestMemorySettings(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()

	// 默认值：注册关闭、初始额度 0。
	s, err := m.Get(ctx)
	if err != nil {
		t.Fatalf("Get 设置失败: %v", err)
	}
	if s.RegistrationEnabled || s.NewUserInitialBalance != 0 {
		t.Fatalf("默认设置应为 关闭/0, 得到 %+v", s)
	}

	// 写入后读回。
	if err := m.Put(ctx, Settings{RegistrationEnabled: true, NewUserInitialBalance: 1000}); err != nil {
		t.Fatalf("Put 设置失败: %v", err)
	}
	s, _ = m.Get(ctx)
	if !s.RegistrationEnabled || s.NewUserInitialBalance != 1000 {
		t.Fatalf("设置未持久化: %+v", s)
	}
}
