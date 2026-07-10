package billing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"linapi/internal/canonical"
	"linapi/internal/store"
)

func newMemoryBilling(t *testing.T, balance int64) (*Billing, *MemoryLedger, *store.MemoryStore) {
	t.Helper()
	st := store.NewMemoryStore([]store.KeySeed{{
		APIKey: "sk-test", KeyID: "k1", UserID: "u1", Enabled: true, InitialBalance: balance,
	}})
	ledger := NewMemoryLedger(st)
	pricing := NewPricingWithPolicies(map[string]ModelPolicy{
		"gpt-test": {
			ModelPrice:             ModelPrice{InputPer1M: 1_000_000, OutputPer1M: 2_000_000},
			MaxBillableInputTokens: 100, MaxOutputTokens: 20,
		},
	}, ModelPolicy{
		ModelPrice:             ModelPrice{InputPer1M: 1_000_000, OutputPer1M: 2_000_000},
		MaxBillableInputTokens: 100, MaxOutputTokens: 20,
	})
	return New(pricing, ledger, 10), ledger, st
}

func reserveTest(t *testing.T, b *Billing) Reservation {
	t.Helper()
	r, ok, err := b.Reserve(context.Background(), ReserveRequest{
		TraceID: "trace-1", UserID: "u1", KeyID: "k1", Model: "gpt-test", MaxOutputTokens: 20,
	})
	if err != nil || !ok {
		t.Fatalf("预授权失败: ok=%v err=%v", ok, err)
	}
	if r.Amount != 140 { // input cap 100*1 + output cap 20*2
		t.Fatalf("预授权金额=%d，期望 140", r.Amount)
	}
	if err := b.MarkInFlight(context.Background(), r, "ch1"); err != nil {
		t.Fatalf("标记 in_flight 失败: %v", err)
	}
	return r
}

func TestBillingReserveSettleExact(t *testing.T) {
	b, ledger, st := newMemoryBilling(t, 1000)
	r := reserveTest(t, b)
	if err := b.Settle(context.Background(), r, "ch1", canonical.Usage{
		InputTokens: 10, OutputTokens: 5,
		InputTokensKnown: true, OutputTokensKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	bal, _ := st.Balance(context.Background(), "u1")
	if bal != 980 { // 实际成本 10 + 5*2 = 20
		t.Fatalf("结算后余额=%d，期望 980", bal)
	}
	snap, ok := ledger.Snapshot(r.ID)
	if !ok || snap.Status != ReservationSettled || snap.Consumption.Cost != 20 || snap.Consumption.Estimated {
		t.Fatalf("账本终态错误: %+v", snap)
	}
}

func TestBillingMissingUsageChargesReservationMaximum(t *testing.T) {
	b, ledger, st := newMemoryBilling(t, 1000)
	r := reserveTest(t, b)
	if err := b.Settle(context.Background(), r, "ch1", canonical.Usage{}); err != nil {
		t.Fatal(err)
	}
	bal, _ := st.Balance(context.Background(), "u1")
	if bal != 860 {
		t.Fatalf("缺 usage 必须保留全部预授权，余额=%d", bal)
	}
	snap, _ := ledger.Snapshot(r.ID)
	if !snap.Consumption.Estimated || snap.Consumption.UsageComplete || snap.Consumption.Cost != r.Amount {
		t.Fatalf("缺 usage 的审计字段错误: %+v", snap.Consumption)
	}
}

func TestBillingTotalOnlyUsesHigherPrice(t *testing.T) {
	b, _, st := newMemoryBilling(t, 1000)
	r := reserveTest(t, b)
	if err := b.Settle(context.Background(), r, "ch1", canonical.Usage{
		ReportedTotalTokens: 10, TotalTokensKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	bal, _ := st.Balance(context.Background(), "u1")
	if bal != 980 { // total 10 全按较高输出价 2 计费
		t.Fatalf("total-only 保守结算余额=%d，期望 980", bal)
	}
}

func TestBillingTotalOnlyRejectsCacheLargerThanTotal(t *testing.T) {
	b, ledger, st := newMemoryBilling(t, 1000)
	r := reserveTest(t, b)
	if err := b.Settle(context.Background(), r, "ch1", canonical.Usage{
		CacheReadInputTokens: 100,
		ReportedTotalTokens:  10, TotalTokensKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	bal, _ := st.Balance(context.Background(), "u1")
	if bal != 860 {
		t.Fatalf("cache token 超过 total 时必须保留全部预授权，余额=%d，期望 860", bal)
	}
	snap, _ := ledger.Snapshot(r.ID)
	if !snap.Consumption.Estimated || snap.Consumption.UsageComplete || snap.Consumption.Cost != r.Amount {
		t.Fatalf("冲突 usage 必须按预授权上限估算结算: %+v", snap.Consumption)
	}
}

func TestBillingReportedTotalIncludesCachedInput(t *testing.T) {
	b, ledger, st := newMemoryBilling(t, 1000)
	r := reserveTest(t, b)
	if err := b.Settle(context.Background(), r, "ch1", canonical.Usage{
		InputTokens: 20, OutputTokens: 10,
		InputTokensKnown: true, OutputTokensKnown: true,
		CacheReadInputTokens: 80,
		ReportedTotalTokens:  110, TotalTokensKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	bal, _ := st.Balance(context.Background(), "u1")
	if bal != 880 { // 普通 input 20 + cached input 80 + output 10*2 = 120
		t.Fatalf("含缓存的 total 应精确结算，余额=%d，期望 880", bal)
	}
	snap, _ := ledger.Snapshot(r.ID)
	if snap.Consumption.Estimated || snap.Consumption.Cost != 120 || snap.Consumption.CacheReadInputTokens != 80 {
		t.Fatalf("缓存 usage 账本记录错误: %+v", snap.Consumption)
	}
}

func TestBillingReserveInsufficient(t *testing.T) {
	b, _, st := newMemoryBilling(t, 139)
	_, ok, err := b.Reserve(context.Background(), ReserveRequest{
		UserID: "u1", KeyID: "k1", Model: "gpt-test", MaxOutputTokens: 20,
	})
	if err != nil || ok {
		t.Fatalf("余额不足应拒绝: ok=%v err=%v", ok, err)
	}
	bal, _ := st.Balance(context.Background(), "u1")
	if bal != 139 {
		t.Fatalf("失败预授权不得改余额，得 %d", bal)
	}
}

func TestMemoryLedgerConsumedCannotRefundAndRecover(t *testing.T) {
	b, ledger, st := newMemoryBilling(t, 1000)
	r := reserveTest(t, b)
	c := Consumption{ReservationID: r.ID, Channel: "ch1", Cost: 20}
	if err := ledger.RecordConsumption(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Refund(context.Background(), r.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("已消费 reservation 不得退款，err=%v", err)
	}
	if err := ledger.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	bal, _ := st.Balance(context.Background(), "u1")
	if bal != 980 {
		t.Fatalf("恢复结算后余额=%d，期望 980", bal)
	}
}

func TestMemoryLedgerConcurrentReserveNoOversell(t *testing.T) {
	_, ledger, st := newMemoryBilling(t, 1000)
	const amount = int64(100)
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := ledger.Reserve(context.Background(), Reservation{
				ID: "res-concurrent-" + string(rune('a'+i)), UserID: "u1", KeyID: "k1",
				Model: "gpt-test", Amount: amount, MaxOutputTokens: 1,
			})
			if err != nil {
				t.Errorf("并发预授权失败: %v", err)
				return
			}
			if ok {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if success != 10 {
		t.Fatalf("余额只够 10 份，实际成功 %d", success)
	}
	bal, _ := st.Balance(context.Background(), "u1")
	if bal != 0 {
		t.Fatalf("并发预授权后余额=%d，期望 0", bal)
	}
}
