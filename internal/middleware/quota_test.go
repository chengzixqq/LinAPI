package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"linapi/internal/billing"
	"linapi/internal/store"
)

// newTestBilling 构建一个由内存 miniredis 支撑的计费门面，默认预扣 100。
func newTestBilling(t *testing.T) *billing.Billing {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	pricing := billing.NewPricing(nil, 1_000_000, 1_000_000)
	acc := billing.NewAccount(rdb)
	rec := billing.NewRecorder(billing.NopSink{}, billing.RecorderConfig{}, nil)
	t.Cleanup(rec.Close)
	return billing.New(pricing, acc, rec, 100)
}

// newQuotaRouter 构建 Auth -> Quota 的路由；Quota 依赖 Auth 注入的身份。
func newQuotaRouter(t *testing.T, s store.Store) *gin.Engine {
	r := gin.New()
	r.Use(Auth(s), Quota(s, newTestBilling(t)))
	r.GET("/probe", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func TestQuotaSufficientBalance(t *testing.T) {
	s := store.NewMemoryStore([]store.KeySeed{
		{APIKey: "sk-rich", KeyID: "k", UserID: "rich", Enabled: true, InitialBalance: 1000},
	})
	r := newQuotaRouter(t, s)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer sk-rich")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("余额充足应放行, 状态码 %d", w.Code)
	}
}

func TestQuotaZeroBalance(t *testing.T) {
	s := store.NewMemoryStore([]store.KeySeed{
		{APIKey: "sk-broke", KeyID: "k", UserID: "broke", Enabled: true, InitialBalance: 0},
	})
	r := newQuotaRouter(t, s)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer sk-broke")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Errorf("余额为 0 应返回 402, 得到 %d", w.Code)
	}
}
