package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// newIPLimiterEngine 造一个挂了 IP 限流中间件的引擎，perMin 为每 IP 每分钟配额。
func newIPLimiterEngine(t *testing.T, perMin int) (*gin.Engine, *miniredis.Miniredis) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	rl := NewIPRateLimiter(rdb, "auth", perMin)
	e := gin.New()
	e.Use(rl.Middleware())
	e.POST("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	return e, mr
}

// TestIPRateLimiterBlocksAfterBurst 验证同一来源 IP 超过每分钟配额后被 429 拦截。
// 这是 P1-27 的核心：匿名登录/注册端点在 bcrypt 前先按来源 IP 限速，堵住撞库与 CPU 耗尽。
func TestIPRateLimiterBlocksAfterBurst(t *testing.T) {
	const perMin = 3
	e, _ := newIPLimiterEngine(t, perMin)

	// 前 perMin 次应放行。
	for i := 0; i < perMin; i++ {
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.RemoteAddr = "203.0.113.7:12345"
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次应放行 200, 得到 %d", i+1, w.Code)
		}
	}
	// 第 perMin+1 次应被限流。
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.RemoteAddr = "203.0.113.7:12345"
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("超配额应 429, 得到 %d", w.Code)
	}
}

// TestIPRateLimiterPerIPIsolation 验证配额按 IP 隔离：A 打满不影响 B。
// 关键安全属性——不能因某个 IP 触限就殃及其它来源（也印证不是按用户名硬锁）。
func TestIPRateLimiterPerIPIsolation(t *testing.T) {
	const perMin = 2
	e, _ := newIPLimiterEngine(t, perMin)

	// A 打满配额。
	for i := 0; i < perMin+1; i++ {
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.RemoteAddr = "198.51.100.1:9999"
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
	}
	// B 首次请求仍应放行。
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.RemoteAddr = "198.51.100.2:9999"
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("独立 IP 不应受他人配额影响, 得到 %d", w.Code)
	}
}

// TestIPRateLimiterFailOpenOnRedisDown 验证 Redis 故障时 fail-open 放行。
// 与业务限流器一致：限流依赖抖动不应打挂整个登录入口（可用性优先，且账户/密码校验仍在）。
func TestIPRateLimiterFailOpenOnRedisDown(t *testing.T) {
	e, mr := newIPLimiterEngine(t, 1)
	mr.Close() // 关闭 Redis 使脚本执行失败。

	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.RemoteAddr = "203.0.113.9:1000"
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Redis 故障应 fail-open 放行 200, 得到 %d", w.Code)
	}
}

// TestBcryptSemaphoreLimitsConcurrency 验证 bcrypt 并发信号量把在途 goroutine 数
// 卡在容量以内：占满后再 Acquire 必须阻塞，直到有 Release。防匿名并发登录把 bcrypt
// goroutine 撑到无界、耗尽 CPU。
func TestBcryptSemaphoreLimitsConcurrency(t *testing.T) {
	const cap = 2
	sem := NewSemaphore(cap)

	// 占满容量。
	for i := 0; i < cap; i++ {
		if !sem.Acquire(context.Background()) {
			t.Fatalf("第 %d 次 Acquire 应成功", i+1)
		}
	}

	// 容量已满：带超时的 Acquire 必须阻塞并超时失败。
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if sem.Acquire(ctx) {
		t.Fatal("容量已满时 Acquire 应阻塞并超时返回 false")
	}

	// 释放一个后应能再获取。
	sem.Release()
	if !sem.Acquire(context.Background()) {
		t.Fatal("释放后 Acquire 应成功")
	}
}

// TestBcryptSemaphoreConcurrentInvariant 用并发压力验证任一时刻在途数不超过容量。
func TestBcryptSemaphoreConcurrentInvariant(t *testing.T) {
	const cap = 4
	sem := NewSemaphore(cap)

	var mu sync.Mutex
	inFlight, maxSeen := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !sem.Acquire(context.Background()) {
				return
			}
			defer sem.Release()
			mu.Lock()
			inFlight++
			if inFlight > maxSeen {
				maxSeen = inFlight
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if maxSeen > cap {
		t.Fatalf("在途 goroutine 峰值 %d 超过信号量容量 %d", maxSeen, cap)
	}
}
