package routing

import (
	"sync"
	"time"
)

// BreakerState 是熔断器状态。
type BreakerState int

const (
	// StateClosed：正常放行。累计连续失败达阈值则转 Open。
	StateClosed BreakerState = iota
	// StateOpen：熔断，拒绝放行。冷却期满后转 HalfOpen。
	StateOpen
	// StateHalfOpen：半开，放行有限探测请求。成功转 Closed，失败转 Open。
	StateHalfOpen
)

func (s BreakerState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// BreakerConfig 是熔断器参数。
type BreakerConfig struct {
	// FailureThreshold 是触发熔断的连续失败次数。
	FailureThreshold int
	// CooldownPeriod 是 Open 状态的冷却时长，期满转 HalfOpen 探测。
	CooldownPeriod time.Duration
	// HalfOpenMaxProbes 是半开状态允许并发探测的请求数。
	HalfOpenMaxProbes int
}

// DefaultBreakerConfig 返回一组适合线上的默认参数。
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		FailureThreshold:  5,
		CooldownPeriod:    30 * time.Second,
		HalfOpenMaxProbes: 1,
	}
}

// Breaker 是单个渠道的熔断器，并发安全。
type Breaker struct {
	cfg BreakerConfig

	mu              sync.Mutex
	state           BreakerState
	consecutiveFail int
	openedAt        time.Time
	halfOpenProbes  int
	generation      uint64

	// now 便于测试注入时钟；生产为 time.Now。
	now func() time.Time
}

// BreakerPermit 表示一次已经获准发起的上游尝试。结果必须通过该许可回报，
// 这样旧请求的迟到结果就不会修改后续代际的熔断状态。
type BreakerPermit struct {
	breaker    *Breaker
	generation uint64
	once       sync.Once
}

type breakerResult uint8

const (
	breakerSuccess breakerResult = iota
	breakerFailure
	breakerNeutral
)

// NewBreaker 创建熔断器。
func NewBreaker(cfg BreakerConfig) *Breaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.CooldownPeriod <= 0 {
		cfg.CooldownPeriod = 30 * time.Second
	}
	if cfg.HalfOpenMaxProbes <= 0 {
		cfg.HalfOpenMaxProbes = 1
	}
	return &Breaker{cfg: cfg, state: StateClosed, now: time.Now}
}

// Allow 返回当前是否允许放行一个请求，并给出绑定本次尝试的许可。
// 有副作用：Open 冷却期满时会转入 HalfOpen 并放行探测；
// HalfOpen 下按 HalfOpenMaxProbes 限制并发探测数。
// 应在“真正发起一次尝试之前”调用，且随后必须配对调用
// BreakerPermit 的结果方法，否则半开探测额度不会释放。
func (b *Breaker) Allow() (*BreakerPermit, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return b.newPermit(), true
	case StateOpen:
		if b.now().Sub(b.openedAt) >= b.cfg.CooldownPeriod {
			// 冷却期满，进入半开并放行首个探测。
			b.state = StateHalfOpen
			b.generation++
			b.halfOpenProbes = 1
			return b.newPermit(), true
		}
		return nil, false
	case StateHalfOpen:
		if b.halfOpenProbes < b.cfg.HalfOpenMaxProbes {
			b.halfOpenProbes++
			return b.newPermit(), true
		}
		return nil, false
	}
	return nil, false
}

func (b *Breaker) newPermit() *BreakerPermit {
	return &BreakerPermit{breaker: b, generation: b.generation}
}

// RecordSuccess 记录该次获准尝试成功。
func (p *BreakerPermit) RecordSuccess() {
	p.resolve(breakerSuccess)
}

// RecordFailure 记录该次获准尝试失败。
func (p *BreakerPermit) RecordFailure() {
	p.resolve(breakerFailure)
}

// RecordNeutral 结束该次尝试但不将其计为渠道成功或失败。
// 典型场景是客户端取消请求；HalfOpen 下仍需释放占用的探测名额。
func (p *BreakerPermit) RecordNeutral() {
	p.resolve(breakerNeutral)
}

func (p *BreakerPermit) resolve(result breakerResult) {
	if p == nil || p.breaker == nil {
		return
	}
	p.once.Do(func() {
		p.breaker.record(p.generation, result)
	})
}

func (b *Breaker) record(generation uint64, result breakerResult) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 许可创建后，熔断器可能已因另一个并发请求推进到新代际。
	// 旧代际的迟到结果不能再覆盖当前状态。
	if generation != b.generation {
		return
	}

	switch result {
	case breakerSuccess:
		switch b.state {
		case StateClosed:
			b.consecutiveFail = 0
		case StateHalfOpen:
			b.consecutiveFail = 0
			b.state = StateClosed
			b.halfOpenProbes = 0
			b.generation++
		}
	case breakerFailure:
		switch b.state {
		case StateHalfOpen:
			b.trip()
		case StateClosed:
			b.consecutiveFail++
			if b.consecutiveFail >= b.cfg.FailureThreshold {
				b.trip()
			}
		}
	case breakerNeutral:
		if b.state == StateHalfOpen && b.halfOpenProbes > 0 {
			b.halfOpenProbes--
		}
	}
}

// trip 转入 Open 状态（调用方须持锁）。
func (b *Breaker) trip() {
	b.state = StateOpen
	b.openedAt = b.now()
	b.halfOpenProbes = 0
	b.generation++
}

// Ready 是无副作用的准入预判：返回该渠道当前是否“有可能”放行，
// 供 Router.Select 过滤候选序列使用（不消耗半开探测额度）。
// 真正发起尝试前仍需调用 Allow() 做带副作用的准入。
func (b *Breaker) Ready() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return true
	case StateOpen:
		// 冷却期满即视为就绪（Allow 时会转半开）。
		return b.now().Sub(b.openedAt) >= b.cfg.CooldownPeriod
	case StateHalfOpen:
		return b.halfOpenProbes < b.cfg.HalfOpenMaxProbes
	}
	return false
}

// State 返回当前状态（用于监控/测试）。
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// StateCode 返回用于监控指标的稳定状态编码：0=closed，1=half-open，2=open。
// 独立于 BreakerState 枚举顺序，避免监控口径与内部枚举耦合。
func (b *Breaker) StateCode() int {
	switch b.State() {
	case StateClosed:
		return 0
	case StateHalfOpen:
		return 1
	case StateOpen:
		return 2
	default:
		return 0
	}
}
