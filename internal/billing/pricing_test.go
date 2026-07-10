package billing

import (
	"errors"
	"math"
	"testing"
)

func TestPricingCost(t *testing.T) {
	p := NewPricing(map[string]ModelPrice{
		"gpt-4o": {InputPer1M: 2_500_000, OutputPer1M: 10_000_000},
	}, 1_000_000, 2_000_000)

	tests := []struct {
		name     string
		model    string
		in, out  int
		wantCost int64
	}{
		{"命中模型-整除", "gpt-4o", 1_000_000, 1_000_000, 12_500_000},
		{"命中模型-零用量", "gpt-4o", 0, 0, 0},
		{"未命中走兜底价", "unknown", 1_000_000, 0, 1_000_000},
		// 向上取整：1 个 input token，单价 2.5/1M -> 2.5 个单位，取整为 3。
		{"不足百万向上取整", "gpt-4o", 1, 0, 3},
		// 输出 1 token，单价 10/1M -> 10 个单位。
		{"输出小额", "gpt-4o", 0, 1, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.Cost(tt.model, tt.in, tt.out)
			if got != tt.wantCost {
				t.Errorf("Cost(%s,%d,%d) = %d, want %d",
					tt.model, tt.in, tt.out, got, tt.wantCost)
			}
		})
	}
}

func TestPricingNilModels(t *testing.T) {
	// models 为 nil 时不应 panic，全部走兜底价。
	p := NewPricing(nil, 1_000_000, 1_000_000)
	if got := p.Cost("any", 1_000_000, 0); got != 1_000_000 {
		t.Errorf("兜底价计算错误: got %d", got)
	}
}

func TestStrictPricingRejectsUnknownModel(t *testing.T) {
	p := NewStrictPricingWithPolicies(map[string]ModelPolicy{
		"known": {
			ModelPrice:             ModelPrice{InputPer1M: 1, OutputPer1M: 1},
			MaxBillableInputTokens: 100, MaxOutputTokens: 10,
		},
	}, ModelPolicy{
		ModelPrice:             ModelPrice{InputPer1M: 1, OutputPer1M: 1},
		MaxBillableInputTokens: 100, MaxOutputTokens: 10,
	})
	if _, err := p.NormalizeMaxOutput("unknown", nil); !errors.Is(err, ErrUnknownModelPolicy) {
		t.Fatalf("生产严格模式必须拒绝未知模型，err=%v", err)
	}
	if _, err := p.NormalizeMaxOutput("known", nil); !errors.Is(err, ErrInvalidTokenLimit) {
		t.Fatalf("严格模式必须要求显式缓存单价，err=%v", err)
	}
}

func TestStrictPricingRequiresExplicitTokenBounds(t *testing.T) {
	prices := ModelPrice{
		InputPer1M: 1, OutputPer1M: 1,
		CacheCreationInputPer1M: 1, CacheReadInputPer1M: 1,
	}
	p := NewStrictPricingWithPolicies(map[string]ModelPolicy{
		"missing-input-bound":  {ModelPrice: prices, MaxOutputTokens: 10},
		"missing-output-bound": {ModelPrice: prices, MaxBillableInputTokens: 100},
	}, ModelPolicy{ModelPrice: prices, MaxBillableInputTokens: 100, MaxOutputTokens: 10})

	for _, model := range []string{"missing-input-bound", "missing-output-bound"} {
		if _, err := p.NormalizeMaxOutput(model, nil); !errors.Is(err, ErrInvalidTokenLimit) {
			t.Fatalf("release 严格策略必须拒绝缺少显式 token 上限的模型 %q: %v", model, err)
		}
	}
}

func TestPricingCheckedOverflow(t *testing.T) {
	p := NewPricing(map[string]ModelPrice{"m": {InputPer1M: math.MaxInt64, OutputPer1M: 1}}, 1, 1)
	if _, err := p.CostChecked("m", 2, 0); !errors.Is(err, ErrCostOverflow) {
		t.Fatalf("乘法溢出必须失败，err=%v", err)
	}
}

func TestPricingUsesIndependentCacheRatesAndReserveMaximum(t *testing.T) {
	p := NewPricingWithPolicies(map[string]ModelPolicy{
		"m": {
			ModelPrice: ModelPrice{
				InputPer1M: 1_000_000, OutputPer1M: 2_000_000,
				CacheCreationInputPer1M: 3_000_000, CacheReadInputPer1M: 500_000,
			},
			MaxBillableInputTokens: 100, MaxOutputTokens: 10,
		},
	}, ModelPolicy{})
	cost, err := p.CostUsageChecked("m", 10, 5, 4, 6)
	if err != nil {
		t.Fatal(err)
	}
	if cost != 35 { // 10*1 + 5*2 + 4*3 + ceil(6*0.5) = 35
		t.Fatalf("缓存独立计价=%d，期望 35", cost)
	}
	reserve, _, err := p.ReservationCost("m", 10)
	if err != nil {
		t.Fatal(err)
	}
	if reserve != 320 { // 全部 input cap 按最贵 cache creation 价 + output cap
		t.Fatalf("预授权未覆盖最贵缓存输入，得 %d want 320", reserve)
	}
}
