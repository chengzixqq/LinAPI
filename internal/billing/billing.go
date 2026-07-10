package billing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	"linapi/internal/canonical"
)

// Billing 是计价与持久账本门面。资金正确性只依赖 Ledger；Redis 不再执行任何
// 增量扣款，因此重试、过期或全库丢失都不会改变权威余额。
type Billing struct {
	pricing        *Pricing
	ledger         Ledger
	defaultReserve int64
}

func New(pricing *Pricing, ledger Ledger, defaultReserve int64) *Billing {
	return &Billing{pricing: pricing, ledger: ledger, defaultReserve: defaultReserve}
}

type ReserveRequest struct {
	TraceID         string
	UserID          string
	KeyID           string
	Model           string
	MaxOutputTokens int
}

// NormalizeMaxOutput 校验客户端输出上限；缺失时返回必须写入上游请求的服务端上限。
func (b *Billing) NormalizeMaxOutput(model string, requested *int) (int, error) {
	return b.pricing.NormalizeMaxOutput(model, requested)
}

// ValidateModel 验证一个对外模型拥有非零价格与可证明的预授权边界。
func (b *Billing) ValidateModel(model string) error {
	maxOutput, err := b.pricing.NormalizeMaxOutput(model, nil)
	if err != nil {
		return err
	}
	_, _, err = b.pricing.ReservationCost(model, maxOutput)
	return err
}

// Reserve 按模型最大可计费输入和本次强制输出上限持久预授权。
func (b *Billing) Reserve(ctx context.Context, in ReserveRequest) (Reservation, bool, error) {
	amount, maxInput, err := b.pricing.ReservationCost(in.Model, in.MaxOutputTokens)
	if err != nil {
		return Reservation{}, false, err
	}
	if b.defaultReserve > amount {
		amount = b.defaultReserve
	}
	if amount <= 0 {
		return Reservation{}, false, fmt.Errorf("%w: 预授权金额必须为正", ErrInvalidTokenLimit)
	}
	id, err := newReservationID()
	if err != nil {
		return Reservation{}, false, err
	}
	r := Reservation{
		ID: id, TraceID: in.TraceID, UserID: in.UserID, KeyID: in.KeyID, Model: in.Model,
		Amount: amount, MaxInputTokens: maxInput, MaxOutputTokens: in.MaxOutputTokens,
		CreatedAt: time.Now().UTC(),
	}
	ok, err := b.ledger.Reserve(ctx, r)
	return r, ok, err
}

// Settle 先持久化“上游已消费”的事实，再执行可重试的最终结算。任一步失败时
// Reservation 都不会被外层当成可退款请求；RecordConsumption 成功后还可由 Recover
// 在重启时继续完成。
func (b *Billing) Settle(ctx context.Context, r Reservation, channel string, usage canonical.Usage) error {
	cost, input, output, complete, estimated := b.settlementCost(r, usage)
	if cost > r.Amount {
		return fmt.Errorf("%w: cost=%d reservation=%d", ErrReservationExceeded, cost, r.Amount)
	}
	c := Consumption{
		ReservationID: r.ID, Channel: channel,
		InputTokens: safeTokenValue(input), OutputTokens: safeTokenValue(output),
		CacheCreationInputTokens: safeTokenValue(usage.CacheCreationInputTokens),
		CacheReadInputTokens:     safeTokenValue(usage.CacheReadInputTokens),
		ReportedTotalTokens:      safeTokenValue(usage.ReportedTotalTokens),
		Cost:                     cost, UsageComplete: complete, Estimated: estimated,
		RecordedAt: time.Now().UTC(),
	}
	if err := b.recordConsumptionWithRetry(ctx, c); err != nil {
		return err
	}
	return b.ledger.Finalize(ctx, r.ID)
}

// recordConsumptionWithRetry 缩小瞬时 PostgreSQL/提交结果未知导致精确 usage 只留在
// 进程内存的窗口。Ledger 的 RecordConsumption 必须幂等；参数冲突/非法状态不重试。
func (b *Billing) recordConsumptionWithRetry(ctx context.Context, c Consumption) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := b.ledger.RecordConsumption(ctx, c); err == nil {
			return nil
		} else {
			lastErr = err
			if errors.Is(err, ErrReservationConflict) || errors.Is(err, ErrInvalidTransition) ||
				errors.Is(err, ErrReservationExceeded) {
				return err
			}
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(25*(attempt+1)) * time.Millisecond)
		select {
		case <-ctx.Done():
			_ = timer.Stop()
			return errors.Join(lastErr, ctx.Err())
		case <-timer.C:
		}
	}
	return lastErr
}

func (b *Billing) MarkInFlight(ctx context.Context, r Reservation, channel string) error {
	return b.ledger.MarkInFlight(ctx, r.ID, channel)
}

func (b *Billing) ReleaseAttempt(ctx context.Context, r Reservation) error {
	return b.ledger.ReleaseAttempt(ctx, r.ID)
}

func (b *Billing) Refund(ctx context.Context, r Reservation) error {
	return b.ledger.Refund(ctx, r.ID)
}

func (b *Billing) Recover(ctx context.Context) error {
	return b.ledger.Recover(ctx)
}

func (b *Billing) settlementCost(r Reservation, usage canonical.Usage) (cost int64, input, output int, complete, estimated bool) {
	input, output = usage.InputTokens, usage.OutputTokens
	inputKnown, outputKnown := usage.InputTokensKnown, usage.OutputTokensKnown
	cacheTotal, cacheOK := checkedUsageTokenSum(usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
	if !cacheOK {
		return r.Amount, input, output, false, true
	}

	if usage.TotalTokensKnown {
		total := usage.ReportedTotalTokens
		if total >= 0 && total < cacheTotal {
			return r.Amount, input, output, false, true
		}
		switch {
		case total < 0:
			return r.Amount, input, output, false, true
		case inputKnown && outputKnown:
			calculated, ok := checkedUsageTokenSum(input, cacheTotal, output)
			if !ok || calculated != total {
				return r.Amount, input, output, false, true
			}
		case inputKnown:
			known, ok := checkedUsageTokenSum(input, cacheTotal)
			if !ok || total < known {
				return r.Amount, input, output, false, true
			}
			output, outputKnown = total-known, true
		case outputKnown:
			known, ok := checkedUsageTokenSum(output, cacheTotal)
			if !ok || total < known {
				return r.Amount, input, output, false, true
			}
			input, inputKnown = total-known, true
		default:
			cost, err := b.pricing.ConservativeTotalCost(r.Model, total)
			if err != nil {
				return r.Amount, 0, 0, false, true
			}
			return cost, 0, 0, false, true
		}
	}

	if !inputKnown || !outputKnown || input < 0 || output < 0 {
		return r.Amount, input, output, false, true
	}
	cost, err := b.pricing.CostUsageChecked(r.Model, input, output,
		usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
	if err != nil {
		return r.Amount, input, output, false, true
	}
	return cost, input, output, true, false
}

func checkedUsageTokenSum(values ...int) (int, bool) {
	total := 0
	for _, value := range values {
		if value < 0 || total > math.MaxInt-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func safeTokenValue(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func newReservationID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("生成 reservation ID 失败: %w", err)
	}
	return "res_" + hex.EncodeToString(buf[:]), nil
}
