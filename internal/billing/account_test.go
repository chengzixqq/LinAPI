package billing

import (
	"context"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestRedis 启动一个内存 miniredis 并返回连接它的客户端。
// miniredis 内置 Lua 解释器，可真实执行我们的原子脚本。
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestAccountReserveSeedAndDeduct(t *testing.T) {
	ctx := context.Background()
	acc := NewAccount(newTestRedis(t))

	// 首次预扣：Redis 无该用户，用 seed=1000 初始化后扣 300。
	ok, bal, err := acc.Reserve(ctx, "u1", 300, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || bal != 700 {
		t.Fatalf("首次预扣: ok=%v bal=%d, 期望 ok=true bal=700", ok, bal)
	}

	// 再次预扣：seed 应被忽略（热值已存在），从 700 继续扣。
	ok, bal, err = acc.Reserve(ctx, "u1", 200, 999999)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || bal != 500 {
		t.Fatalf("二次预扣: ok=%v bal=%d, 期望 ok=true bal=500", ok, bal)
	}
}

func TestAccountReserveInsufficient(t *testing.T) {
	ctx := context.Background()
	acc := NewAccount(newTestRedis(t))

	// 余额 100，预扣 150 应失败且余额不变。
	ok, bal, err := acc.Reserve(ctx, "u1", 150, 100)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("余额不足时预扣应失败")
	}
	if bal != 100 {
		t.Fatalf("失败后余额应不变，得 %d", bal)
	}

	// 恰好扣光应成功（扣后为 0，不低于下限 0）。
	ok, bal, err = acc.Reserve(ctx, "u1", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || bal != 0 {
		t.Fatalf("扣光: ok=%v bal=%d, 期望 ok=true bal=0", ok, bal)
	}
}

func TestAccountSettleRefundAndOvercharge(t *testing.T) {
	ctx := context.Background()
	acc := NewAccount(newTestRedis(t))

	// 预扣 500（余额 1000 -> 500）。
	if _, _, err := acc.Reserve(ctx, "u1", 500, 1000); err != nil {
		t.Fatal(err)
	}

	// 退差 +200（押金多扣，退回）：500 -> 700。
	bal, err := acc.Settle(ctx, "u1", 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 700 {
		t.Fatalf("退回后余额应为 700，得 %d", bal)
	}

	// 补收 -100（成本超押金）：700 -> 600。
	bal, err = acc.Settle(ctx, "u1", -100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 600 {
		t.Fatalf("补收后余额应为 600，得 %d", bal)
	}
}

func TestAccountSettleAllowsNegative(t *testing.T) {
	ctx := context.Background()
	acc := NewAccount(newTestRedis(t))

	// 余额 50，结算补收 -200（实际用量远超押金）应放行，允许透支到 -150。
	if _, _, err := acc.Reserve(ctx, "u1", 0, 50); err != nil {
		t.Fatal(err)
	}
	bal, err := acc.Settle(ctx, "u1", -200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bal != -150 {
		t.Fatalf("结算应允许透支，余额应为 -150，得 %d", bal)
	}

	// 透支后，下一次预扣应被余额闸门拦截。
	ok, _, err := acc.Reserve(ctx, "u1", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("透支后预扣应失败")
	}
}

// TestAccountReserveInsufficientKeepsTTL 验证首次预扣即余额不足时，seed 写入的
// 余额 key 仍带正 TTL——否则该 key 永不过期，后续冷源充值会被这枚陈旧热副本永久屏蔽
// （审查 AUD-P1-03）。
func TestAccountReserveInsufficientKeepsTTL(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	acc := NewAccount(rdb)

	// 首次预扣即不足：seed=100，扣 150 应失败，但 key 已被 seed 写入。
	ok, _, err := acc.Reserve(ctx, "u1", 150, 100)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("余额不足时预扣应失败")
	}

	// 关键断言：seed 出的 key 必须带正 TTL，不能是永久 key（-1）。
	ttl, err := rdb.TTL(ctx, balanceKeyPrefix+"u1").Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 {
		t.Fatalf("余额不足后 key 应带正 TTL，得 %v（-1=永久 key，会永久屏蔽后续充值）", ttl)
	}
}

// TestAccountConcurrentReserveNoOversell 验证高并发下 Redis 原子扣费不超卖：
// 100 个 goroutine 各抢扣 10，总额恰好 1000，应正好全部成功且余额归零，
// 多扣（余额变负）或少扣都是 bug。
func TestAccountConcurrentReserveNoOversell(t *testing.T) {
	ctx := context.Background()
	acc := NewAccount(newTestRedis(t))

	// 先 seed：余额 1000。
	if _, _, err := acc.Reserve(ctx, "u1", 0, 1000); err != nil {
		t.Fatal(err)
	}

	const goroutines = 100
	const each = 10 // 100*10 = 1000，恰好扣光

	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// seed 传 0：此时热值已存在，seed 被忽略。
			ok, _, err := acc.Reserve(ctx, "u1", each, 0)
			if err != nil {
				t.Errorf("并发预扣出错: %v", err)
				return
			}
			if ok {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if successes != goroutines {
		t.Fatalf("应全部成功（余额恰好够），实际成功 %d/%d", successes, goroutines)
	}
}
