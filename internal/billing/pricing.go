// Package billing 实现计费结算：模型计价、Redis 原子预扣费与退差、用量日志异步落库。
//
// 数据流（对齐第 7 步的目标形态）：
//
//	冷源余额（store，未来是 PostgreSQL）──惰性 seed──▶ Redis 热副本（原子预扣/退差）
//	                                                        │
//	                                        用量日志 ──异步批量──▶ Sink（未来是 PostgreSQL）
//
// 请求路径：Reserve（按预估额度原子预扣）→ 转发上游 → Settle（按真实用量退差 + 记日志）。
package billing

// ModelPrice 定义单个模型的计费单价，单位：最小计费单位 / 每 100 万 token。
// 例如 InputPer1M=2500000 表示每 100 万 input token 收 2500000 个最小计费单位。
type ModelPrice struct {
	InputPer1M  int64
	OutputPer1M int64
}

// Pricing 是模型计价表，未命中的模型回退到兜底单价。并发安全（构建后只读）。
type Pricing struct {
	models        map[string]ModelPrice
	defaultInput  int64
	defaultOutput int64
}

// NewPricing 构建计价表。models 可为 nil；defInput/defOutput 是兜底单价。
func NewPricing(models map[string]ModelPrice, defInput, defOutput int64) *Pricing {
	m := make(map[string]ModelPrice, len(models))
	for k, v := range models {
		m[k] = v
	}
	return &Pricing{models: m, defaultInput: defInput, defaultOutput: defOutput}
}

// price 返回模型单价，未命中用兜底价。
func (p *Pricing) price(model string) ModelPrice {
	if mp, ok := p.models[model]; ok {
		return mp
	}
	return ModelPrice{InputPer1M: p.defaultInput, OutputPer1M: p.defaultOutput}
}

// Cost 按真实用量计算成本（最小计费单位）。
// 除以 100 万时向上取整，避免因整数截断而少收费。
func (p *Pricing) Cost(model string, inputTokens, outputTokens int) int64 {
	mp := p.price(model)
	total := int64(inputTokens)*mp.InputPer1M + int64(outputTokens)*mp.OutputPer1M
	if total <= 0 {
		return 0
	}
	return (total + 999_999) / 1_000_000
}
