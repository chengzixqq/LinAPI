package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "linapi/internal/adapter/all"
	"linapi/internal/billing"
	"linapi/internal/config"
	"linapi/internal/forwarder"
	"linapi/internal/routing"
	"linapi/internal/store"
)

// newV1TestServer 构建真实 /v1 中间件链；预授权由 Forwarder 在解析请求后执行。
func newV1TestServer(t *testing.T) (*Server, *store.MemoryStore) {
	t.Helper()

	st := store.NewMemoryStore([]store.KeySeed{
		{APIKey: "sk-test", KeyID: "k1", UserID: "u1", Enabled: true, InitialBalance: 1000},
	})

	pricing := billing.NewPricing(nil, 1_000_000, 1_000_000)
	bill := billing.New(pricing, billing.NewMemoryLedger(st), 100)

	// 空路由 → Models() 返回 []，但 /models 端点仍应可达且不扣押金。
	fwd := forwarder.New(routing.NewRouter(nil, routing.BreakerConfig{}), bill, nil)

	cfg := &config.Config{}
	cfg.Server.Mode = "test"
	deps := Deps{Store: st, Billing: bill, Forwarder: fwd}
	return New(cfg, deps), st
}

// TestModelsEndpointDoesNotConsumeBilling 验证 GET /v1/models 不触发账本预授权——
// 它不产生上游用量，却在原实现里与生成端点共用 Quota 中间件，导致每查一次
// 模型列表就永久扣掉一笔 default_reserve 不退（审查 AUD-P1-01）。
func TestModelsEndpointDoesNotConsumeBilling(t *testing.T) {
	s, st := newV1TestServer(t)
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

	// /models 不进入 Forwarder，不应触碰权威余额。
	bal, err := st.Balance(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if bal != 1000 {
		t.Fatalf("/v1/models 不应扣押金，余额应保持 1000，得 %d", bal)
	}
}

// TestGenerationEndpointStillGuardedByBilling 是 P1-01 修复的反向断言：生成端点
// 必须在上游 I/O 前完成最大成本预授权，余额为 0 时返回 402。
func TestGenerationEndpointStillGuardedByBilling(t *testing.T) {
	// 余额 0 的用户。
	st := store.NewMemoryStore([]store.KeySeed{
		{APIKey: "sk-broke", KeyID: "k1", UserID: "u1", Enabled: true, InitialBalance: 0},
	})
	pricing := billing.NewPricing(nil, 1_000_000, 1_000_000)
	bill := billing.New(pricing, billing.NewMemoryLedger(st), 100)
	channel := &routing.Channel{
		ID: "c1", Format: routing.FormatOpenAI, BaseURL: "http://127.0.0.1:1",
		Models: map[string]string{"gpt-4o": ""}, Weight: 1, Enabled: true,
	}
	fwd := forwarder.New(routing.NewRouter([]*routing.Channel{channel}, routing.BreakerConfig{}), bill, nil)

	cfg := &config.Config{}
	cfg.Server.Mode = "test"
	s := New(cfg, Deps{Store: st, Billing: bill, Forwarder: fwd})

	// 余额不足时，POST 生成端点应在打上游前拦截为 402。
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"gpt-4o","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-broke")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("余额不足时生成端点应被计费预授权拦截 402，得到 %d; body=%s", w.Code, w.Body.String())
	}
}
