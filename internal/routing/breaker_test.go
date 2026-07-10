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

func mustAllow(t *testing.T, b *Breaker) *BreakerPermit {
	t.Helper()
	permit, ok := b.Allow()
	if !ok {
		t.Fatal("期望熔断器放行尝试")
	}
	return permit
}

func TestBreakerTripsAfterThreshold(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{FailureThreshold: 3, CooldownPeriod: 30 * time.Second}, &clock)

	// 未达阈值仍闭合
	mustAllow(t, b).RecordFailure()
	mustAllow(t, b).RecordFailure()
	if b.State() != StateClosed {
		t.Fatalf("2 次失败后应仍 closed, 得到 %s", b.State())
	}
	// 第 3 次触发熔断
	mustAllow(t, b).RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("3 次失败后应 open, 得到 %s", b.State())
	}
	// Open 期间拒绝放行
	if _, ok := b.Allow(); ok {
		t.Error("open 状态不应放行")
	}
}

func TestBreakerSuccessResetsCount(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{FailureThreshold: 3, CooldownPeriod: time.Second}, &clock)
	mustAllow(t, b).RecordFailure()
	mustAllow(t, b).RecordFailure()
	mustAllow(t, b).RecordSuccess() // 重置连续失败
	mustAllow(t, b).RecordFailure()
	mustAllow(t, b).RecordFailure()
	if b.State() != StateClosed {
		t.Fatalf("成功应重置计数, 期望 closed 得到 %s", b.State())
	}
}

func TestBreakerHalfOpenRecovery(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{FailureThreshold: 1, CooldownPeriod: 30 * time.Second, HalfOpenMaxProbes: 1}, &clock)

	mustAllow(t, b).RecordFailure() // 立即熔断
	if b.State() != StateOpen {
		t.Fatalf("应 open")
	}

	// 冷却未满，Ready/Allow 均拒绝
	if b.Ready() {
		t.Error("冷却未满不应就绪")
	}
	if _, ok := b.Allow(); ok {
		t.Error("冷却未满不应就绪/放行")
	}

	// 推进时钟越过冷却期
	clock = clock.Add(31 * time.Second)
	if !b.Ready() {
		t.Error("冷却期满应就绪")
	}
	permit := mustAllow(t, b)
	if b.State() != StateHalfOpen {
		t.Fatalf("放行后应半开, 得到 %s", b.State())
	}
	// 半开探测成功 -> 恢复
	permit.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("探测成功应 closed, 得到 %s", b.State())
	}
}

func TestBreakerHalfOpenFailureReopens(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{FailureThreshold: 1, CooldownPeriod: 10 * time.Second, HalfOpenMaxProbes: 1}, &clock)
	mustAllow(t, b).RecordFailure()
	clock = clock.Add(11 * time.Second)
	mustAllow(t, b).RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("半开失败应重新 open, 得到 %s", b.State())
	}
}

// TestBreakerHalfOpenProbeLimit 验证半开只放行受限探测数，
// 且 Ready() 无副作用（不消耗探测额度）——对应 Select 的过滤修复。
func TestBreakerHalfOpenProbeLimit(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{FailureThreshold: 1, CooldownPeriod: 5 * time.Second, HalfOpenMaxProbes: 1}, &clock)
	mustAllow(t, b).RecordFailure()
	clock = clock.Add(6 * time.Second)

	// 多次 Ready() 不应消耗额度
	for i := 0; i < 5; i++ {
		if !b.Ready() {
			t.Fatalf("Ready 第 %d 次应为 true（无副作用）", i)
		}
	}
	// 首个 Allow 放行
	mustAllow(t, b)
	// 额度用尽，第二个 Allow 拒绝
	if _, ok := b.Allow(); ok {
		t.Error("超出 HalfOpenMaxProbes 应拒绝")
	}
}

func TestBreakerLateSuccessCannotCloseNewOpenGeneration(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{
		FailureThreshold:  1,
		CooldownPeriod:    time.Minute,
		HalfOpenMaxProbes: 1,
	}, &clock)

	first := mustAllow(t, b)
	late := mustAllow(t, b)
	first.RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("并发失败后应 open, 得到 %s", b.State())
	}

	late.RecordSuccess()
	if b.State() != StateOpen {
		t.Fatalf("旧请求迟到成功不应关闭新代际, 得到 %s", b.State())
	}
}

func TestBreakerNeutralHalfOpenAttemptReleasesProbe(t *testing.T) {
	clock := time.Now()
	b := newTestBreaker(BreakerConfig{
		FailureThreshold:  1,
		CooldownPeriod:    time.Second,
		HalfOpenMaxProbes: 1,
	}, &clock)

	mustAllow(t, b).RecordFailure()
	clock = clock.Add(2 * time.Second)
	probe := mustAllow(t, b)
	if b.State() != StateHalfOpen {
		t.Fatalf("冷却后探测应进入 half-open, 得到 %s", b.State())
	}

	probe.RecordNeutral()
	if _, ok := b.Allow(); !ok {
		t.Fatal("中性结果应释放 half-open 探测名额")
	}
}
