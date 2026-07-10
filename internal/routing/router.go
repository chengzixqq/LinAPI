package routing

import (
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNoChannel 表示没有可用于该模型的渠道（无渠道支持，或全部被熔断）。
var ErrNoChannel = errors.New("routing: 无可用渠道")

// snapshot 是一份不可变的渠道集合，供无锁读取。
// 热更新时整体替换指针，读路径不加锁。
type snapshot struct {
	channels []*Channel
	// byModel 预建索引：对外模型名 -> 支持它的渠道列表。
	byModel map[string][]*Channel
}

// Router 是路由/负载均衡引擎。
//
// 读多写少：Select 在每个请求上执行，用 atomic 指针无锁读渠道快照；
// 熔断状态每渠道一把小锁。渠道热更新通过 UpdateChannels 原子替换快照。
type Router struct {
	snap atomic.Pointer[snapshot]

	breakerCfg BreakerConfig

	// 每渠道熔断器，键为 Channel.ID。独立于快照，
	// 快照替换时保留同 ID 渠道的既有熔断状态。
	mu       sync.Mutex
	breakers map[string]*Breaker

	// rngPool 提供并发安全的随机源，避免全局锁竞争。
	rngPool sync.Pool
}

// NewRouter 用初始渠道创建 Router。
func NewRouter(channels []*Channel, breakerCfg BreakerConfig) *Router {
	r := &Router{
		breakerCfg: breakerCfg,
		breakers:   make(map[string]*Breaker),
	}
	r.rngPool.New = func() any {
		return rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	r.UpdateChannels(channels)
	return r
}

// UpdateChannels 原子替换渠道快照（热更新）。
// 保留仍存在渠道的熔断器状态，清理已移除渠道的熔断器。
func (r *Router) UpdateChannels(channels []*Channel) {
	// 构建模型索引。
	byModel := make(map[string][]*Channel)
	for _, c := range channels {
		if !c.Enabled {
			continue
		}
		for model := range c.Models {
			byModel[model] = append(byModel[model], c)
		}
	}

	r.snap.Store(&snapshot{channels: channels, byModel: byModel})

	// 同步熔断器集合。
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[string]*Breaker, len(channels))
	for _, c := range channels {
		if b, ok := r.breakers[c.ID]; ok {
			next[c.ID] = b // 保留既有状态
		} else {
			next[c.ID] = NewBreaker(r.breakerCfg)
		}
	}
	r.breakers = next
}

// Candidate 是一次选择返回的候选项：渠道 + 其熔断器（供转发器上报结果）。
type Candidate struct {
	Channel *Channel
	Breaker *Breaker
}

// Channels 返回当前快照中的渠道列表（浅拷贝切片，元素为共享的只读指针）。
// 供 /v1/models 聚合对外模型、以及热重载时对比使用。无快照时返回 nil。
func (r *Router) Channels() []*Channel {
	snap := r.snap.Load()
	if snap == nil {
		return nil
	}
	out := make([]*Channel, len(snap.channels))
	copy(out, snap.channels)
	return out
}

// Select 返回可服务 model 的候选渠道有序序列（已按优先级+权重排序，
// 并过滤掉当前被熔断的渠道）。转发器应依次尝试：对每个候选，先调用
// Candidate.Breaker.Allow() 做带副作用的准入（半开限流），获准后发起请求，
// 再按成败调用 RecordSuccess/RecordFailure。
//
// 这里用无副作用的 Breaker.Ready() 过滤，避免为未真正尝试的候选
// 提前消耗半开探测额度。若无渠道支持或全部被熔断则返回 ErrNoChannel。
func (r *Router) Select(model string) ([]Candidate, error) {
	snap := r.snap.Load()
	if snap == nil {
		return nil, ErrNoChannel
	}
	matched := snap.byModel[model]
	if len(matched) == 0 {
		return nil, ErrNoChannel
	}

	rng := r.rngPool.Get().(*rand.Rand)
	ordered := orderCandidates(matched, rng)
	r.rngPool.Put(rng)

	r.mu.Lock()
	candidates := make([]Candidate, 0, len(ordered))
	for _, c := range ordered {
		b := r.breakers[c.ID]
		if b == nil {
			// 快照与熔断器暂时不同步的兜底：新建一个。
			b = NewBreaker(r.breakerCfg)
			r.breakers[c.ID] = b
		}
		if b.Ready() {
			candidates = append(candidates, Candidate{Channel: c, Breaker: b})
		}
	}
	r.mu.Unlock()

	if len(candidates) == 0 {
		return nil, ErrNoChannel
	}
	return candidates, nil
}
