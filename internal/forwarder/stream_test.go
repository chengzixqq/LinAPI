package forwarder

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

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
		w.WriteHeader(http.StatusServiceUnavailable)
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
