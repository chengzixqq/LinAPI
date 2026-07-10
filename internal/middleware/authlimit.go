package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// authlimit.go 为匿名认证端点（登录/注册）提供 bcrypt 之前的滥用防护（审查 AUD-P1-27）：
//   - IPRateLimiter：按来源 IP 的 Redis 令牌桶限流，堵住匿名在线撞库与 bcrypt CPU 耗尽。
//     复用 ratelimit.go 的 tokenBucketScript（同一原子令牌桶算法），仅换 key 维度为 IP。
//   - Semaphore：bcrypt 并发信号量，把在途哈希数卡在上限内；认证入口使用非阻塞
//     TryAcquire，容量满即返回 503，不让等待 handler/goroutine 无界堆积。
//
// 用户名维度预算必须与来源 IP 预算并存：单独按用户名硬锁会让攻击者锁死受害账户；
// 二者叠加则同时限制分布式撞库和单来源洪泛。Redis key 只保存归一化标识的摘要。

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

	bucketKey := "authlimit:" + rl.prefix + ":" + ip
	res, err := rl.script.Run(ctx, rl.rdb, []string{bucketKey},
		capacity, refill, 1).Int64Slice()
	if err != nil {
		return false, 0, 0, err
	}
	return res[0] == 1, int(res[1]), int(res[2]), nil
}

// IdentifierRateLimiter 按登录标识提供第二维预算。它不查询账户，因此存在与不存在
// 的用户名走完全相同的 Redis 操作；key 只含 SHA-256 摘要，避免把登录名泄露到 Redis。
type IdentifierRateLimiter struct {
	rdb    *redis.Client
	script *redis.Script
	prefix string
	perMin int
}

func NewIdentifierRateLimiter(rdb *redis.Client, prefix string, perMin int) *IdentifierRateLimiter {
	return &IdentifierRateLimiter{
		rdb: rdb, script: redis.NewScript(tokenBucketScript), prefix: prefix, perMin: perMin,
	}
}

// Allow 对 scope+identifier 消耗一个令牌。scope 由服务端常量提供（如 login/register）。
// perMin<=0 时直接放行；Redis 故障由调用方决定 fail-open 还是 fail-closed。
func (rl *IdentifierRateLimiter) Allow(ctx context.Context, scope, identifier string) (allowed bool, retryAfter int, err error) {
	if rl == nil || rl.perMin <= 0 {
		return true, 0, nil
	}
	normalized := strings.ToLower(strings.TrimSpace(identifier))
	digest := sha256.Sum256([]byte(normalized))
	bucketKey := "authlimit:" + rl.prefix + ":" + scope + ":" + hex.EncodeToString(digest[:])
	capacity := float64(rl.perMin)
	refill := capacity / 60.0
	res, err := rl.script.Run(ctx, rl.rdb, []string{bucketKey}, capacity, refill, 1).Int64Slice()
	if err != nil {
		return false, 0, err
	}
	return res[0] == 1, int(res[2]), nil
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

// TryAcquire 非阻塞占用一个槽；容量已满时立即返回 false。匿名认证入口应使用它，
// 避免攻击者用大量等待中的请求耗尽 handler/goroutine。
func (s *Semaphore) TryAcquire() bool {
	select {
	case s.slots <- struct{}{}:
		return true
	default:
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
