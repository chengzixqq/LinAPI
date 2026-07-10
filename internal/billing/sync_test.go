package billing

import (
	"context"
	"testing"
)

func TestAccountSync(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	acc := NewAccount(rdb)

	// 场景：用户已有热副本（首次预扣触发 seed=1000，扣 300 → 700）。
	if _, _, err := acc.Reserve(ctx, "u1", 300, 1000); err != nil {
		t.Fatal(err)
	}

	// 模拟线上充值：冷源余额被改成 5000，需 Sync 强制覆盖热副本。
	// 若不 Sync，惰性 seed 不会触发（key 已存在），热副本仍是旧值 700。
	if err := acc.Sync(ctx, "u1", 5000); err != nil {
		t.Fatal(err)
	}

	// 覆盖后再预扣 200：应从 5000 扣起 → 4800，证明 Sync 生效。
	ok, bal, err := acc.Reserve(ctx, "u1", 200, 999999)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || bal != 4800 {
		t.Fatalf("Sync 后预扣: ok=%v bal=%d, 期望 ok=true bal=4800", ok, bal)
	}
}

func TestAccountSyncCreatesHotCopy(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	acc := NewAccount(rdb)

	// 热副本原本不存在，Sync 直接写入权威余额。
	if err := acc.Sync(ctx, "fresh", 888); err != nil {
		t.Fatal(err)
	}

	// 随后预扣不应触发 seed（key 已由 Sync 建好），从 888 扣起。
	ok, bal, err := acc.Reserve(ctx, "fresh", 88, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || bal != 800 {
		t.Fatalf("Sync 建副本后预扣: ok=%v bal=%d, 期望 ok=true bal=800", ok, bal)
	}
}
