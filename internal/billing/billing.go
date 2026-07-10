package billing

import (
	"context"
	"time"
)

// Billing 是计费门面，聚合计价、Redis 账户与异步用量记录，
// 向转发层提供「预扣 → 结算」两个原子步骤。并发安全。
type Billing struct {
	pricing  *Pricing
	account  *Account
	recorder *Recorder

	// defaultReserve 是无法预估时的默认预扣额（最小计费单位）。
	// 预扣是「押金」，Settle 时按真实用量退差，故可略高以覆盖长回复。
	defaultReserve int64
}

// New 构建计费门面。
func New(pricing *Pricing, account *Account, recorder *Recorder, defaultReserve int64) *Billing {
	return &Billing{
		pricing:        pricing,
		account:        account,
		recorder:       recorder,
		defaultReserve: defaultReserve,
	}
}

// Reservation 是一次预扣的句柄，转发完成后据此结算。
type Reservation struct {
	UserID string
	KeyID  string
	Model  string
	// Amount 是已预扣的押金额。
	Amount int64
	// Seed 是预扣时用于惰性初始化 Redis 的冷源余额（供 Settle 复用）。
	Seed int64
}

// Reserve 在请求转发前预扣押金。seed 是该用户在冷源（store）的权威余额，
// 仅当 Redis 尚无该用户余额时用于初始化。返回预扣句柄；余额不足时 ok=false。
func (b *Billing) Reserve(ctx context.Context, userID, keyID, model string, seed int64) (Reservation, bool, error) {
	amount := b.defaultReserve
	ok, _, err := b.account.Reserve(ctx, userID, amount, seed)
	if err != nil {
		return Reservation{}, false, err
	}
	if !ok {
		return Reservation{}, false, nil
	}
	return Reservation{
		UserID: userID,
		KeyID:  keyID,
		Model:  model,
		Amount: amount,
		Seed:   seed,
	}, true, nil
}

// Settle 在转发完成后按真实用量结算：算出实际成本，退回「押金 - 成本」的差额
// （成本超押金则补收），并异步记一条用量日志。
//
// channel 是实际命中的上游渠道 ID，requestID 用于对账/幂等。
func (b *Billing) Settle(ctx context.Context, r Reservation, channel, requestID string, inputTokens, outputTokens int) error {
	cost := b.pricing.Cost(r.Model, inputTokens, outputTokens)

	// 退差：押金 - 实际成本。正=退回多扣的，负=补收不足的。
	delta := r.Amount - cost
	if _, err := b.account.Settle(ctx, r.UserID, delta, r.Seed); err != nil {
		return err
	}

	b.recorder.Record(UsageRecord{
		RequestID:    requestID,
		UserID:       r.UserID,
		KeyID:        r.KeyID,
		Model:        r.Model,
		Channel:      channel,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Cost:         cost,
		CreatedAt:    time.Now(),
	})
	return nil
}

// Refund 在预扣后、转发彻底失败（未产生任何用量）时全额退回押金。
// 转发层在所有候选渠道都失败、没有可计费用量时调用。
func (b *Billing) Refund(ctx context.Context, r Reservation) error {
	_, err := b.account.Settle(ctx, r.UserID, r.Amount, r.Seed)
	return err
}

// SyncBalance 用冷源权威余额刷新 Redis 热副本。
// 充值（改冷源余额）后必须调用，否则热副本仍是旧值（惰性 seed 不覆盖已存在的 key）。
func (b *Billing) SyncBalance(ctx context.Context, userID string, balance int64) error {
	return b.account.Sync(ctx, userID, balance)
}
