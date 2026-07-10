package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// newAdminEngine 挂一个受 AdminAuth 保护的探针端点。
func newAdminEngine(token string, loopbackOnly bool) *gin.Engine {
	e := gin.New()
	g := e.Group("/admin")
	g.Use(AdminAuth(token, loopbackOnly))
	g.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	return e
}

func TestAdminAuthValidToken(t *testing.T) {
	e := newAdminEngine("secret-token", false)
	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("有效令牌应 200, 得到 %d", w.Code)
	}
}

func TestAdminAuthInvalidToken(t *testing.T) {
	e := newAdminEngine("secret-token", false)
	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("错误令牌应 401, 得到 %d", w.Code)
	}
}

func TestAdminAuthMissingToken(t *testing.T) {
	e := newAdminEngine("secret-token", false)
	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("缺令牌应 401, 得到 %d", w.Code)
	}
}

func TestAdminAuthEmptyConfigRejectsAll(t *testing.T) {
	// token 未配置时应拒绝一切（即便请求也带了空 Bearer）。
	e := newAdminEngine("", false)
	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未配置令牌应拒绝, 得到 %d", w.Code)
	}
}

func TestAdminAuthLoopbackOnly(t *testing.T) {
	e := newAdminEngine("secret-token", true)

	// 非回环远端地址：应 403（在 token 校验之前拦截）。
	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.RemoteAddr = "203.0.113.7:5555"
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("非回环地址应 403, 得到 %d", w.Code)
	}

	// 回环远端地址 + 有效令牌：应放行。
	req2 := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req2.Header.Set("Authorization", "Bearer secret-token")
	req2.RemoteAddr = "127.0.0.1:5555"
	w2 := httptest.NewRecorder()
	e.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("回环地址+有效令牌应 200, 得到 %d", w2.Code)
	}
}
