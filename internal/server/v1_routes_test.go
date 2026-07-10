package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"linapi/internal/billing"
	"linapi/internal/config"
	"linapi/internal/forwarder"
	"linapi/internal/routing"
	"linapi/internal/store"
)

// newV1TestServer 构建一个带真实 /v1 中间件链（Auth→RateLimit→Quota）与
// 空转发器的 Server，返回 Server、底层 store 与 Redis 客户端，便于观测余额热副本。
// defaultReserve 固定 100，便于断言"是否扣了押金"。
func newV1TestServer(t *testing.T) (*Server, *redis.Client) {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	st := store.NewMemoryStore([]store.KeySeed{
		{APIKey: "sk-test", KeyID: "k1", UserID: "u1", Enabled: true, InitialBalance: 1000},
	})

	pricing := billing.NewPricing(nil, 1_000_000, 1_000_000)
	acc := billing.NewAccount(rdb)
	rec := billing.NewRecorder(billing.NopSink{}, billing.RecorderConfig{}, nil)
	t.Cleanup(rec.Close)
	bill := billing.New(pricing, acc, rec, 100)

	// 空路由 → Models() 返回 []，但 /models 端点仍应可达且不扣押金。
	fwd := forwarder.New(routing.NewRouter(nil, routing.BreakerConfig{}), bill, nil)

	cfg := &config.Config{}
	cfg.Server.Mode = "test"
	deps := Deps{Store: st, Redis: rdb, Billing: bill, Forwarder: fwd}
	return New(cfg, deps), rdb
}

// TestModelsEndpointDoesNotConsumeQuota 验证 GET /v1/models 不预扣押金——
// 它不产生上游用量，却在原实现里与生成端点共用 Quota 中间件，导致每查一次
// 模型列表就永久扣掉一笔 default_reserve 不退（审查 AUD-P1-01）。
func TestModelsEndpointDoesNotConsumeQuota(t *testing.T) {
	s, rdb := newV1TestServer(t)
	ctx := context.Background()

	// 连续调用 /v1/models 多次。
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer sk-test")
		w := httptest.NewRecorder()
		s.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次 /v1/models 应 200, 得到 %d; body=%s", i+1, w.Code, w.Body.String())
		}
	}

	// 断言余额热副本未被扣减：/models 不该触发任何预扣。
	// 若曾预扣，balance:u1 会被 seed 为 1000 后扣成 <1000（且退款 guard 不覆盖只读端点）。
	bal, err := rdb.Get(ctx, "balance:u1").Result()
	if err == redis.Nil {
		// key 从未被创建 —— 最理想：/models 完全没碰计费。
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if bal != "1000" {
		t.Fatalf("/v1/models 不应扣押金，余额应保持 1000，得 %s", bal)
	}
}

// TestGenerationEndpointStillGuardedByQuota 是 P1-01 修复的反向断言：拆分中间件后，
// POST 生成端点必须仍经过 Quota 闸门——余额为 0 时应 402 拦截，绝不能把只读端点
// 修好却顺手拆掉了生成端点的押金闸门。
func TestGenerationEndpointStillGuardedByQuota(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	// 余额 0 的用户。
	st := store.NewMemoryStore([]store.KeySeed{
		{APIKey: "sk-broke", KeyID: "k1", UserID: "u1", Enabled: true, InitialBalance: 0},
	})
	pricing := billing.NewPricing(nil, 1_000_000, 1_000_000)
	rec := billing.NewRecorder(billing.NopSink{}, billing.RecorderConfig{}, nil)
	t.Cleanup(rec.Close)
	bill := billing.New(pricing, billing.NewAccount(rdb), rec, 100)
	fwd := forwarder.New(routing.NewRouter(nil, routing.BreakerConfig{}), bill, nil)

	cfg := &config.Config{}
	cfg.Server.Mode = "test"
	s := New(cfg, Deps{Store: st, Redis: rdb, Billing: bill, Forwarder: fwd})

	// 余额不足时，POST 生成端点应被 Quota 在打上游前拦截为 402。
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-broke")
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("余额不足时生成端点应被 Quota 拦截 402，得到 %d; body=%s", w.Code, w.Body.String())
	}
}
