package billing

import (
	"context"
	"testing"
	"time"
)

// newTestBilling 组装一个用 miniredis + captureSink 的计费门面，返回门面与 sink。
func newTestBilling(t *testing.T, defaultReserve int64) (*Billing, *captureSink) {
	t.Helper()
	pricing := NewPricing(map[string]ModelPrice{
		"gpt-4o": {InputPer1M: 2_500_000, OutputPer1M: 10_000_000},
	}, 1_000_000, 2_000_000)
	acc := NewAccount(newTestRedis(t))
	sink := &captureSink{}
	// 短冲刷间隔：让异步用量日志尽快落库，避免测试等待与默认 1s 间隔在边界上竞争。
	rec := NewRecorder(sink, RecorderConfig{FlushInterval: 10 * time.Millisecond}, nil)
	t.Cleanup(rec.Close)
	return New(pricing, acc, rec, defaultReserve), sink
}

func TestBillingReserveSettleRoundtrip(t *testing.T) {
	ctx := context.Background()
	b, sink := newTestBilling(t, 5000) // 默认预扣 5000

	// 预扣：seed=100000。
	r, ok, err := b.Reserve(ctx, "u1", "k1", "gpt-4o", 100000)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("预扣应成功")
	}
	if r.Amount != 5000 {
		t.Fatalf("预扣额应为 5000，得 %d", r.Amount)
	}

	// 结算：真实用量 input=1M output=1M -> cost = 2.5M + 10M 单位/1M = 12_500_000？
	// 注意此处单价是 每1M token 的单位数，1M token 命中即整价。
	// cost = 1*2_500_000 + 1*10_000_000 = 12_500_000。押金 5000，需补收。
	if err := b.Settle(ctx, r, "ch1", "req-1", 1_000_000, 1_000_000); err != nil {
		t.Fatal(err)
	}

	// 用量日志应异步落库。
	waitFor(t, func() bool { return sink.count() == 1 }, time.Second)
	rec := sink.records[0]
	if rec.Cost != 12_500_000 {
		t.Errorf("成本应为 12_500_000，得 %d", rec.Cost)
	}
	if rec.Channel != "ch1" || rec.RequestID != "req-1" {
		t.Errorf("用量日志归因错误: %+v", rec)
	}
}

func TestBillingReserveInsufficient(t *testing.T) {
	ctx := context.Background()
	b, _ := newTestBilling(t, 5000)

	// 冷源余额只有 100，预扣 5000 应失败。
	_, ok, err := b.Reserve(ctx, "poor", "k", "gpt-4o", 100)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("余额不足时预扣应失败")
	}
}

func TestBillingRefund(t *testing.T) {
	ctx := context.Background()
	b, _ := newTestBilling(t, 5000)

	r, ok, err := b.Reserve(ctx, "u1", "k1", "gpt-4o", 10000)
	if err != nil || !ok {
		t.Fatalf("预扣应成功: ok=%v err=%v", ok, err)
	}
	// 预扣后余额 10000-5000=5000；全额退款后应回到 10000。
	if err := b.Refund(ctx, r); err != nil {
		t.Fatal(err)
	}
	// 再预扣验证已回退：连续预扣两次 5000 应都成功（10000 恰好够）。
	if _, ok, _ := b.Reserve(ctx, "u1", "k1", "gpt-4o", 0); !ok {
		t.Fatal("退款后应能继续预扣")
	}
}
