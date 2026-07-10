package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"linapi/internal/session"
)

func newSessionManager(t *testing.T) *session.Manager {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return session.NewManager(rdb)
}

func TestSessionAuthRejectsNoCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := newSessionManager(t)
	r := gin.New()
	r.Use(SessionAuth(m))
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无 Cookie 应 401, 得到 %d", w.Code)
	}
}

func TestSessionAuthAcceptsValidCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := newSessionManager(t)
	token, _ := m.Create(context.Background(), session.SessionData{
		AccountID: 1, Username: "alice", Role: "user", ExternalID: "alice",
	}, session.DefaultTTL)

	r := gin.New()
	r.Use(SessionAuth(m))
	r.GET("/probe", func(c *gin.Context) {
		s, ok := SessionFrom(c)
		if !ok || s.Username != "alice" {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("有效 Cookie 应 200, 得到 %d", w.Code)
	}
}

func TestRequireRoleForbidsMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := newSessionManager(t)
	token, _ := m.Create(context.Background(), session.SessionData{
		AccountID: 1, Username: "u", Role: "user",
	}, session.DefaultTTL)

	r := gin.New()
	r.Use(SessionAuth(m), RequireRole("admin"))
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("user 访问 admin 路由应 403, 得到 %d", w.Code)
	}
}
