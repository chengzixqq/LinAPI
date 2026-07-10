package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"linapi/internal/billing"
	"linapi/internal/config"
	"linapi/internal/forwarder"
	"linapi/internal/routing"
	"linapi/internal/store"
)

func TestAnthropicEndpointPreForwarderErrorsUseAnthropicSchema(t *testing.T) {
	t.Run("401 auth", func(t *testing.T) {
		s, _ := newV1TestServer(t)
		w := performV1Request(s, "", validAnthropicRequest)
		assertAnthropicEndpointError(t, w, http.StatusUnauthorized, "authentication_error")
	})

	t.Run("413 body limit", func(t *testing.T) {
		cfg := baseProtocolTestConfig()
		cfg.Server.MaxRequestBodyBytes = 8
		s := New(cfg, Deps{})
		w := performV1Request(s, "", validAnthropicRequest)
		assertAnthropicEndpointError(t, w, http.StatusRequestEntityTooLarge, "invalid_request_error")
	})

	t.Run("429 ip limit", func(t *testing.T) {
		rdb := protocolTestRedis(t)
		cfg := baseProtocolTestConfig()
		cfg.Auth.UnauthenticatedRateLimitPerMin = 1
		s := New(cfg, Deps{Redis: rdb})
		_ = performV1Request(s, "", validAnthropicRequest)
		w := performV1Request(s, "", validAnthropicRequest)
		assertAnthropicEndpointError(t, w, http.StatusTooManyRequests, "rate_limit_error")
		if w.Header().Get("Retry-After") == "" {
			t.Fatal("限流错误必须保留 Retry-After")
		}
	})

	t.Run("429 account limit", func(t *testing.T) {
		rdb := protocolTestRedis(t)
		st, bill, fwd := protocolTestForwarder()
		cfg := baseProtocolTestConfig()
		cfg.Auth.AccountRateLimitPerMin = 1
		s := New(cfg, Deps{Store: st, Redis: rdb, Billing: bill, Forwarder: fwd})
		_ = performV1Request(s, "sk-test", validAnthropicRequest)
		w := performV1Request(s, "sk-test", validAnthropicRequest)
		assertAnthropicEndpointError(t, w, http.StatusTooManyRequests, "rate_limit_error")
	})

	t.Run("500 recovery", func(t *testing.T) {
		st := store.NewMemoryStore([]store.KeySeed{{
			APIKey: "sk-test", KeyID: "k1", UserID: "u1", Enabled: true, InitialBalance: 1_000_000,
		}})
		cfg := baseProtocolTestConfig()
		// nil Forwarder 的 Handler 可注册；请求进入时会 panic，由全局 Recovery 转换。
		s := New(cfg, Deps{Store: st, Forwarder: nil})
		w := performV1Request(s, "sk-test", validAnthropicRequest)
		assertAnthropicEndpointError(t, w, http.StatusInternalServerError, "internal_error")
	})
}

func TestOpenAIEndpointPreForwarderErrorKeepsOpenAISchema(t *testing.T) {
	s, _ := newV1TestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if _, hasAnthropicType := envelope["type"]; hasAnthropicType {
		t.Fatalf("OpenAI endpoint 不应输出 Anthropic envelope: %s", w.Body.String())
	}
	errBody, ok := envelope["error"].(map[string]any)
	if !ok || errBody["type"] != "authentication_error" {
		t.Fatalf("OpenAI error schema 不符: %s", w.Body.String())
	}
}

const validAnthropicRequest = `{"model":"claude","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

func baseProtocolTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Server.Mode = "test"
	cfg.Database.MaxOpenConns = 4
	return cfg
}

func protocolTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func protocolTestForwarder() (*store.MemoryStore, *billing.Billing, *forwarder.Forwarder) {
	st := store.NewMemoryStore([]store.KeySeed{{
		APIKey: "sk-test", KeyID: "k1", UserID: "u1", Enabled: true, InitialBalance: 1_000_000,
	}})
	bill := billing.New(billing.NewPricing(nil, 1_000_000, 1_000_000), billing.NewMemoryLedger(st), 100)
	fwd := forwarder.New(routing.NewRouter(nil, routing.BreakerConfig{}), bill, nil)
	return st, bill, fwd
}

func performV1Request(s *Server, apiKey, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	return w
}

func assertAnthropicEndpointError(t *testing.T, w *httptest.ResponseRecorder, status int, errType string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
	}
	var envelope struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("错误体不是 JSON: %v; body=%s", err, w.Body.String())
	}
	if envelope.Type != "error" || envelope.Error.Type != errType || envelope.Error.Message == "" {
		t.Fatalf("Anthropic 错误 schema 不符: %+v body=%s", envelope, w.Body.String())
	}
}
