package billing

import "testing"

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
