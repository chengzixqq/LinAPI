package forwarder

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"linapi/internal/routing"
)

// waitFor 轮询 cond 直到为真或超时，供异步计费结算的断言使用。
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// TestForwardNonStreamSuccess 端到端：OpenAI 客户端 → OpenAI 上游，非流式成功，
// 校验响应透传与计费结算（押金退差后余额正确）。
func TestForwardNonStreamSuccess(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-upstream" {
			t.Errorf("上游未收到正确鉴权头: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, openAIChatResp)
	})

	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, 1_000_000)

	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	if resp["object"] != "chat.completion" {
		t.Errorf("响应 object 字段错误: %v", resp["object"])
	}

	// 计费：押金 5000 预扣，实际 cost = 10*1 + 5*2 = 20（兜底价 input=1e6/1M, output=2e6/1M）。
	// 结算后余额 = 100 万 - 20。等待异步/同步结算完成。
	waitFor(t, func() bool {
		bal, ok := env.balanceOf(t, "u-test")
		return ok && bal == 1_000_000-20
	}, time.Second)
}

// TestForwardCrossFormat 端到端：OpenAI 客户端 → Anthropic 上游（跨格式转换）。
// 上游收到的应是 Anthropic Messages 格式，客户端拿回的应是 OpenAI 格式。
func TestForwardCrossFormat(t *testing.T) {
	var gotUpstreamBody atomic.Value
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotUpstreamBody.Store(string(body))
		if got := r.Header.Get("x-api-key"); got != "sk-upstream" {
			t.Errorf("上游未收到 x-api-key: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
		  "id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022",
		  "content":[{"type":"text","text":"你好"}],
		  "stop_reason":"end_turn",
		  "usage":{"input_tokens":8,"output_tokens":3}
		}`)
	})

	ch := &routing.Channel{
		ID:       "ant",
		Name:     "ant",
		Format:   routing.FormatAnthropic,
		BaseURL:  up.URL,
		APIKey:   "sk-upstream",
		Models:   map[string]string{"gpt-4o": "claude-3-5-sonnet-20241022"},
		Priority: 10,
		Weight:   1,
		Enabled:  true,
	}
	env := newTestEnv(t, []*routing.Channel{ch}, 1_000_000)

	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", w.Code, w.Body.String())
	}

	// 上游应收到 Anthropic 格式（含 max_tokens、model 已改写为上游名）。
	upBody, _ := gotUpstreamBody.Load().(string)
	if !strings.Contains(upBody, "claude-3-5-sonnet-20241022") {
		t.Errorf("上游请求未改写模型名: %s", upBody)
	}
	if !strings.Contains(upBody, "max_tokens") {
		t.Errorf("上游请求应含 Anthropic 必填的 max_tokens: %s", upBody)
	}

	// 客户端应拿回 OpenAI 格式。
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	if resp["object"] != "chat.completion" {
		t.Errorf("客户端应拿到 OpenAI 格式响应: %v", resp["object"])
	}
}

// TestForwardFailover 首选渠道 5xx，应故障转移到次选渠道成功。
func TestForwardFailover(t *testing.T) {
	var badHits int32
	bad := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&badHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	})
	good := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, openAIChatResp)
	})

	// bad 优先级更高，先被尝试；失败后转 good。
	env := newTestEnv(t, []*routing.Channel{
		openAIChannel("bad", bad.URL, 20),
		openAIChannel("good", good.URL, 10),
	}, 1_000_000)

	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != http.StatusOK {
		t.Fatalf("故障转移后应成功，状态码 = %d；body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&badHits) == 0 {
		t.Error("首选渠道应被尝试过")
	}
}

// TestForwardAllChannelsFail 全部渠道 5xx，应返回 502 且全额退回押金。
func TestForwardAllChannelsFail(t *testing.T) {
	bad := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"down"}}`)
	})
	env := newTestEnv(t, []*routing.Channel{openAIChannel("bad", bad.URL, 10)}, 1_000_000)

	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("全败应返回 502，得 %d；body=%s", w.Code, w.Body.String())
	}

	// 押金应全额退回：余额回到 100 万。
	waitFor(t, func() bool {
		bal, ok := env.balanceOf(t, "u-test")
		return ok && bal == 1_000_000
	}, time.Second)
}

// TestForwardUpstreamClientError 上游返回 4xx（非渠道故障），应透传且不故障转移、退回押金。
func TestForwardUpstreamClientError(t *testing.T) {
	var hits int32
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"message":"bad param","type":"invalid_request_error"}}`)
	})
	// 两个渠道，但 4xx 不应触发故障转移，只打首选一次。
	env := newTestEnv(t, []*routing.Channel{
		openAIChannel("c1", up.URL, 20),
		openAIChannel("c2", up.URL, 10),
	}, 1_000_000)

	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("应透传上游 400，得 %d", w.Code)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("4xx 不应故障转移，上游应只被打 1 次，实际 %d", got)
	}

	// 未产生用量，押金全额退回。
	waitFor(t, func() bool {
		bal, ok := env.balanceOf(t, "u-test")
		return ok && bal == 1_000_000
	}, time.Second)
}

// TestForwardInsufficientBalance 余额低于预扣额，Quota 中间件应 402 拦截，不打上游。
func TestForwardInsufficientBalance(t *testing.T) {
	var hits int32
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, openAIChatResp)
	})
	// 余额 100 < 预扣 5000。
	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, 100)

	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("余额不足应 402，得 %d", w.Code)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Error("余额不足不应打上游")
	}
}

// TestForwardNoChannel 请求的模型无渠道支持，应返回 503。
func TestForwardNoChannel(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, openAIChatResp)
	})
	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, 1_000_000)

	// 请求一个未配置的模型。
	body := `{"model":"unknown-model","messages":[{"role":"user","content":"hi"}]}`
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", body)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("无渠道应 503，得 %d；body=%s", w.Code, w.Body.String())
	}

	// 押金退回。
	waitFor(t, func() bool {
		bal, ok := env.balanceOf(t, "u-test")
		return ok && bal == 1_000_000
	}, time.Second)
}

// TestForwardPassthroughVerbatim 同格式（openai→openai）且无模型重命名时走直通：
// 上游应收到与客户端逐字节相同的请求体（含 canonical 超集未覆盖的自定义字段），
// 客户端也应拿回上游逐字节透传的响应（不经 canonical 往返丢字段）。
func TestForwardPassthroughVerbatim(t *testing.T) {
	// 带一个 canonical 模型不认识的自定义字段：直通保留，往返会丢。
	const reqWithExtra = `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"x_custom":"keep-me"}`
	// 响应也带自定义字段，验证响应透传保真。
	const respWithExtra = `{"id":"chatcmpl-9","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},"x_vendor":"passthru"}`

	var gotUpstreamBody atomic.Value
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotUpstreamBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respWithExtra)
	})

	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, 1_000_000)

	w := env.doRequest(http.MethodPost, "/v1/chat/completions", reqWithExtra)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", w.Code, w.Body.String())
	}

	// 请求体逐字节透传：上游收到的应与客户端原文完全一致。
	upBody, _ := gotUpstreamBody.Load().(string)
	if upBody != reqWithExtra {
		t.Errorf("直通应逐字节透传请求体\n期望: %s\n实际: %s", reqWithExtra, upBody)
	}

	// 响应逐字节透传：自定义字段应保留。
	if w.Body.String() != respWithExtra {
		t.Errorf("直通应逐字节透传响应体\n期望: %s\n实际: %s", respWithExtra, w.Body.String())
	}
}

// TestForwardRenameNoPassthrough 同格式但有模型重命名时不走直通：
// 上游应收到改写后的模型名（证明经过 BuildRequest，未原样透传客户端 body）。
func TestForwardRenameNoPassthrough(t *testing.T) {
	var gotUpstreamBody atomic.Value
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotUpstreamBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, openAIChatResp)
	})

	// 同为 openai 格式，但对外 gpt-4o 映射到上游 gpt-4o-internal。
	ch := &routing.Channel{
		ID:       "c1",
		Name:     "c1",
		Format:   routing.FormatOpenAI,
		BaseURL:  up.URL,
		APIKey:   "sk-upstream",
		Models:   map[string]string{"gpt-4o": "gpt-4o-internal"},
		Priority: 10,
		Weight:   1,
		Enabled:  true,
	}
	env := newTestEnv(t, []*routing.Channel{ch}, 1_000_000)

	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", w.Code, w.Body.String())
	}

	upBody, _ := gotUpstreamBody.Load().(string)
	if !strings.Contains(upBody, "gpt-4o-internal") {
		t.Errorf("重命名渠道应改写上游模型名，未走直通；上游 body: %s", upBody)
	}
}
