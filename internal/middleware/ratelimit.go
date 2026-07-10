package middleware

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// tokenBucketScript 是原子令牌桶的 Lua 脚本，在 Redis 服务端原子执行，
// 避免「读令牌数 -> 判断 -> 写回」之间的竞态（分布式多实例下尤为关键）。
//
// KEYS[1]           = 桶的 key
// ARGV[1] capacity  = 桶容量（每分钟允许的请求数）
// ARGV[2] refill    = 每秒补充的令牌数（capacity/60）
// ARGV[3] requested = 本次请求消耗的令牌数（通常为 1）
//
// 返回：{allowed(1/0), remaining(取整), retry_after_seconds(取整向上)}
//
// 桶状态以 hash 存储：tokens=当前令牌数，ts=上次补充时间戳。
// 惰性补充：每次请求按距上次的时间差补充令牌，无需后台定时任务。
const tokenBucketScript = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill = tonumber(ARGV[2])
local clock = redis.call('TIME')
local now = tonumber(clock[1]) + tonumber(clock[2]) / 1000000
local requested = tonumber(ARGV[3])

local bucket = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(bucket[1])
local ts = tonumber(bucket[2])

if tokens == nil then
  tokens = capacity
  ts = now
end

-- 惰性补充：按经过的时间补令牌，上限为容量。
local elapsed = math.max(0, now - ts)
tokens = math.min(capacity, tokens + elapsed * refill)

local allowed = 0
local retry_after = 0
if tokens >= requested then
  allowed = 1
  tokens = tokens - requested
else
  -- 距离攒够所需令牌还要多久。
  retry_after = math.ceil((requested - tokens) / refill)
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
-- 设置过期：桶从满到空最长 capacity/refill 秒，加倍冗余以防提前失忆。
local ttl = math.ceil(capacity / refill) * 2
redis.call('EXPIRE', key, ttl)

return {allowed, math.floor(tokens), retry_after}
`

// RateLimiter 基于 Redis 令牌桶做分布式限流。
type RateLimiter struct {
	rdb    *redis.Client
	script *redis.Script
}

// NewRateLimiter 创建限流器。
func NewRateLimiter(rdb *redis.Client) *RateLimiter {
	return &RateLimiter{
		rdb:    rdb,
		script: redis.NewScript(tokenBucketScript),
	}
}

// Middleware 返回限流中间件。必须挂在 Auth 之后（依赖已注入的身份）。
// 身份的 RateLimitPerMin <= 0 时视为不限流，直接放行。
func (rl *RateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := IdentityFrom(c)
		if !ok {
			// 理论上 Auth 已保证身份存在；防御性处理。
			abortError(c, http.StatusUnauthorized, "authentication_error", "未鉴权")
			return
		}
		if id.RateLimitPerMin <= 0 {
			c.Next()
			return
		}

		allowed, remaining, retryAfter, err := rl.allow(c.Request.Context(), "key:"+id.KeyID, id.RateLimitPerMin)
		if err != nil {
			// Redis 故障时的取舍：放行而非拦截（fail-open），避免限流组件抖动
			// 直接打挂整个网关；额度中间件仍会兜住余额。
			c.Next()
			return
		}

		c.Header("X-RateLimit-Limit", strconv.Itoa(id.RateLimitPerMin))
		c.Header("X-RateLimit-Remaining", strconv.Itoa(remaining))

		if !allowed {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			abortError(c, http.StatusTooManyRequests, "rate_limit_error",
				"请求过于频繁，请稍后重试")
			return
		}
		c.Next()
	}
}

// AccountMiddleware 在单 Key 限流外增加账户级总预算，防止用户创建多把 Key 线性叠加
// 平台吞吐。应挂在 Auth 之后、单 Key RateLimiter 之前。
func (rl *RateLimiter) AccountMiddleware(perMin int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if perMin <= 0 {
			c.Next()
			return
		}
		id, ok := IdentityFrom(c)
		if !ok {
			abortError(c, http.StatusUnauthorized, "authentication_error", "未鉴权")
			return
		}
		allowed, remaining, retryAfter, err := rl.allow(c.Request.Context(), "account:"+id.UserID, perMin)
		if err != nil {
			c.Next()
			return
		}
		c.Header("X-Account-RateLimit-Limit", strconv.Itoa(perMin))
		c.Header("X-Account-RateLimit-Remaining", strconv.Itoa(remaining))
		if !allowed {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			abortError(c, http.StatusTooManyRequests, "rate_limit_error", "账户请求过于频繁，请稍后重试")
			return
		}
		c.Next()
	}
}

// allow 执行一次令牌桶判定。
func (rl *RateLimiter) allow(ctx context.Context, bucketID string, perMin int) (allowed bool, remaining, retryAfter int, err error) {
	capacity := float64(perMin)
	refill := capacity / 60.0

	bucketKey := "ratelimit:" + bucketID
	res, err := rl.script.Run(ctx, rl.rdb, []string{bucketKey},
		capacity, refill, 1).Int64Slice()
	if err != nil {
		return false, 0, 0, err
	}
	return res[0] == 1, int(res[1]), int(res[2]), nil
}
