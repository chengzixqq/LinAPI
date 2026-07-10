package middleware

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	_ "linapi/internal/adapter/all"
)

func TestProtocolContextSelectsEndpointErrorSchema(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ProtocolContext(
		ProtocolRoute{Method: http.MethodPost, Path: "/v1/messages", Protocol: ProtocolAnthropic},
	))
	engine.POST("/v1/messages", func(c *gin.Context) {
		abortError(c, http.StatusUnauthorized, "authentication_error", "missing key")
	})
	engine.POST("/admin/action", func(c *gin.Context) {
		abortError(c, http.StatusForbidden, "permission_error", "denied")
	})

	anthropic := httptest.NewRecorder()
	engine.ServeHTTP(anthropic, httptest.NewRequest(http.MethodPost, "/v1/messages?trace=1", nil))
	assertProtocolError(t, anthropic, "anthropic", http.StatusUnauthorized, "authentication_error")

	admin := httptest.NewRecorder()
	engine.ServeHTTP(admin, httptest.NewRequest(http.MethodPost, "/admin/action", nil))
	assertProtocolError(t, admin, "openai", http.StatusForbidden, "permission_error")
}

func TestRecoveryUsesProtocolContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ProtocolContext(
		ProtocolRoute{Method: http.MethodPost, Path: "/v1/messages", Protocol: ProtocolAnthropic},
	))
	engine.Use(Recovery(slog.New(slog.NewTextHandler(io.Discard, nil))))
	engine.POST("/v1/messages", func(*gin.Context) { panic("boom") })

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	assertProtocolError(t, w, "anthropic", http.StatusInternalServerError, "internal_error")
}

func assertProtocolError(t *testing.T, w *httptest.ResponseRecorder, protocol string, status int, errType string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是 JSON: %v; body=%s", err, w.Body.String())
	}
	if protocol == "anthropic" {
		if body["type"] != "error" {
			t.Fatalf("Anthropic envelope 不符: %s", w.Body.String())
		}
	}
	errBody, ok := body["error"].(map[string]any)
	if !ok || errBody["type"] != errType {
		t.Fatalf("error.type 不符: %s", w.Body.String())
	}
}
