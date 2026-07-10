package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// authlimit.go 为匿名认证端点（登录/注册）提供 bcrypt 之前的滥用防护（审查 AUD-P1-27）：
//   - IPRateLimiter：按来源 IP 的 Redis 令牌桶限流，堵住匿名在线撞库与 bcrypt CPU 耗尽。
//     复用 ratelimit.go 的 tokenBucketScript（同一原子令牌桶算法），仅换 key 维度为 IP。
//   - Semaphore：bcrypt 并发信号量，把在途哈希 goroutine 数卡在上限内，防止并发登录
//     把 goroutine 撑到无界。
//
// 刻意不按用户名硬锁：那会让攻击者构造失败登录锁死受害账户（拒绝服务）。限速维度是
// 来源 IP + 全局并发，存在与不存在的用户名共享同一预算，不泄露账户是否存在。

// IPRateLimiter 基于 Redis 令牌桶，按来源 IP 对匿名端点限流。
type IPRateLimiter struct {
	rdb    *redis.Client
	script *redis.Script
	prefix string // Redis key 前缀，区分不同端点组（如 "auth"）。
	perMin int    // 每 IP 每分钟允许的请求数。
}

// NewIPRateLimiter 创建按 IP 限流的中间件工厂。perMin<=0 时中间件直接放行（不限流）。
func NewIPRateLimiter(rdb *redis.Client, prefix string, perMin int) *IPRateLimiter {
	return &IPRateLimiter{
		rdb:    rdb,
		script: redis.NewScript(tokenBucketScript),
		prefix: prefix,
		perMin: perMin,
	}
}

// Middleware 返回按来源 IP 限流的中间件。应挂在匿名认证端点最前（bcrypt 之前）。
func (rl *IPRateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if rl.perMin <= 0 {
			c.Next()
			return
		}
		allowed, _, retryAfter, err := rl.allow(c.Request.Context(), c.ClientIP())
		if err != nil {
			// Redis 故障：fail-open 放行，与业务限流器一致——限流依赖抖动不应打挂
			// 整个登录入口，账户/密码校验仍是最后防线。
			c.Next()
			return
		}
		if !allowed {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			abortError(c, http.StatusTooManyRequests, "rate_limit_error",
				"请求过于频繁，请稍后重试")
			return
		}
		c.Next()
	}
}

// allow 执行一次按 IP 的令牌桶判定。
func (rl *IPRateLimiter) allow(ctx context.Context, ip string) (allowed bool, remaining, retryAfter int, err error) {
	capacity := float64(rl.perMin)
	refill := capacity / 60.0
	now := float64(time.Now().UnixNano()) / 1e9

	bucketKey := "authlimit:" + rl.prefix + ":" + ip
	res, err := rl.script.Run(ctx, rl.rdb, []string{bucketKey},
		capacity, refill, now, 1).Int64Slice()
	if err != nil {
		return false, 0, 0, err
	}
	return res[0] == 1, int(res[1]), int(res[2]), nil
}

// Semaphore 是一个带容量的计数信号量，用于限制 bcrypt 等 CPU 密集操作的并发度。
// 底层是带缓冲 channel：Acquire 占一个槽，Release 释放。支持 context 取消/超时。
type Semaphore struct {
	slots chan struct{}
}

// NewSemaphore 创建容量为 cap 的信号量。cap<=0 视为 1（至少允许一个在途，避免死锁）。
func NewSemaphore(cap int) *Semaphore {
	if cap <= 0 {
		cap = 1
	}
	return &Semaphore{slots: make(chan struct{}, cap)}
}

// Acquire 占用一个槽；容量已满时阻塞，直到有 Release 或 ctx 结束。
// 返回 true 表示获取成功（此时调用方必须在用完后 Release）；ctx 取消/超时返回 false。
func (s *Semaphore) Acquire(ctx context.Context) bool {
	select {
	case s.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// Release 释放一个槽。必须与成功的 Acquire 配对，多释放会 panic（暴露配对错误）。
func (s *Semaphore) Release() {
	select {
	case <-s.slots:
	default:
		panic("middleware: Semaphore.Release 无对应 Acquire（释放次数超过获取次数）")
	}
}
