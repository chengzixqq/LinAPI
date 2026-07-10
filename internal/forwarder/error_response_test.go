package forwarder

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"linapi/internal/routing"
)

func TestCrossFormatUpstreamErrorUsesAnthropicSchemaAndSafeHeaders(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set("X-RateLimit-Remaining-Requests", "0")
		w.Header().Set("X-Request-Id", "req_openai_upstream")
		w.Header().Set("X-Unsafe-Internal", "must-not-leak")
		w.Header().Set("Set-Cookie", "upstream=secret")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad parameter","type":"invalid_request_error","param":"temperature"}}`)
	})
	ch := &routing.Channel{
		ID: "openai", Format: routing.FormatOpenAI, BaseURL: up.URL, APIKey: "sk-upstream",
		Models: map[string]string{"claude": "gpt-4o"}, Priority: 1, Weight: 1, Enabled: true,
	}
	env := newTestEnv(t, []*routing.Channel{ch}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/messages",
		`{"model":"claude","max_tokens":8,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("Anthropic 错误体不是 JSON: %v; body=%s", err, w.Body.String())
	}
	if envelope.Type != "error" || envelope.Error.Type != "invalid_request_error" || envelope.Error.Message != "bad parameter" {
		t.Fatalf("Anthropic 错误 schema 不符: %+v", envelope)
	}
	if envelope.RequestID != "req_openai_upstream" {
		t.Fatalf("上游 request id 未进入目标协议错误体: %+v", envelope)
	}
	if w.Header().Get("Retry-After") != "7" || w.Header().Get("X-RateLimit-Remaining-Requests") != "0" {
		t.Fatalf("安全速率限制头丢失: %v", w.Header())
	}
	if w.Header().Get("X-Upstream-Request-Id") != "req_openai_upstream" {
		t.Fatalf("上游 request id 响应头丢失: %v", w.Header())
	}
	if w.Header().Get("X-Unsafe-Internal") != "" || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("非允许列表头被转发: %v", w.Header())
	}
}

func TestCrossFormatAnthropicErrorUsesOpenAISchema(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Request-Id", "req_anthropic_upstream")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"denied"},"request_id":"req_body"}`)
	})
	ch := &routing.Channel{
		ID: "anthropic", Format: routing.FormatAnthropic, BaseURL: up.URL, APIKey: "sk-upstream",
		Models: map[string]string{"gpt-4o": "claude"}, Priority: 1, Weight: 1, Enabled: true,
	}
	env := newTestEnv(t, []*routing.Channel{ch}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Type != "invalid_request_error" || envelope.Error.Message != "denied" {
		t.Fatalf("OpenAI 错误 schema 不符: %+v", envelope)
	}
}

func TestNonJSONUpstreamErrorBecomesClientProtocolJSON(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "plain upstream failure")
	})
	ch := &routing.Channel{
		ID: "openai", Format: routing.FormatOpenAI, BaseURL: up.URL, APIKey: "sk-upstream",
		Models: map[string]string{"claude": "gpt-4o"}, Priority: 1, Weight: 1, Enabled: true,
	}
	env := newTestEnv(t, []*routing.Channel{ch}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/messages",
		`{"model":"claude","max_tokens":8,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	if got := w.Header().Get("Content-Type"); got != contentTypeJSON {
		t.Fatalf("Content-Type=%q", got)
	}
	var envelope struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("非 JSON 上游错误必须转成 JSON: %v; body=%s", err, w.Body.String())
	}
	if envelope.Type != "error" || envelope.Error.Type != "upstream_error" || envelope.Error.Message != "plain upstream failure" {
		t.Fatalf("fallback 错误不符: %+v", envelope)
	}
}

func TestLocalAnthropicErrorUsesAnthropicSchema(t *testing.T) {
	env := newTestEnv(t, nil, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/messages", `{"model":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["type"] != "error" {
		t.Fatalf("本地 Anthropic 错误 schema 不符: %s", w.Body.String())
	}
}

func TestSuccessCopiesOnlySafeUpstreamHeaders(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining-Tokens", "42")
		w.Header().Set("X-Request-Id", "req_success")
		w.Header().Set("X-Unsafe-Internal", "hidden")
		_, _ = io.WriteString(w, openAIChatResp)
	})
	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 1)}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-RateLimit-Remaining-Tokens") != "42" || w.Header().Get("X-Upstream-Request-Id") != "req_success" {
		t.Fatalf("安全头丢失: %v", w.Header())
	}
	if w.Header().Get("X-Unsafe-Internal") != "" {
		t.Fatalf("非允许列表头被转发: %v", w.Header())
	}
}

func TestAllRetryableErrorsKeepLastProtocolErrorAndRetryAfter(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "11")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
	})
	channel := func(id string, priority int) *routing.Channel {
		return &routing.Channel{
			ID: id, Format: routing.FormatOpenAI, BaseURL: up.URL, APIKey: "sk-upstream",
			Models: map[string]string{"claude": "gpt-4o"}, Priority: priority, Weight: 1, Enabled: true,
		}
	}
	env := newTestEnv(t, []*routing.Channel{channel("a", 2), channel("b", 1)}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/messages",
		`{"model":"claude","max_tokens":8,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("全部渠道失败仍应维持网关 502，status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Type != "error" || envelope.Error.Type != "rate_limit_error" || envelope.Error.Message != "rate limited" {
		t.Fatalf("最后一份上游错误未按客户端协议返回: %+v", envelope)
	}
	if w.Header().Get("Retry-After") != "11" {
		t.Fatalf("最终 Retry-After 丢失: %v", w.Header())
	}
}

func TestStreamingHTTPErrorUsesClientProtocolBeforeSSECommit(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad stream request","type":"invalid_request_error"}}`)
	})
	ch := &routing.Channel{
		ID: "openai", Format: routing.FormatOpenAI, BaseURL: up.URL, APIKey: "sk-upstream",
		Models: map[string]string{"claude": "gpt-4o"}, Priority: 1, Weight: 1, Enabled: true,
	}
	env := newTestEnv(t, []*routing.Channel{ch}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/messages",
		`{"model":"claude","max_tokens":8,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("SSE 提交前的 HTTP 错误必须是目标协议 JSON: %v; body=%s", err, w.Body.String())
	}
	if envelope["type"] != "error" {
		t.Fatalf("Anthropic 错误 schema 不符: %s", w.Body.String())
	}
}
