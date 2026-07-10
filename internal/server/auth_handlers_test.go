package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"linapi/internal/account"
	"linapi/internal/middleware"
	"linapi/internal/session"
	"linapi/internal/store"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// newAuthTestEngine 构建挂了 /auth 的 gin 引擎，返回引擎与底层依赖。
func newAuthTestEngine(t *testing.T) (*gin.Engine, account.AccountStore, *session.Manager) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sess := session.NewManager(rdb)

	accStore := account.NewMemoryStore(store.NewMemoryStore(nil))
	h := newAuthHandlers(accStore, accStore, sess, false)

	e := gin.New()
	g := e.Group("/auth")
	g.POST("/register", h.register)
	g.POST("/login", h.login)
	g.POST("/logout", middleware.SessionAuth(sess), h.logout)
	g.GET("/me", middleware.SessionAuth(sess), h.me)
	return e, accStore, sess
}

func TestRegisterDisabledByDefault(t *testing.T) {
	e, _, _ := newAuthTestEngine(t)
	body, _ := json.Marshal(gin.H{"username": "alice", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("默认注册关闭应 403, 得到 %d", w.Code)
	}
}

func TestRegisterWhenEnabledThenLogin(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	// 打开注册开关。
	_ = accStore.(*account.MemoryStore).Put(context.Background(), account.Settings{RegistrationEnabled: true})

	body, _ := json.Marshal(gin.H{"username": "alice", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("开启后注册应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}

	// 登录应下发 Cookie。
	req = httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, 得到 %d", w.Code)
	}
	if len(w.Result().Cookies()) == 0 {
		t.Fatal("登录应下发会话 Cookie")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	_, _ = accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "bob", PasswordHash: hash, Role: account.RoleAdmin,
	})

	body, _ := json.Marshal(gin.H{"username": "bob", "password": "wrongpass"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401, 得到 %d", w.Code)
	}
}

func TestMeReturnsIdentity(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	_, _ = accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "carol", PasswordHash: hash, Role: account.RoleAdmin,
	})
	login, _ := json.Marshal(gin.H{"username": "carol", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(login))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	cookies := w.Result().Cookies()

	req = httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/auth/me 应 200, 得到 %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["username"] != "carol" || got["role"] != "admin" {
		t.Fatalf("me 身份不符: %+v", got)
	}
}
