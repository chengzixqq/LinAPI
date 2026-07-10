package billing

import (
	"errors"
	"math"
)

const (
	DefaultMaxBillableInputTokens = 128_000
	DefaultMaxOutputTokens        = 4_096
	tokensPerMillion              = int64(1_000_000)
)

var (
	ErrInvalidTokenLimit  = errors.New("billing: token 上限无效")
	ErrCostOverflow       = errors.New("billing: 计价整数溢出")
	ErrUnknownModelPolicy = errors.New("billing: 模型缺少显式计费策略")
)

// ModelPrice 定义单个模型的计费单价，单位：最小计费单位 / 每 100 万 token。
type ModelPrice struct {
	InputPer1M              int64
	OutputPer1M             int64
	CacheCreationInputPer1M int64
	CacheReadInputPer1M     int64
}

// ModelPolicy 同时给出价格和可证明的最大计费边界。MaxBillableInputTokens
// 与 MaxOutputTokens 分别计入预授权，即使供应商共享 context window 也不做未经证明
// 的相减优化；多冻结的余额会在精确结算时立即退回。
type ModelPolicy struct {
	ModelPrice
	MaxBillableInputTokens int
	MaxOutputTokens        int
}

type Pricing struct {
	models        map[string]ModelPolicy
	fallback      ModelPolicy
	strict        bool
	invalidModels map[string]struct{}
}

// NewPricing 保留旧调用方的便捷构造，使用安全的默认计费边界。
func NewPricing(models map[string]ModelPrice, defInput, defOutput int64) *Pricing {
	policies := make(map[string]ModelPolicy, len(models))
	for model, price := range models {
		policies[model] = ModelPolicy{
			ModelPrice: price, MaxBillableInputTokens: DefaultMaxBillableInputTokens,
			MaxOutputTokens: DefaultMaxOutputTokens,
		}
	}
	return NewPricingWithPolicies(policies, ModelPolicy{
		ModelPrice:             ModelPrice{InputPer1M: defInput, OutputPer1M: defOutput},
		MaxBillableInputTokens: DefaultMaxBillableInputTokens,
		MaxOutputTokens:        DefaultMaxOutputTokens,
	})
}

func NewPricingWithPolicies(models map[string]ModelPolicy, fallback ModelPolicy) *Pricing {
	m := make(map[string]ModelPolicy, len(models))
	for name, policy := range models {
		m[name] = normalizePolicy(policy, fallback)
	}
	fallback = normalizePolicy(fallback, ModelPolicy{
		MaxBillableInputTokens: DefaultMaxBillableInputTokens,
		MaxOutputTokens:        DefaultMaxOutputTokens,
	})
	return &Pricing{models: m, fallback: fallback}
}

// NewStrictPricingWithPolicies 用于生产：任何未显式配置的对外模型都 fail-closed，
// 防止供应商上下文上限高于全局猜测值时低估预授权。
func NewStrictPricingWithPolicies(models map[string]ModelPolicy, fallback ModelPolicy) *Pricing {
	p := NewPricingWithPolicies(models, fallback)
	p.strict = true
	p.invalidModels = make(map[string]struct{})
	for name, policy := range models {
		if policy.InputPer1M <= 0 || policy.OutputPer1M <= 0 ||
			policy.CacheCreationInputPer1M <= 0 || policy.CacheReadInputPer1M <= 0 ||
			policy.MaxBillableInputTokens <= 0 || policy.MaxOutputTokens <= 0 {
			p.invalidModels[name] = struct{}{}
		}
	}
	return p
}

func normalizePolicy(p, fallback ModelPolicy) ModelPolicy {
	if p.CacheCreationInputPer1M <= 0 {
		p.CacheCreationInputPer1M = p.InputPer1M
	}
	if p.CacheReadInputPer1M <= 0 {
		p.CacheReadInputPer1M = p.InputPer1M
	}
	if p.MaxBillableInputTokens <= 0 {
		p.MaxBillableInputTokens = fallback.MaxBillableInputTokens
	}
	if p.MaxBillableInputTokens <= 0 {
		p.MaxBillableInputTokens = DefaultMaxBillableInputTokens
	}
	if p.MaxOutputTokens <= 0 {
		p.MaxOutputTokens = fallback.MaxOutputTokens
	}
	if p.MaxOutputTokens <= 0 {
		p.MaxOutputTokens = DefaultMaxOutputTokens
	}
	return p
}

func (p *Pricing) policy(model string) ModelPolicy {
	if policy, ok := p.models[model]; ok {
		return policy
	}
	return p.fallback
}

func (p *Pricing) checkedPolicy(model string) (ModelPolicy, error) {
	policy, ok := p.models[model]
	if !ok {
		if p.strict {
			return ModelPolicy{}, ErrUnknownModelPolicy
		}
		policy = p.fallback
	}
	if _, invalid := p.invalidModels[model]; invalid {
		return ModelPolicy{}, ErrInvalidTokenLimit
	}
	return policy, nil
}

// NormalizeMaxOutput 校验客户端上限；未提供时注入服务端模型上限。
func (p *Pricing) NormalizeMaxOutput(model string, requested *int) (int, error) {
	policy, err := p.checkedPolicy(model)
	if err != nil {
		return 0, err
	}
	limit := policy.MaxOutputTokens
	if limit <= 0 {
		return 0, ErrInvalidTokenLimit
	}
	if requested == nil {
		return limit, nil
	}
	if *requested <= 0 || *requested > limit {
		return 0, ErrInvalidTokenLimit
	}
	return *requested, nil
}

// ReservationCost 返回本次请求在服务端强制输出上限下的最大可计费成本。
func (p *Pricing) ReservationCost(model string, maxOutput int) (int64, int, error) {
	policy, err := p.checkedPolicy(model)
	if err != nil {
		return 0, 0, err
	}
	if maxOutput <= 0 || maxOutput > policy.MaxOutputTokens || policy.MaxBillableInputTokens <= 0 {
		return 0, 0, ErrInvalidTokenLimit
	}
	if policy.InputPer1M <= 0 || policy.OutputPer1M <= 0 ||
		policy.CacheCreationInputPer1M <= 0 || policy.CacheReadInputPer1M <= 0 {
		return 0, 0, ErrInvalidTokenLimit
	}
	maxInputPrice := max(policy.InputPer1M, policy.CacheCreationInputPer1M, policy.CacheReadInputPer1M)
	cost, err := checkedCost(ModelPrice{InputPer1M: maxInputPrice, OutputPer1M: policy.OutputPer1M},
		policy.MaxBillableInputTokens, maxOutput)
	return cost, policy.MaxBillableInputTokens, err
}

// CostChecked 按精确输入/输出 token 计算成本，任何负数或 int64 溢出都返回错误。
func (p *Pricing) CostChecked(model string, inputTokens, outputTokens int) (int64, error) {
	policy, err := p.checkedPolicy(model)
	if err != nil {
		return 0, err
	}
	return checkedCost(policy.ModelPrice, inputTokens, outputTokens)
}

func (p *Pricing) CostUsageChecked(model string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int) (int64, error) {
	policy, err := p.checkedPolicy(model)
	if err != nil {
		return 0, err
	}
	return checkedCostParts(
		weightedTokens{inputTokens, policy.InputPer1M},
		weightedTokens{outputTokens, policy.OutputPer1M},
		weightedTokens{cacheCreationTokens, policy.CacheCreationInputPer1M},
		weightedTokens{cacheReadTokens, policy.CacheReadInputPer1M},
	)
}

// ConservativeTotalCost 在只有 total_tokens 时按输入/输出较高单价收费，保证
// 任意真实拆分都不会低估。
func (p *Pricing) ConservativeTotalCost(model string, totalTokens int) (int64, error) {
	policy, err := p.checkedPolicy(model)
	if err != nil {
		return 0, err
	}
	price := policy.ModelPrice
	if price.InputPer1M <= 0 || price.OutputPer1M <= 0 ||
		price.CacheCreationInputPer1M <= 0 || price.CacheReadInputPer1M <= 0 {
		return 0, ErrInvalidTokenLimit
	}
	maxPrice := max(price.InputPer1M, price.OutputPer1M, price.CacheCreationInputPer1M, price.CacheReadInputPer1M)
	return checkedCost(ModelPrice{InputPer1M: maxPrice}, totalTokens, 0)
}

// Cost 保留兼容接口；发生非法输入/溢出时返回 MaxInt64，调用方不能因此少收费。
func (p *Pricing) Cost(model string, inputTokens, outputTokens int) int64 {
	cost, err := p.CostChecked(model, inputTokens, outputTokens)
	if err != nil {
		return math.MaxInt64
	}
	return cost
}

func checkedCost(price ModelPrice, inputTokens, outputTokens int) (int64, error) {
	return checkedCostParts(weightedTokens{inputTokens, price.InputPer1M}, weightedTokens{outputTokens, price.OutputPer1M})
}

type weightedTokens struct {
	tokens int
	price  int64
}

func checkedCostParts(parts ...weightedTokens) (int64, error) {
	total := int64(0)
	for _, part := range parts {
		if part.tokens < 0 || part.price < 0 {
			return 0, ErrCostOverflow
		}
		value, err := checkedMul(int64(part.tokens), part.price)
		if err != nil || total > math.MaxInt64-value {
			return 0, ErrCostOverflow
		}
		total += value
	}
	if total == 0 {
		return 0, nil
	}
	if total > math.MaxInt64-(tokensPerMillion-1) {
		return 0, ErrCostOverflow
	}
	return (total + tokensPerMillion - 1) / tokensPerMillion, nil
}

func checkedMul(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a > math.MaxInt64/b {
		return 0, ErrCostOverflow
	}
	return a * b, nil
}
