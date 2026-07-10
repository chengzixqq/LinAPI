package routing

import (
	"math/rand"
	"sort"
)

// weightedShuffle 对同一优先级组内的渠道按权重做“加权随机排序”，
// 返回打乱后的顺序。权重越大越可能排在前面。
//
// 算法：加权随机不放回抽样（每轮按剩余权重占比抽一个放到结果尾部）。
// 这样既保证首选符合权重分布，又给出完整的故障转移次序。
func weightedShuffle(channels []*Channel, rng *rand.Rand) []*Channel {
	n := len(channels)
	if n <= 1 {
		out := make([]*Channel, n)
		copy(out, channels)
		return out
	}

	// 复制一份用于消耗。
	pool := make([]*Channel, n)
	copy(pool, channels)

	out := make([]*Channel, 0, n)
	for len(pool) > 0 {
		total := 0
		for _, c := range pool {
			w := c.Weight
			if w <= 0 {
				w = 1
			}
			total += w
		}

		// 在 [0,total) 取点，落在哪个渠道的权重区间就选谁。
		r := rng.Intn(total)
		idx := 0
		acc := 0
		for i, c := range pool {
			w := c.Weight
			if w <= 0 {
				w = 1
			}
			acc += w
			if r < acc {
				idx = i
				break
			}
		}

		out = append(out, pool[idx])
		// 从 pool 移除 idx（顺序无所谓，用 swap-remove）。
		pool[idx] = pool[len(pool)-1]
		pool = pool[:len(pool)-1]
	}
	return out
}

// orderCandidates 把候选渠道按“优先级降序分组、组内加权随机”排成完整候选序列。
func orderCandidates(channels []*Channel, rng *rand.Rand) []*Channel {
	if len(channels) == 0 {
		return nil
	}

	// 按优先级降序分组。
	byPriority := map[int][]*Channel{}
	var priorities []int
	for _, c := range channels {
		if _, ok := byPriority[c.Priority]; !ok {
			priorities = append(priorities, c.Priority)
		}
		byPriority[c.Priority] = append(byPriority[c.Priority], c)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(priorities)))

	out := make([]*Channel, 0, len(channels))
	for _, p := range priorities {
		out = append(out, weightedShuffle(byPriority[p], rng)...)
	}
	return out
}
