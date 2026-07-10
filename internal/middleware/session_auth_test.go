package middleware

import (
	"context"
	"errors"
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

// TestSessionAuthRejectsExpiredSession 验证会话失效（token 不存在）时返回 401
func TestSessionAuthRejectsExpiredSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := newSessionManager(t)

	// 先创建再删除，或直接用一个不存在的随机 token
	invalidToken := "0000000000000000000000000000000000000000"

	r := gin.New()
	r.Use(SessionAuth(m))
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: invalidToken})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("会话失效应 401, 得到 %d", w.Code)
	}
}

// TestSessionAuthRedisErrorFailsClosed 验证 Redis 异常时返回 503（fail-closed）
func TestSessionAuthRedisErrorFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	m := session.NewManager(rdb)

	// 关闭 miniredis 使后续 redis 命令失败（模拟 Redis 异常）
	mr.Close()

	r := gin.New()
	r.Use(SessionAuth(m))
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "anytoken"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("Redis 异常应 503, 得到 %d", w.Code)
	}
}

// TestRequireRoleWithoutSession 验证未挂 SessionAuth 时 RequireRole 返回 401
func TestRequireRoleWithoutSession(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	// 只挂 RequireRole，不挂 SessionAuth
	r.Use(RequireRole("admin"))
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无会话应 401, 得到 %d", w.Code)
	}
}

// TestSessionAuthWithVersionAcceptsMatch 验证带会话代次校验时，会话快照 version 与账户
// 当前 version 一致则放行（审查 AUD-P1-17）。
func TestSessionAuthWithVersionAcceptsMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := newSessionManager(t)
	token, _ := m.Create(context.Background(), session.SessionData{
		AccountID: 7, Username: "alice", Role: "user", SessionVersion: 3,
	}, session.DefaultTTL)

	// 账户当前 version=3，与会话快照一致。
	checker := SessionVersionCheckerFunc(func(_ context.Context, id int64) (int, error) {
		if id != 7 {
			t.Fatalf("应按会话 AccountID=7 回查, 得到 %d", id)
		}
		return 3, nil
	})

	r := gin.New()
	r.Use(SessionAuthWithVersion(m, checker))
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("代次一致应 200, 得到 %d", w.Code)
	}
}

// TestSessionAuthWithVersionRejectsStale 验证账户 version 已递增（禁用/改密）后，
// 持旧代次快照的会话被拒 401，且该会话被主动删除，杜绝旧 Cookie 继续可用
// （审查 AUD-P1-17）。
func TestSessionAuthWithVersionRejectsStale(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := newSessionManager(t)
	token, _ := m.Create(context.Background(), session.SessionData{
		AccountID: 7, Username: "alice", Role: "user", SessionVersion: 3,
	}, session.DefaultTTL)

	// 账户当前 version=4（已因禁用/改密递增），旧会话快照 3 应作废。
	checker := SessionVersionCheckerFunc(func(_ context.Context, _ int64) (int, error) {
		return 4, nil
	})

	r := gin.New()
	r.Use(SessionAuthWithVersion(m, checker))
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("代次过期应 401, 得到 %d", w.Code)
	}
	// 过期会话应被主动删除（下次拿同 token 反查已不存在）。
	if _, err := m.Get(context.Background(), token); err == nil {
		t.Fatal("代次过期的会话应被主动删除")
	}
}

// TestSessionAuthWithVersionFailsClosedOnCheckerError 验证代次回查出错（账户库异常）时
// fail-closed 返回 503，而非放行——绝不能因回查失败就跳过代次校验（审查 AUD-P1-17）。
func TestSessionAuthWithVersionFailsClosedOnCheckerError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := newSessionManager(t)
	token, _ := m.Create(context.Background(), session.SessionData{
		AccountID: 7, Username: "alice", Role: "user", SessionVersion: 3,
	}, session.DefaultTTL)

	checker := SessionVersionCheckerFunc(func(_ context.Context, _ int64) (int, error) {
		return 0, errors.New("账户库暂时不可用")
	})

	r := gin.New()
	r.Use(SessionAuthWithVersion(m, checker))
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("代次回查出错应 fail-closed 503, 得到 %d", w.Code)
	}
}
