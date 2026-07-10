package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRecoveryObservesPanicWithoutLoggingCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	r := gin.New()
	r.Use(RequestLogger(logger), Metrics(), Recovery(logger))
	r.GET("/panic", func(*gin.Context) { panic("secret panic payload") })

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	req.Header.Set("Cookie", "linapi_session=secret-cookie")
	req.Header.Set("x-api-key", "secret-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	out := logs.String()
	if !strings.Contains(out, "request_panic") || !strings.Contains(out, "http_request") || !strings.Contains(out, `"status":500`) {
		t.Fatalf("panic 与 500 访问日志均应存在: %s", out)
	}
	for _, secret := range []string{"secret-cookie", "secret-key", "secret panic payload"} {
		if strings.Contains(out, secret) {
			t.Fatalf("恢复日志泄露凭证或 panic 值 %q: %s", secret, out)
		}
	}
}
