package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"linapi/internal/config"
)

func TestLivenessAndReadinessAreSeparated(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Mode = "test"
	cfg.Server.ReadTimeoutSeconds = 30
	cfg.Server.IdleTimeoutSeconds = 120
	s := New(cfg, Deps{Ready: func(context.Context) error { return errors.New("dependency down") }})
	if s.http.ReadTimeout.Seconds() != 30 || s.http.IdleTimeout.Seconds() != 120 {
		t.Fatalf("HTTP 超时未应用: read=%s idle=%s", s.http.ReadTimeout, s.http.IdleTimeout)
	}

	for _, path := range []string{"/healthz", "/livez"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		s.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, w.Code)
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("依赖失联时 /readyz status = %d, want 503", w.Code)
	}
}
