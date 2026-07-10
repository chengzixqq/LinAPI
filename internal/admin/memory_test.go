package admin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

	// 删除密钥。
	if err := m.DeleteAPIKey(ctx, "k1"); err != nil {
		t.Fatalf("DeleteAPIKey 失败: %v", err)
	}
	if keys, _ := m.ListAPIKeysByUser(ctx, "u1"); len(keys) != 0 {
		t.Fatalf("删除后应无密钥, 得到 %d 个", len(keys))
	}
	if err := m.DeleteAPIKey(ctx, "k1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除不存在密钥应 ErrNotFound, 得到 %v", err)
	}
}

func TestMemoryStoreRejectsUnsafeRateLimit(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	if _, err := m.CreateUser(ctx, CreateUserInput{ExternalID: "u", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, err := m.CreateAPIKey(ctx, CreateAPIKeyInput{
		APIKey: "sk", KeyID: "k", UserID: "u", RateLimitPerMin: MaxRateLimitPerMin + 1,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("超大限流值应被领域层拒绝，得到 %v", err)
	}
}

func TestMemoryStoreKeyLimitIsAtomic(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	if _, err := m.CreateUser(ctx, CreateUserInput{ExternalID: "limited", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := m.CreateAPIKeyLimited(ctx, CreateAPIKeyInput{
				APIKey: fmt.Sprintf("sk-%d", i), KeyID: fmt.Sprintf("k-%d", i),
				UserID: "limited", RateLimitPerMin: 60, Enabled: true,
			}, 3)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrLimitReached) {
				t.Errorf("并发创建返回意外错误: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := successes.Load(); got != 3 {
		t.Fatalf("并发创建成功数=%d, want 3", got)
	}
}

func TestMemoryAPIKeyCreatedAtPersistsAcrossListAndToggle(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	if _, err := m.CreateUser(ctx, CreateUserInput{ExternalID: "u1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	created, err := m.CreateAPIKey(ctx, CreateAPIKeyInput{
		APIKey: "sk-created", KeyID: "k-created", UserID: "u1", Enabled: true,
	})
	if err != nil || created.CreatedAt.IsZero() {
		t.Fatalf("创建密钥必须返回非零 created_at: %+v err=%v", created, err)
	}
	listed, err := m.ListAPIKeysByUser(ctx, "u1")
	if err != nil || len(listed) != 1 || !listed[0].CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("列表必须保留 created_at: %+v err=%v", listed, err)
	}
	toggled, err := m.SetAPIKeyEnabled(ctx, "k-created", false)
	if err != nil || !toggled.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("启停密钥不得丢失 created_at: %+v err=%v", toggled, err)
	}
}

func TestChannelInputRejectsOutOfRangePriorityAndWeight(t *testing.T) {
	m := newMemStore()
	base := ChannelInput{
		ChannelID: "c1", Name: "channel", Format: "openai", BaseURL: "https://up.example",
		APIKey: "sk-test", Models: map[string]string{"gpt-4o": ""}, Weight: 1,
	}
	for name, mutate := range map[string]func(*ChannelInput){
		"priority too large": func(in *ChannelInput) { in.Priority = MaxChannelPriority + 1 },
		"priority too small": func(in *ChannelInput) { in.Priority = MinChannelPriority - 1 },
		"weight negative":    func(in *ChannelInput) { in.Weight = -1 },
		"weight too large":   func(in *ChannelInput) { in.Weight = MaxChannelWeight + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			in := base
			mutate(&in)
			if _, err := m.CreateChannel(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("CreateChannel 应拒绝越界数值，得到 %v", err)
			}
		})
	}
	base.ChannelID = "c-default"
	base.Weight = 0
	created, err := m.CreateChannel(context.Background(), base)
	if err != nil || created.Weight != 1 {
		t.Fatalf("缺失 weight 应归一为 1: %+v err=%v", created, err)
	}
}

// TestMemoryDeleteKeyRevokesResolve 验证删除密钥后，热路径再也无法用明文 key 解析出身份
// ——双索引（keyByID + 明文 keys）必须同步清除，否则被删的 key 仍能通过 ResolveKey 通过鉴权（越权漏洞）。
func TestMemoryDeleteKeyRevokesResolve(t *testing.T) {
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

	// 删除前：热路径可解析。
	if _, err := base.ResolveKey(ctx, "sk-live"); err != nil {
		t.Fatalf("删除前热路径应可解析: %v", err)
	}

	// 删除后：明文 key 必须再也解析不出身份（明文索引被同步清除）。
	if err := m.DeleteAPIKey(ctx, "k1"); err != nil {
		t.Fatalf("DeleteAPIKey 失败: %v", err)
	}
	if _, err := base.ResolveKey(ctx, "sk-live"); !errors.Is(err, store.ErrKeyNotFound) {
		t.Fatalf("删除后明文 key 仍可解析（越权漏洞！）应 ErrKeyNotFound, 得到 %v", err)
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
