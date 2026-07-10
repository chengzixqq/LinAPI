package forwarder

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"linapi/internal/billing"
	"linapi/internal/canonical"
	"linapi/internal/routing"
)

// openAIStreamChunks 是一段 OpenAI 流式 SSE：role 首片 + 两段文本 + 结束（带 usage）+ DONE。
const openAIStreamChunks = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":8,\"total_tokens\":18}}\n\n" +
	"data: [DONE]\n\n"

// TestForwardStreamSameFormat OpenAI 客户端 → OpenAI 上游流式，直通转发。
func TestForwardStreamSameFormat(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, chunk := range strings.SplitAfter(openAIStreamChunks, "\n\n") {
			if chunk == "" {
				continue
			}
			_, _ = io.WriteString(w, chunk)
			if fl != nil {
				fl.Flush()
			}
		}
	})

	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, 1_000_000)

	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", body)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("流式响应 Content-Type 错误: %q", ct)
	}

	out := w.Body.String()
	// 应含文本增量与结束标记。
	if !strings.Contains(out, "你") || !strings.Contains(out, "好") {
		t.Errorf("流式响应缺文本增量: %s", out)
	}
	if !strings.Contains(out, "[DONE]") {
		t.Errorf("流式响应缺 [DONE] 结束标记: %s", out)
	}

	// 计费：input=10 output=8 -> cost = 10*1 + 8*2 = 26。余额 = 100万 - 26。
	waitFor(t, func() bool {
		bal, ok := env.balanceOf(t, "u-test")
		return ok && bal == 1_000_000-26
	}, time.Second)
}

// TestForwardStreamMissingFinalUsageChargesFullReservation 验证协议正常结束但上游
// 没有返回最终 usage 时，不能按 0 token 结算或退款，必须扣满预授权。
func TestForwardStreamMissingFinalUsageChargesFullReservation(t *testing.T) {
	const chunks = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chunks)
	})

	const initialBalance int64 = 1_000_000
	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, initialBalance)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("客户端应收到完整流，status=%d body=%s", w.Code, w.Body.String())
	}
	assertStreamBalance(t, env, initialBalance-streamFullReservationCost())
}

// TestForwardStreamMissingTerminalChargesFullReservation 验证即使最终 usage 完整，
// 缺少协议结束事件的提前 EOF 仍不得按该 usage 精确结算或自动退款。
func TestForwardStreamMissingTerminalChargesFullReservation(t *testing.T) {
	truncated := strings.TrimSuffix(openAIStreamChunks, "data: [DONE]\n\n")
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, truncated)
	})

	const initialBalance int64 = 1_000_000
	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, initialBalance)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if w.Code != http.StatusOK {
		t.Fatalf("流已提交后应保持 200 并截断，status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("测试上游不应产生结束标记: %s", w.Body.String())
	}
	assertStreamBalance(t, env, initialBalance-streamFullReservationCost())
}

// TestForwardStreamErrorEventChargesFullReservation 验证 HTTP 2xx 内的 Anthropic
// error 事件是已消费的异常终态：转发错误事件，但资金按预授权上限结算。
func TestForwardStreamErrorEventChargesFullReservation(t *testing.T) {
	const chunks = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude\",\"usage\":{\"input_tokens\":7}}}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"stream failed\"}}\n\n"

	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chunks)
	})
	ch := &routing.Channel{
		ID:       "ant",
		Format:   routing.FormatAnthropic,
		BaseURL:  up.URL,
		APIKey:   "sk-upstream",
		Models:   map[string]string{"claude": ""},
		Priority: 10,
		Weight:   1,
		Enabled:  true,
	}

	const initialBalance int64 = 1_000_000
	env := newTestEnv(t, []*routing.Channel{ch}, initialBalance)
	w := env.doRequest(http.MethodPost, "/v1/messages",
		`{"model":"claude","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "stream failed") {
		t.Fatalf("客户端应收到上游 error 事件，status=%d body=%s", w.Code, w.Body.String())
	}
	assertStreamBalance(t, env, initialBalance-streamFullReservationCost())
}

func TestForwardStreamFinalUsageCannotBorrowProvisionalOutput(t *testing.T) {
	const chunks = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude\",\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chunks)
	})
	ch := &routing.Channel{
		ID: "ant", Format: routing.FormatAnthropic, BaseURL: up.URL, APIKey: "sk-upstream",
		Models: map[string]string{"claude": ""}, Priority: 10, Weight: 1, Enabled: true,
	}
	env := newTestEnv(t, []*routing.Channel{ch}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/messages",
		`{"model":"claude","max_tokens":4,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if bal, _ := env.balanceOf(t, "u-test"); bal != 1_000_000-128_008 {
		t.Fatalf("残缺 final usage 必须扣满预授权，余额=%d", bal)
	}
}

func TestForwardStreamContentAfterFinalUsageChargesFullReservation(t *testing.T) {
	const chunks = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude\",\"usage\":{\"input_tokens\":7}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"late\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chunks)
	})
	ch := &routing.Channel{
		ID: "ant", Format: routing.FormatAnthropic, BaseURL: up.URL, APIKey: "sk-upstream",
		Models: map[string]string{"claude": ""}, Priority: 10, Weight: 1, Enabled: true,
	}
	const initialBalance int64 = 1_000_000
	env := newTestEnv(t, []*routing.Channel{ch}, initialBalance)
	w := env.doRequest(http.MethodPost, "/v1/messages",
		`{"model":"claude","max_tokens":4,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	assertStreamBalance(t, env, initialBalance-128_008)
}

// TestForwardStreamCrossFormat OpenAI 客户端 → Anthropic 上游流式（跨格式转换）。
// 上游发 Anthropic SSE，客户端应收到 OpenAI chunk 流。
func TestForwardStreamCrossFormat(t *testing.T) {
	// 一段最简 Anthropic 流：message_start → block_start → 文本 delta → block_stop
	// → message_delta（stop_reason + usage）→ message_stop。
	antStream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, antStream)
		if fl != nil {
			fl.Flush()
		}
	})

	ch := &routing.Channel{
		ID:       "ant",
		Format:   routing.FormatAnthropic,
		BaseURL:  up.URL,
		APIKey:   "sk-upstream",
		Models:   map[string]string{"gpt-4o": "claude-3-5-sonnet"},
		Priority: 10,
		Weight:   1,
		Enabled:  true,
	}
	env := newTestEnv(t, []*routing.Channel{ch}, 1_000_000)

	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", body)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", w.Code, w.Body.String())
	}
	out := w.Body.String()
	// 客户端应拿到 OpenAI chunk 格式（chat.completion.chunk）与 [DONE]。
	if !strings.Contains(out, "chat.completion.chunk") {
		t.Errorf("跨格式流未转为 OpenAI chunk: %s", out)
	}
	if !strings.Contains(out, "Hi") {
		t.Errorf("流式文本丢失: %s", out)
	}
	if !strings.Contains(out, "[DONE]") {
		t.Errorf("缺 [DONE]: %s", out)
	}

	// 计费：input=7 output=4 -> cost = 7*1 + 4*2 = 15。
	waitFor(t, func() bool {
		bal, ok := env.balanceOf(t, "u-test")
		return ok && bal == 1_000_000-15
	}, time.Second)
}

// TestForwardStreamCountsChunks 粗略校验客户端收到的 chunk 条数合理（含 role 首片与结束）。
func TestForwardStreamCountsChunks(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, openAIStreamChunks)
		if fl != nil {
			fl.Flush()
		}
	})
	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, 1_000_000)

	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", body)

	// 数一下 data: 行数（含 [DONE]）。
	var dataLines int
	sc := bufio.NewScanner(strings.NewReader(w.Body.String()))
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data:") {
			dataLines++
		}
	}
	if dataLines < 2 {
		t.Errorf("客户端收到的 data 行过少: %d", dataLines)
	}
}

// TestForwardStreamFailover 流式请求首选渠道 5xx（尚未提交响应），应故障转移到健康渠道。
// 这覆盖了「提交点与 committed 标志一致」的修复：首块之前的失败仍可换渠道。
func TestForwardStreamFailover(t *testing.T) {
	bad := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"unavailable"}}`)
	})
	good := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, openAIStreamChunks)
		if fl != nil {
			fl.Flush()
		}
	})

	env := newTestEnv(t, []*routing.Channel{
		openAIChannel("bad", bad.URL, 20),   // 优先，先试
		openAIChannel("good", good.URL, 10), // 故障转移目标
	}, 1_000_000)

	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", body)

	if w.Code != http.StatusOK {
		t.Fatalf("流式故障转移后应 200，得 %d；body=%s", w.Code, w.Body.String())
	}
	out := w.Body.String()
	if !strings.Contains(out, "[DONE]") || !strings.Contains(out, "你") {
		t.Errorf("故障转移后应拿到健康渠道的完整流: %s", out)
	}
}

// TestForwardStreamPassthroughVerbatim 流式同格式直通应逐字节透传上游 SSE 记录，
// 保留 canonical 事件模型未覆盖的自定义字段（跨格式重新编码会丢）。
func TestForwardStreamPassthroughVerbatim(t *testing.T) {
	// chunk 带一个自定义字段 x_stream_extra：直通透传保留，重新编码会丢。
	const chunks = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}],\"x_stream_extra\":\"keep\"}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":8,\"total_tokens\":18}}\n\n" +
		"data: [DONE]\n\n"

	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, chunks)
		if fl != nil {
			fl.Flush()
		}
	})

	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 10)}, 1_000_000)

	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", body)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", w.Code, w.Body.String())
	}
	out := w.Body.String()
	// 直通保真：自定义字段应原样出现在客户端流里。
	if !strings.Contains(out, "x_stream_extra") {
		t.Errorf("流式直通应逐字节透传自定义字段，实际输出: %s", out)
	}

	// usage 仍应被解码累计用于计费：input=10 output=8 -> cost = 10*1 + 8*2 = 26。
	waitFor(t, func() bool {
		bal, ok := env.balanceOf(t, "u-test")
		return ok && bal == 1_000_000-26
	}, time.Second)
}

func TestAccumulateUsageMergesPresenceAndCumulativeValues(t *testing.T) {
	var got canonical.Usage
	accumulateUsage(&got, canonical.Event{Usage: &canonical.Usage{
		InputTokens: 7, InputTokensKnown: true,
		OutputTokens: 1, OutputTokensKnown: true,
		CacheCreationInputTokens: 2,
		CacheReadInputTokens:     3,
	}})
	accumulateUsage(&got, canonical.Event{Usage: &canonical.Usage{
		OutputTokens: 4, OutputTokensKnown: true,
		ReportedTotalTokens: 11, TotalTokensKnown: true,
		CacheCreationInputTokens: 5,
		CacheReadInputTokens:     1,
	}})

	if got.InputTokens != 7 || !got.InputTokensKnown ||
		got.OutputTokens != 4 || !got.OutputTokensKnown ||
		got.ReportedTotalTokens != 11 || !got.TotalTokensKnown ||
		got.CacheCreationInputTokens != 5 || got.CacheReadInputTokens != 3 {
		t.Fatalf("usage 累计错误: %+v", got)
	}
}

func TestAccumulateUsagePreservesInvalidCacheValues(t *testing.T) {
	got := canonical.Usage{InputTokensKnown: true, OutputTokensKnown: true}
	accumulateUsage(&got, canonical.Event{Usage: &canonical.Usage{CacheReadInputTokens: -1}})
	accumulateUsage(&got, canonical.Event{Usage: &canonical.Usage{CacheReadInputTokens: 20}})
	if got.CacheReadInputTokens != -1 || usageReadyForExactSettlement(got) {
		t.Fatalf("非法缓存 usage 必须保持失效，不能被后续值覆盖: %+v", got)
	}
}

func TestUsageWithFinalAuthorityDoesNotUseProvisionalOutput(t *testing.T) {
	aggregate := canonical.Usage{
		InputTokens: 7, InputTokensKnown: true,
		OutputTokens: 1, OutputTokensKnown: true,
	}
	got := usageWithFinalAuthority(aggregate, canonical.Usage{})
	if got.OutputTokensKnown || usageReadyForExactSettlement(got) {
		t.Fatalf("残缺最终 usage 不得被早期 output 补齐: %+v", got)
	}

	final := canonical.Usage{OutputTokens: 4, OutputTokensKnown: true}
	got = usageWithFinalAuthority(aggregate, final)
	if !usageReadyForExactSettlement(got) || got.OutputTokens != 4 {
		t.Fatalf("最终 output 应覆盖临时值并可精确结算: %+v", got)
	}
}

func TestUsageReadyForExactSettlement(t *testing.T) {
	tests := []struct {
		name  string
		usage canonical.Usage
		want  bool
	}{
		{
			name: "双边完整",
			usage: canonical.Usage{
				InputTokens: 10, InputTokensKnown: true,
				OutputTokens: 5, OutputTokensKnown: true,
			},
			want: true,
		},
		{
			name: "total 加单边可推导",
			usage: canonical.Usage{
				InputTokens: 10, InputTokensKnown: true,
				ReportedTotalTokens: 15, TotalTokensKnown: true,
			},
			want: true,
		},
		{
			name:  "完全缺失",
			usage: canonical.Usage{},
		},
		{
			name: "只有 total",
			usage: canonical.Usage{
				ReportedTotalTokens: 15, TotalTokensKnown: true,
			},
		},
		{
			name: "total 冲突",
			usage: canonical.Usage{
				InputTokens: 10, InputTokensKnown: true,
				OutputTokens: 5, OutputTokensKnown: true,
				ReportedTotalTokens: 99, TotalTokensKnown: true,
			},
		},
		{
			name: "负缓存 token",
			usage: canonical.Usage{
				InputTokens: 10, InputTokensKnown: true,
				OutputTokens: 5, OutputTokensKnown: true,
				CacheReadInputTokens: -1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usageReadyForExactSettlement(tt.usage); got != tt.want {
				t.Fatalf("usageReadyForExactSettlement() = %v，期望 %v；usage=%+v", got, tt.want, tt.usage)
			}
		})
	}
}

func streamFullReservationCost() int64 {
	// 测试 Pricing 的兜底单价为 input=1/token、output=2/token。
	return int64(billing.DefaultMaxBillableInputTokens) +
		2*int64(billing.DefaultMaxOutputTokens)
}

func assertStreamBalance(t *testing.T, env *testEnv, want int64) {
	t.Helper()
	waitFor(t, func() bool {
		balance, ok := env.balanceOf(t, "u-test")
		return ok && balance == want
	}, time.Second)
}
