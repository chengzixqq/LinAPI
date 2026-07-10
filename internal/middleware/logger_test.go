package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"linapi/internal/store"
)

// newLoggerEngine 构建挂了 RequestLogger 的测试引擎，日志写入返回的 buffer，
// 便于断言结构化字段。skip 指定跳过记录的路径。
func newLoggerEngine(t *testing.T, skip ...string) (*gin.Engine, *bytes.Buffer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := gin.New()
	e.Use(RequestLogger(logger, skip...))
	return e, &buf
}

// parseLogLine 解析 buffer 中的单条 JSON 日志。空则失败。
func parseLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	if buf.Len() == 0 {
		t.Fatalf("期望有日志输出，实际为空")
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("日志非合法 JSON: %v; 原文=%s", err, buf.String())
	}
	return m
}

// TestRequestLoggerAssignsRequestID 未带 X-Request-Id 时应生成 ID，
// 注入 context 并回填响应头，且日志含该 ID。
func TestRequestLoggerAssignsRequestID(t *testing.T) {
	e, buf := newLoggerEngine(t)
	var fromCtx string
	e.GET("/x", func(c *gin.Context) {
		id, ok := RequestIDFrom(c)
		if !ok || id == "" {
			t.Errorf("handler 内应能取到非空 request_id")
		}
		fromCtx = id
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	if got := w.Header().Get("X-Request-Id"); got == "" || got != fromCtx {
		t.Errorf("响应头 X-Request-Id=%q 应与 context 内 %q 一致且非空", got, fromCtx)
	}
	m := parseLogLine(t, buf)
	if m["request_id"] != fromCtx {
		t.Errorf("日志 request_id=%v 应为 %q", m["request_id"], fromCtx)
	}
	if m["msg"] != "http_request" {
		t.Errorf("日志 msg=%v 应为 http_request", m["msg"])
	}
}

// TestRequestLoggerReusesInboundID 带 X-Request-Id 时应复用之，便于跨服务串联。
func TestRequestLoggerReusesInboundID(t *testing.T) {
	e, buf := newLoggerEngine(t)
	e.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-Id", "req_inbound_123")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if got := w.Header().Get("X-Request-Id"); got != "req_inbound_123" {
		t.Errorf("应复用入站 request_id，得到 %q", got)
	}
	if m := parseLogLine(t, buf); m["request_id"] != "req_inbound_123" {
		t.Errorf("日志应复用入站 request_id，得到 %v", m["request_id"])
	}
}

// TestRequestLoggerSkipPaths skip 路径不应产生日志（但仍分配 request_id 头）。
func TestRequestLoggerSkipPaths(t *testing.T) {
	e, buf := newLoggerEngine(t, "/healthz")
	e.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if buf.Len() != 0 {
		t.Errorf("skip 路径不应记日志，实际输出: %s", buf.String())
	}
	if w.Header().Get("X-Request-Id") == "" {
		t.Errorf("skip 路径仍应回填 X-Request-Id 响应头")
	}
}

// TestRequestLoggerBackfillsFields 验证 SetLog* 回填的模型/渠道/用量与身份进入日志。
func TestRequestLoggerBackfillsFields(t *testing.T) {
	e, buf := newLoggerEngine(t)
	e.GET("/v1/x", func(c *gin.Context) {
		// 模拟 Auth 中间件注入身份。
		c.Set(ctxKeyIdentity, &store.Identity{UserID: "u1", KeyID: "k1"})
		SetLogModel(c, "gpt-4o")
		SetLogUpstream(c, "chan-1")
		SetLogUsage(c, 12, 34)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	_ = w

	m := parseLogLine(t, buf)
	checks := map[string]any{
		"model":         "gpt-4o",
		"channel":       "chan-1",
		"user_id":       "u1",
		"key_id":        "k1",
		"input_tokens":  float64(12), // JSON 数字解析为 float64
		"output_tokens": float64(34),
	}
	for k, want := range checks {
		if m[k] != want {
			t.Errorf("日志字段 %s=%v，期望 %v", k, m[k], want)
		}
	}
}

// TestRequestLoggerLevelByStatus 5xx→error、4xx→warn、2xx→info。
func TestRequestLoggerLevelByStatus(t *testing.T) {
	cases := []struct {
		status    int
		wantLevel string
	}{
		{http.StatusOK, "INFO"},
		{http.StatusBadRequest, "WARN"},
		{http.StatusInternalServerError, "ERROR"},
	}
	for _, tc := range cases {
		e, buf := newLoggerEngine(t)
		e.GET("/x", func(c *gin.Context) { c.Status(tc.status) })
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		_ = w
		m := parseLogLine(t, buf)
		if m["level"] != tc.wantLevel {
			t.Errorf("status=%d 期望日志级别 %s，得到 %v", tc.status, tc.wantLevel, m["level"])
		}
		if m["status"] != float64(tc.status) {
			t.Errorf("日志 status=%v 应为 %d", m["status"], tc.status)
		}
	}
}

// TestSetLogNoMiddlewareNoPanic 未挂 RequestLogger 时 SetLog* 应为无操作、不 panic。
func TestSetLogNoMiddlewareNoPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	// 无 accessLog 载体：以下调用应静默返回。
	SetLogModel(c, "m")
	SetLogUpstream(c, "ch")
	SetLogUsage(c, 1, 2)
	if _, ok := RequestIDFrom(c); ok {
		t.Errorf("未挂中间件时不应有 request_id")
	}
}
