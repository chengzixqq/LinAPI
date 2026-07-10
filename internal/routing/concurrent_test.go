package routing

import (
	"sync"
	"testing"
	"time"
)

// TestConcurrentSelectAndReport 并发压测：多 goroutine 同时 Select 并上报成败，
// 期间穿插热更新。本机无 CGO 无法用 -race，此测试意在触发 map 并发读写 panic
// 或状态崩溃（若存在），作为并发安全的冒烟验证。
func TestConcurrentSelectAndReport(t *testing.T) {
	r := NewRouter([]*Channel{
		ch("a", 5, 3, "gpt-4o"),
		ch("b", 5, 1, "gpt-4o"),
		ch("c", 1, 1, "gpt-4o"),
	}, BreakerConfig{FailureThreshold: 3, CooldownPeriod: 50 * time.Millisecond, HalfOpenMaxProbes: 1})

	const workers = 32
	const iters = 500
	var wg sync.WaitGroup

	// 并发选择 + 上报
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				cands, err := r.Select("gpt-4o")
				if err != nil {
					continue // 可能短暂全熔断
				}
				c := cands[0]
				if permit, ok := c.Breaker.Allow(); ok {
					// 交替上报成败，制造熔断/恢复翻转
					if (seed+i)%4 == 0 {
						permit.RecordFailure()
					} else {
						permit.RecordSuccess()
					}
				}
			}
		}(w)
	}

	// 并发热更新
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			r.UpdateChannels([]*Channel{
				ch("a", 5, 3, "gpt-4o"),
				ch("b", 5, 1, "gpt-4o"),
				ch("c", 1, 1, "gpt-4o"),
			})
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
	// 跑完不 panic 即通过；再做一次正常 Select 确认引擎仍可用。
	if _, err := r.Select("gpt-4o"); err != nil && err != ErrNoChannel {
		t.Fatalf("压测后 Select 异常: %v", err)
	}
}
