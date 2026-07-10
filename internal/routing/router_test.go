package routing

import (
	"math/rand"
	"testing"
	"time"
)

func ch(id string, priority, weight int, models ...string) *Channel {
	m := make(map[string]string)
	for _, model := range models {
		m[model] = ""
	}
	return &Channel{
		ID: id, Name: id, Format: FormatOpenAI,
		BaseURL: "http://x", APIKey: "k",
		Models: m, Priority: priority, Weight: weight, Enabled: true,
	}
}

func TestSelectNoChannel(t *testing.T) {
	r := NewRouter([]*Channel{ch("a", 0, 1, "gpt-4o")}, DefaultBreakerConfig())
	if _, err := r.Select("claude-3"); err != ErrNoChannel {
		t.Errorf("不支持的模型应返回 ErrNoChannel, 得到 %v", err)
	}
}

func TestSelectPriorityOrder(t *testing.T) {
	// 高优先级 hi，低优先级 lo，均支持 gpt-4o。
	r := NewRouter([]*Channel{
		ch("lo", 1, 1, "gpt-4o"),
		ch("hi", 10, 1, "gpt-4o"),
	}, DefaultBreakerConfig())

	for i := 0; i < 20; i++ {
		cands, err := r.Select("gpt-4o")
		if err != nil {
			t.Fatalf("Select 失败: %v", err)
		}
		if len(cands) != 2 {
			t.Fatalf("应有 2 个候选, 得到 %d", len(cands))
		}
		// 高优先级必须永远排在首位
		if cands[0].Channel.ID != "hi" {
			t.Fatalf("高优先级应排首位, 得到 %s", cands[0].Channel.ID)
		}
	}
}

func TestSelectWeightedDistribution(t *testing.T) {
	// 同优先级，权重 9:1，统计首选分布应大致符合。
	r := NewRouter([]*Channel{
		ch("heavy", 5, 9, "gpt-4o"),
		ch("light", 5, 1, "gpt-4o"),
	}, DefaultBreakerConfig())

	counts := map[string]int{}
	const N = 4000
	for i := 0; i < N; i++ {
		cands, err := r.Select("gpt-4o")
		if err != nil {
			t.Fatalf("Select 失败: %v", err)
		}
		counts[cands[0].Channel.ID]++
	}
	// heavy 期望约 90%，给宽松区间避免偶发抖动。
	ratio := float64(counts["heavy"]) / float64(N)
	if ratio < 0.82 || ratio > 0.96 {
		t.Errorf("加权分布偏离预期: heavy 占比 %.3f（期望 ~0.9）", ratio)
	}
}

func TestSelectSkipsOpenBreaker(t *testing.T) {
	r := NewRouter([]*Channel{
		ch("a", 5, 1, "gpt-4o"),
		ch("b", 5, 1, "gpt-4o"),
	}, BreakerConfig{FailureThreshold: 1, CooldownPeriod: time.Minute})

	// 让 a 熔断
	cands, _ := r.Select("gpt-4o")
	var aBreaker *Breaker
	for _, c := range cands {
		if c.Channel.ID == "a" {
			aBreaker = c.Breaker
		}
	}
	if aBreaker == nil {
		t.Fatal("未取到 a 的熔断器")
	}
	mustAllow(t, aBreaker).RecordFailure() // 阈值 1，立即熔断

	// 之后 Select 应只剩 b
	for i := 0; i < 10; i++ {
		cands, err := r.Select("gpt-4o")
		if err != nil {
			t.Fatalf("Select 失败: %v", err)
		}
		for _, c := range cands {
			if c.Channel.ID == "a" {
				t.Fatal("a 已熔断, 不应出现在候选中")
			}
		}
		if len(cands) != 1 || cands[0].Channel.ID != "b" {
			t.Fatalf("应只剩 b, 得到 %+v", cands)
		}
	}
}

func TestSelectAllOpenReturnsError(t *testing.T) {
	r := NewRouter([]*Channel{ch("a", 5, 1, "gpt-4o")}, BreakerConfig{FailureThreshold: 1, CooldownPeriod: time.Minute})
	cands, _ := r.Select("gpt-4o")
	mustAllow(t, cands[0].Breaker).RecordFailure()
	if _, err := r.Select("gpt-4o"); err != ErrNoChannel {
		t.Errorf("全熔断应返回 ErrNoChannel, 得到 %v", err)
	}
}

// TestUpdateChannelsPreservesBreaker 验证热更新保留同 ID 渠道的熔断状态。
func TestUpdateChannelsPreservesBreaker(t *testing.T) {
	r := NewRouter([]*Channel{ch("a", 5, 1, "gpt-4o")}, BreakerConfig{FailureThreshold: 1, CooldownPeriod: time.Minute})
	cands, _ := r.Select("gpt-4o")
	mustAllow(t, cands[0].Breaker).RecordFailure() // 熔断 a

	// 热更新：a 仍在，新增 b
	r.UpdateChannels([]*Channel{
		ch("a", 5, 1, "gpt-4o"),
		ch("b", 5, 1, "gpt-4o"),
	})

	// a 的熔断状态应保留，Select 只返回 b
	cands, err := r.Select("gpt-4o")
	if err != nil {
		t.Fatalf("Select 失败: %v", err)
	}
	if len(cands) != 1 || cands[0].Channel.ID != "b" {
		t.Fatalf("热更新后应保留 a 的熔断, 只剩 b, 得到 %+v", cands)
	}
}

func TestWeightedShuffleAllPresent(t *testing.T) {
	// 加权乱序必须是完整排列（故障转移要能遍历所有渠道）。
	rng := rand.New(rand.NewSource(1))
	channels := []*Channel{ch("a", 0, 1), ch("b", 0, 5), ch("c", 0, 3)}
	out := weightedShuffle(channels, rng)
	if len(out) != 3 {
		t.Fatalf("乱序后数量应为 3, 得到 %d", len(out))
	}
	seen := map[string]bool{}
	for _, c := range out {
		seen[c.ID] = true
	}
	if !seen["a"] || !seen["b"] || !seen["c"] {
		t.Errorf("乱序后应包含全部渠道, 得到 %v", seen)
	}
}
