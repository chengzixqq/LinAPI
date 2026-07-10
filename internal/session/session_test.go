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
	m, _ := newTestManager(t)
	ctx := context.Background()
	data := SessionData{AccountID: 1, Username: "alice", Role: "user", ExternalID: "alice"}

	token, err := m.Create(ctx, data, DefaultTTL)
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if len(token) != 64 { // 32 字节 hex。
		t.Fatalf("token 应为 64 位 hex, 得到 %d 位", len(token))
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

func TestSessionExpiry(t *testing.T) {
	m, mr := newTestManager(t)
	ctx := context.Background()
	token, _ := m.Create(ctx, SessionData{AccountID: 1, Username: "a", Role: "user"}, 1*time.Second)

	mr.FastForward(2 * time.Second) // 快进过期。
	if _, err := m.Get(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("过期后应 ErrNotFound, 得到 %v", err)
	}
}
