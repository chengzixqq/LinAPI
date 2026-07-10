package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"linapi/internal/store"
)

func TestAccountRateLimitIsSharedAcrossKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	rl := NewRateLimiter(rdb)

	r := gin.New()
	request := 0
	r.Use(func(c *gin.Context) {
		request++
		c.Set(ctxKeyIdentity, &store.Identity{UserID: "same-user", KeyID: "key-" + string(rune('0'+request))})
		c.Next()
	}, rl.AccountMiddleware(2))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	for i, want := range []int{http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != want {
			t.Fatalf("请求 %d status=%d want=%d", i+1, w.Code, want)
		}
	}
}
