package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"linapi/internal/account"
	"linapi/internal/store"
)

func newAccountConsoleEngine(t *testing.T) (*gin.Engine, account.AccountStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	accStore := account.NewMemoryStore(store.NewMemoryStore(nil))
	h := newAccountConsoleHandlers(accStore, accStore)
	e := gin.New()
	g := e.Group("/admin")
	g.GET("/accounts", h.listAccounts)
	g.POST("/accounts", h.createAccount)
	g.PATCH("/accounts/:id/enabled", h.setAccountEnabled)
	g.POST("/accounts/:id/password", h.resetPassword)
	g.GET("/settings", h.getSettings)
	g.PUT("/settings", h.putSettings)
	return e, accStore
}

func TestAdminCreateUserAccountWithInitialBalance(t *testing.T) {
	e, _ := newAccountConsoleEngine(t)
	body, _ := json.Marshal(gin.H{"username": "u1", "password": "password123", "role": "user", "initial_balance": 300})
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("建账户应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["role"] != "user" || got["external_id"] != "u1" {
		t.Fatalf("user 账户应有 external_id: %+v", got)
	}
}

func TestAdminCreateAccountRejectsBadRole(t *testing.T) {
	e, _ := newAccountConsoleEngine(t)
	body, _ := json.Marshal(gin.H{"username": "x", "password": "password123", "role": "superuser"})
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法角色应 400, 得到 %d", w.Code)
	}
}

func TestAdminAccountResponseHasNoPasswordHash(t *testing.T) {
	e, _ := newAccountConsoleEngine(t)
	body, _ := json.Marshal(gin.H{"username": "u2", "password": "password123", "role": "admin"})
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	// 先坐实请求成功，否则「错误响应恰好不含 password_hash」会让本测试假性通过。
	if w.Code != http.StatusCreated {
		t.Fatalf("建 admin 账户应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	if bytesContainsAny(w.Body.Bytes(), "password_hash", "PasswordHash") {
		t.Error("账户响应不得含 password_hash")
	}
	// 角色分流断言：admin 请求必须真的建成 admin（走 CreateAccount，无计费实体 external_id），
	// 而非被误路由进 CreateUserAccount 静默降级为 user。
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["role"] != "admin" {
		t.Fatalf("admin 请求应建成 role=admin, 得到 %v", got["role"])
	}
	if ext, _ := got["external_id"].(string); ext != "" {
		t.Fatalf("admin 账户不应有计费实体 external_id, 得到 %q", ext)
	}
}

// TestPutSettingsRejectsPositiveInitialBalance 是 P0-07 的纵深防御：既然自助注册
// 恒不发额度（register 固定传 0），new_user_initial_balance 已无正向语义。管理员
// 若把它设成正值，只会得到一个“看起来会发额度、实则被忽略”的脏配置（脚枪）——
// 直接在写入层拒绝正值，杜绝误配复活“注册送额度”的错觉。
func TestPutSettingsRejectsPositiveInitialBalance(t *testing.T) {
	e, _ := newAccountConsoleEngine(t)
	body, _ := json.Marshal(gin.H{"registration_enabled": true, "new_user_initial_balance": 1})
	req := httptest.NewRequest(http.MethodPut, "/admin/settings", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("正的 new_user_initial_balance 应 400, 得到 %d; body=%s", w.Code, w.Body.String())
	}
}

func TestAdminSettingsRoundTrip(t *testing.T) {
	e, _ := newAccountConsoleEngine(t)
	// 改设置。new_user_initial_balance 恒为 0（P0-07 后正值会被拒），此处验证
	// registration_enabled 开关与 0 额度能正常往返持久化。
	body, _ := json.Marshal(gin.H{"registration_enabled": true, "new_user_initial_balance": 0})
	req := httptest.NewRequest(http.MethodPut, "/admin/settings", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("改设置应 200, 得到 %d", w.Code)
	}
	// 读回。
	req = httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	var got account.Settings
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.RegistrationEnabled || got.NewUserInitialBalance != 0 {
		t.Fatalf("设置未持久化: %+v", got)
	}
}

// bytesContainsAny 报告 body 是否含任一子串。
func bytesContainsAny(b []byte, subs ...string) bool {
	s := string(b)
	for _, sub := range subs {
		if len(sub) > 0 && contains(s, sub) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
