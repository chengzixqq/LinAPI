package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"linapi/internal/store"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func authTestStore() store.Store {
	return store.NewMemoryStore([]store.KeySeed{
		{APIKey: "sk-good", KeyID: "key-1", UserID: "u1", Enabled: true, InitialBalance: 100},
	})
}

// newAuthRouter 构建一个仅挂 Auth 中间件的路由，末端回显注入的身份。
func newAuthRouter() *gin.Engine {
	r := gin.New()
	r.Use(Auth(authTestStore()))
	r.GET("/probe", func(c *gin.Context) {
		id, ok := IdentityFrom(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"err": "身份未注入"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"user": id.UserID})
	})
	return r
}

func TestAuthBearerHeader(t *testing.T) {
	r := newAuthRouter()

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer sk-good")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Bearer 头有效密钥应放行, 状态码 %d, body=%s", w.Code, w.Body.String())
	}
}

func TestAuthXAPIKeyHeader(t *testing.T) {
	r := newAuthRouter()

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("x-api-key", "sk-good")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("x-api-key 头有效密钥应放行, 状态码 %d", w.Code)
	}
}

func TestAuthMissingKey(t *testing.T) {
	r := newAuthRouter()

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("缺少密钥应返回 401, 得到 %d", w.Code)
	}
}

func TestAuthInvalidKey(t *testing.T) {
	r := newAuthRouter()

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer sk-wrong")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("无效密钥应返回 401, 得到 %d", w.Code)
	}
}
