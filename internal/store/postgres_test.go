package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"linapi/internal/db"
)

// fakeQuerier 是 db.Querier 的测试替身，只实现 PGStore 用到的方法，
// 其余方法留空 panic（本测试不触达）。通过闭包注入每个方法的返回，
// 便于逐用例定制「命中 / 未命中 / 出错」。
type fakeQuerier struct {
	db.Querier // 嵌入接口：未实现的方法调用会 panic（nil 接口），本测试不涉及。
	resolveFn  func(ctx context.Context, keyHash string) (db.ResolveAPIKeyRow, error)
	balanceFn  func(ctx context.Context, externalID string) (int64, error)
}

func (f *fakeQuerier) ResolveAPIKey(ctx context.Context, keyHash string) (db.ResolveAPIKeyRow, error) {
	return f.resolveFn(ctx, keyHash)
}

func (f *fakeQuerier) GetBalance(ctx context.Context, externalID string) (int64, error) {
	return f.balanceFn(ctx, externalID)
}

func TestHashAPIKeyStable(t *testing.T) {
	// 同一明文两次哈希必须一致，且为 64 位十六进制（SHA-256）。
	h1 := HashAPIKey("sk-secret")
	h2 := HashAPIKey("sk-secret")
	if h1 != h2 {
		t.Fatalf("同一 Key 哈希不一致: %s vs %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("SHA-256 摘要应为 64 位十六进制，实际 %d 位", len(h1))
	}
	if HashAPIKey("sk-other") == h1 {
		t.Fatal("不同 Key 不应哈希到同值")
	}
}

func TestPGStoreResolveKey(t *testing.T) {
	ctx := context.Background()
	wantHash := HashAPIKey("sk-live-123")

	q := &fakeQuerier{
		resolveFn: func(_ context.Context, keyHash string) (db.ResolveAPIKeyRow, error) {
			// 验证 PGStore 传给查询的是明文的哈希摘要，而非明文本身。
			if keyHash != wantHash {
				t.Errorf("查询用的 keyHash=%q, 期望 %q", keyHash, wantHash)
			}
			return db.ResolveAPIKeyRow{
				KeyID:           "k1",
				UserExternalID:  "u1",
				RateLimitPerMin: 60,
				AllowedModels:   []string{"gpt-4o"},
			}, nil
		},
	}
	s := NewPGStore(q)

	id, err := s.ResolveKey(ctx, "sk-live-123")
	if err != nil {
		t.Fatal(err)
	}
	if id.KeyID != "k1" || id.UserID != "u1" || id.RateLimitPerMin != 60 {
		t.Fatalf("身份字段映射错误: %+v", id)
	}
	if !id.Enabled {
		t.Fatal("查到的密钥应视为启用")
	}
	if !id.Allows("gpt-4o") || id.Allows("claude-3") {
		t.Fatal("AllowedModels 未正确传递")
	}
}

func TestPGStoreResolveKeyNotFound(t *testing.T) {
	// 查询返回 pgx.ErrNoRows（密钥或用户禁用/不存在）应映射为 ErrKeyNotFound。
	q := &fakeQuerier{
		resolveFn: func(_ context.Context, _ string) (db.ResolveAPIKeyRow, error) {
			return db.ResolveAPIKeyRow{}, pgx.ErrNoRows
		},
	}
	s := NewPGStore(q)

	_, err := s.ResolveKey(context.Background(), "sk-nope")
	if err != ErrKeyNotFound {
		t.Fatalf("期望 ErrKeyNotFound, 实际 %v", err)
	}
}

func TestPGStoreBalance(t *testing.T) {
	ctx := context.Background()

	// 命中：返回冷源余额。
	q := &fakeQuerier{
		balanceFn: func(_ context.Context, _ string) (int64, error) { return 4200, nil },
	}
	bal, err := NewPGStore(q).Balance(ctx, "u1")
	if err != nil || bal != 4200 {
		t.Fatalf("命中余额: bal=%d err=%v, 期望 4200/nil", bal, err)
	}

	// 未命中（用户不存在或禁用）：按 0 余额返回，不视作错误。
	q.balanceFn = func(_ context.Context, _ string) (int64, error) { return 0, pgx.ErrNoRows }
	bal, err = NewPGStore(q).Balance(ctx, "ghost")
	if err != nil {
		t.Fatalf("未命中不应报错, 实际 %v", err)
	}
	if bal != 0 {
		t.Fatalf("未命中应返回 0 余额, 实际 %d", bal)
	}
}
