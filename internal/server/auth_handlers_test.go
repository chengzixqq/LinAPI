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

func TestLoginCookieAttributes(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	_, _ = accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "dave", PasswordHash: hash, Role: account.RoleAdmin,
	})

	body, _ := json.Marshal(gin.H{"username": "dave", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, 得到 %d", w.Code)
	}

	var sessionCookie *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == middleware.CookieName {
			sessionCookie = ck
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("未找到会话 Cookie")
	}
	if !sessionCookie.HttpOnly {
		t.Fatal("会话 Cookie 应为 HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("会话 Cookie SameSite 应为 Strict, 得到 %v", sessionCookie.SameSite)
	}
}

func TestLoginDisabledAccount(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	acc, err := accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "eve", PasswordHash: hash, Role: account.RoleUser,
	})
	if err != nil {
		t.Fatalf("建账户失败: %v", err)
	}
	if _, err := accStore.SetEnabled(context.Background(), acc.ID, false); err != nil {
		t.Fatalf("禁用账户失败: %v", err)
	}

	body, _ := json.Marshal(gin.H{"username": "eve", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("禁用账户登录应 401, 得到 %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("用户名或密码错误")) {
		t.Fatalf("禁用账户的错误消息应与密码错误一致, 得到 body=%s", w.Body.String())
	}
}

func TestLogoutClearsSession(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	_, _ = accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "frank", PasswordHash: hash, Role: account.RoleAdmin,
	})

	login, _ := json.Marshal(gin.H{"username": "frank", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(login))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, 得到 %d", w.Code)
	}
	cookies := w.Result().Cookies()

	req = httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登出应 200, 得到 %d; body=%s", w.Code, w.Body.String())
	}

	// 同一个 Cookie 再访问 /auth/me，会话应已被删除。
	req = httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("登出后 /auth/me 应 401, 得到 %d", w.Code)
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
