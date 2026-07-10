package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestManager(t *testing.T) (*Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewManager(rdb), mr
}

func TestSessionCreateGetDelete(t *testing.T) {
	m, mr := newTestManager(t)
	ctx := context.Background()
	data := SessionData{AccountID: 1, Username: "alice", Role: "user", ExternalID: "alice"}

	token, err := m.Create(ctx, data, DefaultTTL)
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if len(token) != 64 { // 32 字节 hex。
		t.Fatalf("token 应为 64 位 hex, 得到 %d 位", len(token))
	}
	if mr.Exists(keyPrefix + token) {
		t.Fatal("Redis key 不得直接包含可重放的会话 token")
	}
	if !mr.Exists(sessionKey(token)) {
		t.Fatal("会话应按 token 摘要索引")
	}

	got, err := m.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if got != data {
		t.Fatalf("会话数据不符: %+v vs %+v", got, data)
	}

	if err := m.Delete(ctx, token); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if _, err := m.Get(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应 ErrNotFound, 得到 %v", err)
	}
}

func TestSessionGetMissing(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.Get(context.Background(), "nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在 token 应 ErrNotFound, 得到 %v", err)
	}
}

// TestSessionCarriesCSRFToken 验证会话能承载 CSRFToken 字段并原样往返（审查 AUD-P1-26）。
// CSRF token 与会话绑定：登录时生成并存入会话，写请求校验 header 是否等于此值，
// 登出删会话即令 token 失效。session 包只负责存取，token 由 login handler 生成。
func TestSessionCarriesCSRFToken(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	data := SessionData{AccountID: 1, Username: "alice", Role: "user", ExternalID: "alice", CSRFToken: "csrf-abc-123"}

	token, err := m.Create(ctx, data, DefaultTTL)
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	got, err := m.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if got.CSRFToken != "csrf-abc-123" {
		t.Fatalf("CSRFToken 未往返: 得到 %q", got.CSRFToken)
	}
}

func TestSessionExpiry(t *testing.T) {
	m, mr := newTestManager(t)
	ctx := context.Background()
	token, _ := m.Create(ctx, SessionData{AccountID: 1, Username: "a", Role: "user"}, 1*time.Second)

	mr.FastForward(2 * time.Second) // 快进过期。
	if _, err := m.Get(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("过期后应 ErrNotFound, 得到 %v", err)
	}
}

func TestSessionLimitIsAtomicAndDeleteFreesSlot(t *testing.T) {
	_, mr := newTestManager(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	m := NewManagerWithLimit(rdb, 2)
	ctx := context.Background()
	data := SessionData{AccountID: 7, Username: "u", Role: "user"}
	first, err := m.Create(ctx, data, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, data, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, data, time.Hour); !errors.Is(err, ErrTooManyActiveSessions) {
		t.Fatalf("第三个会话应被拒绝，得到 %v", err)
	}
	if err := m.Delete(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, data, time.Hour); err != nil {
		t.Fatalf("删除后应释放名额，得到 %v", err)
	}
}
