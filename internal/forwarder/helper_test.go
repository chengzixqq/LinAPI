package forwarder

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	_ "linapi/internal/adapter/all" // 注册 openai / anthropic 适配器
	"linapi/internal/billing"
	"linapi/internal/middleware"
	"linapi/internal/routing"
	"linapi/internal/store"
)

func init() { gin.SetMode(gin.TestMode) }

// testEnv 打包一次集成测试所需的全部依赖。
type testEnv struct {
	engine  *gin.Engine
	billing *billing.Billing
	store   *store.MemoryStore
	rdb     *redis.Client
}

// newTestEnv 组装「鉴权 + 额度 + 转发」的完整链路，渠道指向给定的上游 URL。
// 每个渠道用 openai 格式，除非在 channels 里另行指定。
func newTestEnv(t *testing.T, channels []*routing.Channel, initialBalance int64) *testEnv {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	// 内存 Store：单个测试密钥，余额由参数给定，不限流不限模型。
	st := store.NewMemoryStore([]store.KeySeed{{
		APIKey:         "sk-test",
		KeyID:          "k-test",
		UserID:         "u-test",
		Enabled:        true,
		InitialBalance: initialBalance,
	}})

	pricing := billing.NewPricing(nil, 1_000_000, 2_000_000)
	acc := billing.NewAccount(rdb)
	rec := billing.NewRecorder(billing.NopSink{}, billing.RecorderConfig{FlushInterval: 10 * time.Millisecond}, nil)
	t.Cleanup(rec.Close)
	bill := billing.New(pricing, acc, rec, 5000) // 默认预扣 5000

	router := routing.NewRouter(channels, routing.DefaultBreakerConfig())
	fwd := New(router, bill, slog.New(slog.NewTextHandler(io.Discard, nil)))

	engine := gin.New()
	v1 := engine.Group("/v1")
	v1.Use(
		middleware.Auth(st),
		middleware.Quota(st, bill),
	)
	v1.POST("/chat/completions", fwd.Handler("openai"))
	v1.POST("/messages", fwd.Handler("anthropic"))

	return &testEnv{engine: engine, billing: bill, store: st, rdb: rdb}
}

// doRequest 发起一次带鉴权头的请求。
func (e *testEnv) doRequest(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// balanceOf 读取用户当前 Redis 热副本余额（key 形如 balance:{userID}）。
// 未预扣过则 key 不存在，返回 (0, false)。
func (e *testEnv) balanceOf(t *testing.T, userID string) (int64, bool) {
	t.Helper()
	v, err := e.rdb.Get(context.Background(), "balance:"+userID).Int64()
	if err == redis.Nil {
		return 0, false
	}
	if err != nil {
		t.Fatalf("读余额失败: %v", err)
	}
	return v, true
}

// openAIChannel 构造一个指向给定 URL 的 openai 格式渠道。
func openAIChannel(id, url string, priority int) *routing.Channel {
	return &routing.Channel{
		ID:       id,
		Name:     id,
		Format:   routing.FormatOpenAI,
		BaseURL:  url,
		APIKey:   "sk-upstream",
		Models:   map[string]string{"gpt-4o": ""},
		Priority: priority,
		Weight:   1,
		Enabled:  true,
	}
}

// mockUpstream 启动一个模拟上游 HTTP 服务，用给定 handler 响应。
func mockUpstream(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

const openAIChatResp = `{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "model": "gpt-4o-2024-08-06",
  "choices": [{"index":0,"message":{"role":"assistant","content":"你好！"},"finish_reason":"stop"}],
  "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
}`

const openAIChatReq = `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
