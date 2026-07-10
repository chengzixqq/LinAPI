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

	const renameReq = `{"model":"gpt-4o","max_completion_tokens":512,"messages":[{"role":"user","content":"hi"}],"x_keep":true}`
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", renameReq)
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
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"busy"}}`)
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

// TestForwardAllChannelsFail 全部渠道明确 429 拒绝（未消费），应返回 502 且退款。
func TestForwardAllChannelsFail(t *testing.T) {
	bad := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
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

// TestForwardAmbiguousServerErrorDoesNotReplayOrRefund 覆盖 AUD-P1-15：请求已送达后
// 返回 5xx 不能证明未消费，因此不得跨渠道重放，也不得自动退款。
func TestForwardAmbiguousServerErrorDoesNotReplayOrRefund(t *testing.T) {
	var secondHits int32
	bad := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"ambiguous"}}`)
	})
	second := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&secondHits, 1)
		_, _ = io.WriteString(w, openAIChatResp)
	})
	env := newTestEnv(t, []*routing.Channel{
		openAIChannel("bad", bad.URL, 20), openAIChannel("second", second.URL, 10),
	}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("应保留上游 500，得 %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&secondHits) != 0 {
		t.Fatal("结果不确定的请求不得跨渠道重放")
	}
	if bal, _ := env.balanceOf(t, "u-test"); bal != 1_000_000-136_192 {
		t.Fatalf("结果不确定时应保留预授权，余额=%d", bal)
	}
}

func TestForwardUnknown4xxDoesNotReplayOrRefund(t *testing.T) {
	var secondHits int32
	unknown := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(499)
		_, _ = io.WriteString(w, `{"error":{"message":"client closed after send"}}`)
	})
	second := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&secondHits, 1)
		_, _ = io.WriteString(w, openAIChatResp)
	})
	env := newTestEnv(t, []*routing.Channel{
		openAIChannel("unknown", unknown.URL, 20), openAIChannel("second", second.URL, 10),
	}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != 499 {
		t.Fatalf("未知 4xx 应原样返回，得 %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&secondHits) != 0 {
		t.Fatal("未知 4xx 不得跨渠道重放")
	}
	if bal, _ := env.balanceOf(t, "u-test"); bal != 1_000_000-136_192 {
		t.Fatalf("未知 4xx 应保留预授权待对账，余额=%d", bal)
	}
}

func TestForwardDoesNotFollowOrRefundCrossHostRedirect(t *testing.T) {
	var targetCalls int
	target := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
		if r.Header.Get("x-api-key") != "" || r.Header.Get("Authorization") != "" {
			t.Fatalf("重定向目标收到上游凭证")
		}
		w.WriteHeader(http.StatusOK)
	})
	source := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/steal")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	ch := &routing.Channel{
		ID: "ant", Format: routing.FormatAnthropic, BaseURL: source.URL, APIKey: "sk-secret",
		Models: map[string]string{"claude": ""}, Priority: 1, Weight: 1, Enabled: true,
	}
	const initialBalance int64 = 1_000_000
	env := newTestEnv(t, []*routing.Channel{ch}, initialBalance)
	w := env.doRequest(http.MethodPost, "/v1/messages",
		`{"model":"claude","max_tokens":4,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("redirect 应原样返回且停止重放，status=%d body=%s", w.Code, w.Body.String())
	}
	if targetCalls != 0 {
		t.Fatalf("不得自动跟随跨主机重定向，target calls=%d", targetCalls)
	}
	if balance, _ := env.balanceOf(t, "u-test"); balance != initialBalance-128_008 {
		t.Fatalf("3xx 可能是已处理 POST，必须保留预授权待对账，余额=%d", balance)
	}
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

// TestForwardInsufficientBalance 余额低于最大成本预授权额，Forwarder 应 402 拦截且不打上游。
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
// 请求仅最小合并服务端输出上限并保留未知字段；响应仍逐字节透传。
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

	// 请求必须保留未知字段，同时真实写入服务端强制的 max_tokens，防止直通绕过
	// 最大成本预授权。JSON 字段顺序/空白不属于保真契约。
	upBody, _ := gotUpstreamBody.Load().(string)
	var upstream map[string]json.RawMessage
	if err := json.Unmarshal([]byte(upBody), &upstream); err != nil {
		t.Fatalf("上游请求不是合法 JSON: %v; body=%s", err, upBody)
	}
	var custom string
	if err := json.Unmarshal(upstream["x_custom"], &custom); err != nil || custom != "keep-me" {
		t.Fatalf("直通最小合并丢失未知字段: %s", upBody)
	}
	var maxTokens int
	if err := json.Unmarshal(upstream["max_tokens"], &maxTokens); err != nil || maxTokens != 4096 {
		t.Fatalf("上游请求未注入服务端输出上限: %s", upBody)
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

	const renameReq = `{"model":"gpt-4o","max_completion_tokens":512,"messages":[{"role":"user","content":"hi"}],"x_keep":true}`
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", renameReq)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", w.Code, w.Body.String())
	}

	upBody, _ := gotUpstreamBody.Load().(string)
	if !strings.Contains(upBody, "gpt-4o-internal") {
		t.Errorf("重命名渠道应改写上游模型名，未走直通；上游 body: %s", upBody)
	}
	var rewritten map[string]json.RawMessage
	if err := json.Unmarshal([]byte(upBody), &rewritten); err != nil {
		t.Fatal(err)
	}
	if _, ok := rewritten["max_completion_tokens"]; !ok {
		t.Fatalf("同协议模型别名不得丢 max_completion_tokens: %s", upBody)
	}
	if _, ok := rewritten["max_tokens"]; ok {
		t.Fatalf("现代输出上限不得退化为 max_tokens: %s", upBody)
	}
	if _, ok := rewritten["x_keep"]; !ok {
		t.Fatalf("模型别名最小改写应保留未知字段: %s", upBody)
	}
}

// TestForwardNonStreamMissingUsageChargesMaximum 覆盖 AUD-P0-06：成功响应完全缺失
// usage 时不能零成本或退款，必须按本次预授权上限保守结算。
func TestForwardNonStreamMissingUsageChargesMaximum(t *testing.T) {
	const response = `{"id":"c1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	})
	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// 测试价格为 input=1/token、output=2/token；默认边界 128000 + 4096*2。
	want := int64(1_000_000 - 136_192)
	if bal, _ := env.balanceOf(t, "u-test"); bal != want {
		t.Fatalf("缺 usage 应扣满预授权，余额=%d want=%d", bal, want)
	}
}

// TestForwardNonStreamTotalOnlyUsesConservativePrice 验证只有 total_tokens 时按
// 输入/输出较高单价结算，而不是把两边都当成零。
func TestForwardNonStreamTotalOnlyUsesConservativePrice(t *testing.T) {
	const response = `{"id":"c1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":15}}`
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	})
	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", openAIChatReq)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if bal, _ := env.balanceOf(t, "u-test"); bal != 1_000_000-30 {
		t.Fatalf("total-only 应按较高输出价结算 30，余额=%d", bal)
	}
}
