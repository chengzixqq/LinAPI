package routing

import (
	"testing"
	"time"
)

// newTestBreaker 造一个可控时钟的熔断器。
func newTestBreaker(cfg BreakerConfig, clock *time.Time) *Breaker {
	b := NewBreaker(cfg)
	b.now = func() time.Time { return *clock }
	return b
}

func TestBreakerTripsAfterThreshold(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{FailureThreshold: 3, CooldownPeriod: 30 * time.Second}, &clock)

	// 未达阈值仍闭合
	b.RecordFailure()
	b.RecordFailure()
	if b.State() != StateClosed {
		t.Fatalf("2 次失败后应仍 closed, 得到 %s", b.State())
	}
	// 第 3 次触发熔断
	b.RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("3 次失败后应 open, 得到 %s", b.State())
	}
	// Open 期间拒绝放行
	if b.Allow() {
		t.Error("open 状态不应放行")
	}
}

func TestBreakerSuccessResetsCount(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{FailureThreshold: 3, CooldownPeriod: time.Second}, &clock)
	b.RecordFailure()
	b.RecordFailure()
	b.RecordSuccess() // 重置连续失败
	b.RecordFailure()
	b.RecordFailure()
	if b.State() != StateClosed {
		t.Fatalf("成功应重置计数, 期望 closed 得到 %s", b.State())
	}
}

func TestBreakerHalfOpenRecovery(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{FailureThreshold: 1, CooldownPeriod: 30 * time.Second, HalfOpenMaxProbes: 1}, &clock)

	b.RecordFailure() // 立即熔断
	if b.State() != StateOpen {
		t.Fatalf("应 open")
	}

	// 冷却未满，Ready/Allow 均拒绝
	if b.Ready() || b.Allow() {
		t.Error("冷却未满不应就绪/放行")
	}

	// 推进时钟越过冷却期
	clock = clock.Add(31 * time.Second)
	if !b.Ready() {
		t.Error("冷却期满应就绪")
	}
	if !b.Allow() {
		t.Fatal("冷却期满应放行探测")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("放行后应半开, 得到 %s", b.State())
	}
	// 半开探测成功 -> 恢复
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("探测成功应 closed, 得到 %s", b.State())
	}
}

func TestBreakerHalfOpenFailureReopens(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{FailureThreshold: 1, CooldownPeriod: 10 * time.Second, HalfOpenMaxProbes: 1}, &clock)
	b.RecordFailure()
	clock = clock.Add(11 * time.Second)
	b.Allow() // 进入半开
	b.RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("半开失败应重新 open, 得到 %s", b.State())
	}
}

// TestBreakerHalfOpenProbeLimit 验证半开只放行受限探测数，
// 且 Ready() 无副作用（不消耗探测额度）——对应 Select 的过滤修复。
func TestBreakerHalfOpenProbeLimit(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{FailureThreshold: 1, CooldownPeriod: 5 * time.Second, HalfOpenMaxProbes: 1}, &clock)
	b.RecordFailure()
	clock = clock.Add(6 * time.Second)

	// 多次 Ready() 不应消耗额度
	for i := 0; i < 5; i++ {
		if !b.Ready() {
			t.Fatalf("Ready 第 %d 次应为 true（无副作用）", i)
		}
	}
	// 首个 Allow 放行
	if !b.Allow() {
		t.Fatal("首个探测应放行")
	}
	// 额度用尽，第二个 Allow 拒绝
	if b.Allow() {
		t.Error("超出 HalfOpenMaxProbes 应拒绝")
	}
}
