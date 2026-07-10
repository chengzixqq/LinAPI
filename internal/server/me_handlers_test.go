package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"linapi/internal/admin"
	"linapi/internal/session"
	"linapi/internal/store"
)

// meTestCtx 造一个带会话身份的 /me 引擎；sessionExt 是当前登录用户的 external_id。
func newMeTestEngine(t *testing.T, sessionExt string) (*gin.Engine, *admin.Service) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	base := store.NewMemoryStore(nil)
	as := admin.NewMemoryStore(base, nil)
	svc := admin.NewService(as, nil, nil)
	// 预置两个用户（当前登录者 + 他人），供越权测试。
	_, _ = as.CreateUser(context.Background(), admin.CreateUserInput{ExternalID: sessionExt, Enabled: true})
	_, _ = as.CreateUser(context.Background(), admin.CreateUserInput{ExternalID: "other", Enabled: true})

	h := newMeHandlers(svc, base)
	e := gin.New()
	// 测试用中间件：直接注入固定会话身份（跳过真实 SessionAuth）。
	inject := func(c *gin.Context) {
		c.Set("linapi.session", session.SessionData{
			AccountID: 1, Username: "me", Role: "user", ExternalID: sessionExt,
		})
		c.Next()
	}
	g := e.Group("/me", inject)
	g.GET("/profile", h.profile)
	g.GET("/keys", h.listKeys)
	g.POST("/keys", h.createKey)
	g.PATCH("/keys/:keyid/enabled", h.setKeyEnabled)
	g.DELETE("/keys/:keyid", h.deleteKey)
	return e, svc
}

func TestMeCreateKeyBindsToSession(t *testing.T) {
	e, svc := newMeTestEngine(t, "me")
	// 即便请求体塞 user_id=other，也必须绑定到会话的 "me"。
	body, _ := json.Marshal(gin.H{"user_id": "other", "external_id": "other", "rate_limit_per_min": 60})
	req := httptest.NewRequest(http.MethodPost, "/me/keys", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("建 key 应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	// 断言 key 落在 "me" 名下，而非 "other"。
	meKeys, _ := svc.Store().ListAPIKeysByUser(context.Background(), "me")
	otherKeys, _ := svc.Store().ListAPIKeysByUser(context.Background(), "other")
	if len(meKeys) != 1 || len(otherKeys) != 0 {
		t.Fatalf("key 必须绑定会话用户 me: me=%d other=%d", len(meKeys), len(otherKeys))
	}
}

func TestMeCannotTouchOthersKey(t *testing.T) {
	e, svc := newMeTestEngine(t, "me")
	// 直接给 "other" 建一把 key。
	gen, _ := admin.GenerateKey()
	_, _ = svc.Store().CreateAPIKey(context.Background(), admin.CreateAPIKeyInput{
		APIKey: gen.APIKey, KeyID: "other-key", UserID: "other", Enabled: true,
	})

	// 会话是 "me"，尝试禁用他人 key -> 404。
	body, _ := json.Marshal(gin.H{"enabled": false})
	req := httptest.NewRequest(http.MethodPatch, "/me/keys/other-key/enabled", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("操作他人 key 应 404, 得到 %d", w.Code)
	}

	// 尝试删他人 key -> 404。
	req = httptest.NewRequest(http.MethodDelete, "/me/keys/other-key", nil)
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("删他人 key 应 404, 得到 %d", w.Code)
	}
}

func TestMeProfileReturnsBalance(t *testing.T) {
	e, svc := newMeTestEngine(t, "me")
	_, _ = svc.Store().AddBalance(context.Background(), "me", 888)

	req := httptest.NewRequest(http.MethodGet, "/me/profile", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("profile 应 200, 得到 %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["external_id"] != "me" {
		t.Fatalf("profile external_id 不符: %+v", got)
	}
	// 余额必须为会话用户 me 的充值额 888（JSON 数字反序列化为 float64）。
	if got["balance"] != float64(888) {
		t.Fatalf("profile balance 应为 888, 得到 %v", got["balance"])
	}
}

// TestMeListKeysReturnsOwnOnly 验证 GET /me/keys 只返回会话用户自己的 key，且不含明文。
func TestMeListKeysReturnsOwnOnly(t *testing.T) {
	e, svc := newMeTestEngine(t, "me")
	// me 一把、other 一把。
	genMe, _ := admin.GenerateKey()
	_, _ = svc.Store().CreateAPIKey(context.Background(), admin.CreateAPIKeyInput{
		APIKey: genMe.APIKey, KeyID: "me-key", UserID: "me", Enabled: true,
	})
	genOther, _ := admin.GenerateKey()
	_, _ = svc.Store().CreateAPIKey(context.Background(), admin.CreateAPIKeyInput{
		APIKey: genOther.APIKey, KeyID: "other-key", UserID: "other", Enabled: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/me/keys", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("列 key 应 200, 得到 %d", w.Code)
	}
	// 只应含自己的 me-key，绝不含他人 other-key。
	body := w.Body.String()
	if !strings.Contains(body, "me-key") {
		t.Fatalf("应含自己的 me-key: %s", body)
	}
	if strings.Contains(body, "other-key") {
		t.Fatalf("绝不应含他人 other-key（越权泄露）: %s", body)
	}
	// 不得回显明文 api_key。
	if strings.Contains(body, genMe.APIKey) {
		t.Fatalf("列表不应回显明文 api_key")
	}
}

// TestMeFailsClosedWithoutSession 验证无会话（未挂 SessionAuth/ext 为空）时自助端点 fail-closed 返回 401，
// 而非以空身份静默返回默认数据。
func TestMeFailsClosedWithoutSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := store.NewMemoryStore(nil)
	as := admin.NewMemoryStore(base, nil)
	svc := admin.NewService(as, nil, nil)
	h := newMeHandlers(svc, base)

	// 刻意不注入任何会话（模拟漏挂 SessionAuth 或空 ext）。
	e := gin.New()
	g := e.Group("/me")
	g.GET("/profile", h.profile)
	g.GET("/keys", h.listKeys)

	for _, path := range []string{"/me/profile", "/me/keys"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s 无会话应 401(fail-closed), 得到 %d", path, w.Code)
		}
	}
}
