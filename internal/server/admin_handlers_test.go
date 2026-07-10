package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"linapi/internal/admin"
	"linapi/internal/middleware"
	"linapi/internal/store"
)

const testAdminToken = "test-admin-token"

// newAdminTestEngine 构建一个只挂 /admin 路由的 gin 引擎，复用真实的
// adminHandlers 与 AdminAuth 中间件，但不拉起 /v1（无需 Redis/Forwarder）。
// 返回引擎与底层 Service，便于测试直接预置数据或断言。
func newAdminTestEngine(t *testing.T) (*gin.Engine, *admin.Service) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	base := store.NewMemoryStore(nil)
	as := admin.NewMemoryStore(base, nil)
	svc := admin.NewService(as, nil, nil) // router 为 nil：本测试只验证 HTTP 层

	h := &adminHandlers{svc: svc}
	e := gin.New()
	g := e.Group("/admin")
	g.Use(middleware.AdminAuth(testAdminToken, false))
	{
		g.POST("/users", h.createUser)
		g.GET("/users", h.listUsers)
		g.GET("/users/:id", h.getUser)
		g.PATCH("/users/:id/enabled", h.setUserEnabled)
		g.POST("/users/:id/balance", h.addBalance)
		g.POST("/users/:id/keys", h.createKey)
		g.GET("/users/:id/keys", h.listKeys)
		g.PATCH("/keys/:keyid/enabled", h.setKeyEnabled)
		g.POST("/channels", h.createChannel)
		g.GET("/channels", h.listChannels)
		g.GET("/channels/:id", h.getChannel)
		g.PUT("/channels/:id", h.updateChannel)
		g.PATCH("/channels/:id/enabled", h.setChannelEnabled)
		g.DELETE("/channels/:id", h.deleteChannel)
	}
	return e, svc
}

// doAdmin 发起一次带管理令牌的请求，body 为 nil 时不带请求体。
func doAdmin(t *testing.T, e *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

// TestAdminAuthGuardsRoutes 无令牌应被 AdminAuth 拦截为 401。
func TestAdminAuthGuardsRoutes(t *testing.T) {
	e, _ := newAdminTestEngine(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401, 得到 %d", w.Code)
	}
}

// TestAdminUserLifecycle 覆盖用户创建→查询→启停→充值的 HTTP 全链路。
func TestAdminUserLifecycle(t *testing.T) {
	e, _ := newAdminTestEngine(t)

	// 创建。
	w := doAdmin(t, e, http.MethodPost, "/admin/users", gin.H{"external_id": "u1", "balance": 500})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建用户应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}

	// 缺 external_id 应 400（binding:"required"）。
	w = doAdmin(t, e, http.MethodPost, "/admin/users", gin.H{"balance": 1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 external_id 应 400, 得到 %d", w.Code)
	}

	// 重复创建应 409。
	w = doAdmin(t, e, http.MethodPost, "/admin/users", gin.H{"external_id": "u1"})
	if w.Code != http.StatusConflict {
		t.Fatalf("重复用户应 409, 得到 %d", w.Code)
	}

	// 查询。
	w = doAdmin(t, e, http.MethodGet, "/admin/users/u1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("查询用户应 200, 得到 %d", w.Code)
	}
	var u admin.User
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatalf("响应非用户 JSON: %v", err)
	}
	if u.Balance != 500 {
		t.Errorf("余额不符: %d", u.Balance)
	}

	// 查不存在的用户应 404。
	w = doAdmin(t, e, http.MethodGet, "/admin/users/ghost", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("查不存在用户应 404, 得到 %d", w.Code)
	}

	// 充值。
	w = doAdmin(t, e, http.MethodPost, "/admin/users/u1/balance", gin.H{"delta": 250})
	if w.Code != http.StatusOK {
		t.Fatalf("充值应 200, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	var bres struct {
		Balance int64 `json:"balance"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &bres)
	if bres.Balance != 750 {
		t.Errorf("充值后余额应 750, 得到 %d", bres.Balance)
	}

	// 禁用。
	w = doAdmin(t, e, http.MethodPatch, "/admin/users/u1/enabled", gin.H{"enabled": false})
	if w.Code != http.StatusOK {
		t.Fatalf("禁用应 200, 得到 %d", w.Code)
	}
}

// TestAdminKeyCreateEchoesPlaintextOnce 验证创建密钥仅回显一次明文，
// 且后续列表不含明文（只存摘要，对齐主流网关做法）。
func TestAdminKeyCreateEchoesPlaintextOnce(t *testing.T) {
	e, _ := newAdminTestEngine(t)

	// 先建用户。
	if w := doAdmin(t, e, http.MethodPost, "/admin/users", gin.H{"external_id": "u1"}); w.Code != http.StatusCreated {
		t.Fatalf("建用户失败: %d", w.Code)
	}

	// 建密钥：响应应含明文 api_key。
	w := doAdmin(t, e, http.MethodPost, "/admin/users/u1/keys", gin.H{"rate_limit_per_min": 60})
	if w.Code != http.StatusCreated {
		t.Fatalf("建密钥应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	apiKey, ok := created["api_key"].(string)
	if !ok || apiKey == "" {
		t.Fatalf("创建响应应含明文 api_key, 得到 %v", created["api_key"])
	}

	// 列表不应回显明文。
	w = doAdmin(t, e, http.MethodGet, "/admin/users/u1/keys", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("列密钥应 200, 得到 %d", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(apiKey)) {
		t.Errorf("密钥列表不应回显明文 api_key")
	}
	if bytes.Contains(w.Body.Bytes(), []byte("api_key")) {
		t.Errorf("密钥视图不应含 api_key 字段")
	}
}

// TestAdminChannelSanitizesAPIKey 验证渠道读取端点脱敏上游 api_key。
func TestAdminChannelSanitizesAPIKey(t *testing.T) {
	e, _ := newAdminTestEngine(t)

	body := gin.H{
		"channel_id": "c1", "format": "openai", "base_url": "https://up.example",
		"api_key": "sk-secret-upstream", "models": gin.H{"gpt-4o": ""},
		"priority": 10, "weight": 1, "enabled": true,
	}
	w := doAdmin(t, e, http.MethodPost, "/admin/channels", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("建渠道应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("sk-secret-upstream")) {
		t.Errorf("创建渠道响应不应回显上游 api_key")
	}

	// GET 也应脱敏。
	w = doAdmin(t, e, http.MethodGet, "/admin/channels/c1", nil)
	if bytes.Contains(w.Body.Bytes(), []byte("sk-secret-upstream")) {
		t.Errorf("GET 渠道不应回显上游 api_key")
	}

	// 非法 format 应 400。
	bad := gin.H{"channel_id": "c2", "format": "gemini", "base_url": "u"}
	w = doAdmin(t, e, http.MethodPost, "/admin/channels", bad)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 format 应 400, 得到 %d", w.Code)
	}
}

// TestAdminChannelDelete 验证删除渠道返回 204，再删返回 404。
func TestAdminChannelDelete(t *testing.T) {
	e, svc := newAdminTestEngine(t)
	// 预置一个渠道。
	if _, err := svc.CreateChannel(context.Background(), admin.ChannelInput{
		ChannelID: "c1", Format: "openai", BaseURL: "u", Weight: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("预置渠道失败: %v", err)
	}

	w := doAdmin(t, e, http.MethodDelete, "/admin/channels/c1", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("删除应 204, 得到 %d", w.Code)
	}
	w = doAdmin(t, e, http.MethodDelete, "/admin/channels/c1", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("再删应 404, 得到 %d", w.Code)
	}
}
