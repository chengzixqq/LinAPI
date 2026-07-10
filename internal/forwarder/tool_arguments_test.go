package forwarder

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"linapi/internal/routing"
)

func TestOpenAIModelAliasPreservesRawToolArguments(t *testing.T) {
	const arguments = `{"order_id":9007199254740993,"partial":`
	upstream := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		quoted, _ := json.Marshal(arguments)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{
				"id":"call_1","type":"function","function":{"name":"submit","arguments":` + string(quoted) + `}
			}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`))
	})

	channel := openAIChannel("aliased", upstream.URL, 10)
	channel.Models = map[string]string{"public-model": "gpt-4o"}
	env := newTestEnv(t, []*routing.Channel{channel}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", `{
		"model":"public-model","messages":[{"role":"user","content":"hello"}]
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("别名转换请求失败: status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if response.Model != "public-model" {
		t.Fatalf("模型别名未恢复: %q", response.Model)
	}
	if got := response.Choices[0].Message.ToolCalls[0].Function.Arguments; got != arguments {
		t.Fatalf("别名往返改写了 arguments: got=%q want=%q", got, arguments)
	}
}

func TestConsumedToolArgumentConversionErrorDoesNotRetry(t *testing.T) {
	var firstCalls atomic.Int32
	first := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{
				"id":"call_1","type":"function","function":{"name":"submit","arguments":"{\"order_id\":"}
			}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`))
	})

	var secondCalls atomic.Int32
	second := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(openAIChatResp))
	})

	firstChannel := openAIChannel("first", first.URL, 20)
	firstChannel.Models = map[string]string{"claude-test": "gpt-4o"}
	secondChannel := openAIChannel("second", second.URL, 10)
	secondChannel.Models = map[string]string{"claude-test": "gpt-4o"}
	env := newTestEnv(t, []*routing.Channel{firstChannel, secondChannel}, 1_000_000)

	w := env.doRequest(http.MethodPost, "/v1/messages", `{
		"model":"claude-test","max_tokens":64,
		"messages":[{"role":"user","content":"hello"}]
	}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("跨格式无法表示截断 arguments 时应返回本地转换错误, status=%d body=%s", w.Code, w.Body.String())
	}
	if got := firstCalls.Load(); got != 1 {
		t.Fatalf("首选渠道调用次数 = %d, want 1", got)
	}
	if got := secondCalls.Load(); got != 0 {
		t.Fatalf("已消费的 2xx 转换错误不得重试第二渠道, 实际调用 %d 次", got)
	}
}

func TestConsumedMultipleChoicesDoesNotRetryOrSilentlyDrop(t *testing.T) {
	var firstCalls atomic.Int32
	first := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1","model":"gpt-4o",
			"choices":[
				{"index":0,"message":{"role":"assistant","content":"a"},"finish_reason":"stop"},
				{"index":1,"message":{"role":"assistant","content":"b"},"finish_reason":"stop"}
			],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`))
	})

	var secondCalls atomic.Int32
	second := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		_, _ = w.Write([]byte(openAIChatResp))
	})
	firstChannel := openAIChannel("first", first.URL, 20)
	firstChannel.Models = map[string]string{"claude-test": "gpt-4o"}
	secondChannel := openAIChannel("second", second.URL, 10)
	secondChannel.Models = map[string]string{"claude-test": "gpt-4o"}
	env := newTestEnv(t, []*routing.Channel{firstChannel, secondChannel}, 1_000_000)

	w := env.doRequest(http.MethodPost, "/v1/messages", `{
		"model":"claude-test","max_tokens":64,
		"messages":[{"role":"user","content":"hello"}]
	}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("异常多 choice 不得静默截成一个, status=%d body=%s", w.Code, w.Body.String())
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("已消费响应不得故障转移: first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
}
