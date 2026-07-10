package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"linapi/internal/session"
)

// newCSRFEngine 造一个先注入会话（含固定 CSRFToken）、再挂 CSRFProtect 的引擎。
// injectCSRF 为会话里存的 token；设为 "" 模拟会话无 CSRF token。
func newCSRFEngine(t *testing.T, sessionCSRF string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(func(c *gin.Context) {
		c.Set(ctxKeySession, session.SessionData{
			AccountID: 1, Username: "u", Role: "admin", CSRFToken: sessionCSRF,
		})
		c.Next()
	})
	e.Use(CSRFProtect())
	e.POST("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	e.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	return e
}

// TestCSRFAllowsMatchingToken 合法写请求：JSON + 同源 Origin + 正确 X-CSRF-Token → 放行。
func TestCSRFAllowsMatchingToken(t *testing.T) {
	e := newCSRFEngine(t, "good-token")
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", "good-token")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("合法带 token 写请求应 200, 得到 %d", w.Code)
	}
}

// TestCSRFRejectsMissingToken 无 X-CSRF-Token → 403（攻击者跨站无法读取受害者 csrf cookie）。
func TestCSRFRejectsMissingToken(t *testing.T) {
	e := newCSRFEngine(t, "good-token")
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("缺 CSRF token 应 403, 得到 %d", w.Code)
	}
}

// TestCSRFRejectsMismatchedToken X-CSRF-Token 与会话不符 → 403。
func TestCSRFRejectsMismatchedToken(t *testing.T) {
	e := newCSRFEngine(t, "good-token")
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", "wrong-token")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("CSRF token 不符应 403, 得到 %d", w.Code)
	}
}

// TestCSRFRejectsNonJSON 非 application/json（如攻击页可用的 text/plain 表单）→ 403，
// 即便带了正确 token 也拒绝——挡住无需预检的简单请求构造。
func TestCSRFRejectsNonJSON(t *testing.T) {
	e := newCSRFEngine(t, "good-token")
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-CSRF-Token", "good-token")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("非 JSON Content-Type 应 403, 得到 %d", w.Code)
	}
}

// TestCSRFRejectsCrossOrigin Origin 与 Host 不同源 → 403（同站异源攻击页）。
func TestCSRFRejectsCrossOrigin(t *testing.T) {
	e := newCSRFEngine(t, "good-token")
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", "good-token")
	req.Header.Set("Origin", "http://evil.example.com")
	req.Host = "api.example.com"
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("跨源 Origin 应 403, 得到 %d", w.Code)
	}
}

// TestCSRFSkipsSafeMethods GET 等安全方法不校验（无副作用，且需支持前端读取）。
func TestCSRFSkipsSafeMethods(t *testing.T) {
	e := newCSRFEngine(t, "good-token")
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	// 刻意不带任何 CSRF token / JSON 头。
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("安全方法 GET 不应被 CSRF 拦截, 得到 %d", w.Code)
	}
}

// TestCSRFFailsClosedWithoutSession 未注入会话（漏挂 SessionAuth）时写请求 403，
// 绝不放行——fail-closed。
func TestCSRFFailsClosedWithoutSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(CSRFProtect()) // 刻意不注入会话。
	e.POST("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", "anything")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("无会话写请求应 403(fail-closed), 得到 %d", w.Code)
	}
}
