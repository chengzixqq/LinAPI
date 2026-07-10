package billing

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// balanceKeyPrefix 是用户余额热副本在 Redis 的 key 前缀：balance:{userID}。
const balanceKeyPrefix = "balance:"

// balanceTTL 是余额 key 的存活时间（秒）。每次读写续期，冷用户到期后从冷源重新 seed。
// 取 7 天，远大于任何请求周期，避免活跃用户余额被误逐出。
const balanceTTL = 7 * 24 * 3600

// settleFloor 是结算（退差/补收）时允许的余额下限。
// 取一个远小于任何合理余额的负值，表示「结算永远放行」——用户已实际消费，
// 即便补收导致余额为负也应记账（欠费下次充值补齐；下一请求的 Reserve 会因余额不足拦截）。
// 用 -(1<<50) 而非更小值：Redis 的 Lua 是 double 数值，需保持在 2^53 精度内。
const settleFloor int64 = -(1 << 50)

// adjustScript 原子地「惰性 seed + 校验下限 + 调整余额」，是预扣与退差的共同底座。
//
// KEYS[1]        = 余额 key（balance:{userID}）
// ARGV[1] delta  = 变动量（负=扣费/预扣，正=退回/充值）
// ARGV[2] seed   = key 不存在时的初始余额（来自冷源 store）
// ARGV[3] floor  = 变动后允许的最低余额；低于此值则拒绝、余额不变
// ARGV[4] ttl    = key 存活秒数
//
// 返回：{ok(1/0), balance}
//   - ok=1：调整成功，balance 为调整后余额
//   - ok=0：会突破下限（余额不足），已拒绝，balance 为调整前余额
//
// 惰性 seed 语义：仅当 key 不存在时用 seed 初始化，因此已扣费的热值不会被旧的
// 冷源初始值覆盖。充值同步（改动冷源后刷新 Redis）属于第 7 步职责。
const adjustScript = `
local key = KEYS[1]
local delta = tonumber(ARGV[1])
local seed = tonumber(ARGV[2])
local floor = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

if redis.call('EXISTS', key) == 0 then
  -- seed 时即带 TTL：余额不足会在 EXPIRE 之前提前 return，若此处不带 TTL 则留下
  -- 永久 key，后续冷源充值被陈旧热副本永久屏蔽（审查 AUD-P1-03）。
  redis.call('SET', key, seed, 'EX', ttl)
end

local bal = tonumber(redis.call('GET', key))
if bal + delta < floor then
  return {0, bal}
end

-- 用原字符串走 INCRBY，保证 64 位整数精确（不经 double 中转）。
local newbal = redis.call('INCRBY', key, ARGV[1])
redis.call('EXPIRE', key, ttl)
return {1, newbal}
`

// Account 是用户余额的 Redis 热副本，提供原子的预扣费与退差。
//
// 真相源在过渡期是内存 store、第 7 步后是 PostgreSQL；Account 通过惰性 seed
// 把权威初始余额搬进 Redis，之后所有扣减/退回都在 Redis 上原子完成，
// 支撑多实例一致的高并发计费。无状态，并发安全。
type Account struct {
	rdb    *redis.Client
	script *redis.Script
}

// NewAccount 创建 Redis 余额账户。
func NewAccount(rdb *redis.Client) *Account {
	return &Account{
		rdb:    rdb,
		script: redis.NewScript(adjustScript),
	}
}

// Reserve 原子预扣 amount（预授权）。seed 是 key 不存在时的初始余额（来自冷源）。
// 返回是否成功与预扣后的余额；余额不足（扣后 < 0）时 ok=false 且余额不变。
func (a *Account) Reserve(ctx context.Context, userID string, amount, seed int64) (ok bool, balance int64, err error) {
	// 预扣要求扣后余额不低于 0（闸门）。
	return a.adjust(ctx, userID, -amount, seed, 0)
}

// Settle 结算退差：delta = 预扣额 - 实际成本（正=退回押金，负=补收）。
// 结算永远放行（下限为 settleFloor），允许必要时轻微透支。
func (a *Account) Settle(ctx context.Context, userID string, delta, seed int64) (balance int64, err error) {
	_, balance, err = a.adjust(ctx, userID, delta, seed, settleFloor)
	return balance, err
}

// Sync 用冷源权威余额强制覆盖 Redis 热副本并续期。
//
// 用途：线上给用户充值（改冷源 balance）后，惰性 seed 不会触发（key 已存在），
// 必须主动调 Sync 把新余额刷进 Redis，否则热副本仍是旧值。
// 与 Reserve/Settle 的「惰性 seed 仅在 key 不存在时初始化」互补。
func (a *Account) Sync(ctx context.Context, userID string, balance int64) error {
	key := balanceKeyPrefix + userID
	return a.rdb.Set(ctx, key, balance, balanceTTL*time.Second).Err()
}

// adjust 执行一次原子调整。
func (a *Account) adjust(ctx context.Context, userID string, delta, seed, floor int64) (bool, int64, error) {
	key := balanceKeyPrefix + userID
	res, err := a.script.Run(ctx, a.rdb, []string{key},
		delta, seed, floor, balanceTTL).Int64Slice()
	if err != nil {
		return false, 0, err
	}
	return res[0] == 1, res[1], nil
}
