package forwarder

import (
	"encoding/json"
	"net/http"
	"testing"

	"linapi/internal/canonical"
	"linapi/internal/routing"
)

func TestOpenAIOutputLimitResolverRequiresReleaseMapping(t *testing.T) {
	resolver, err := NewOpenAIOutputLimitResolver(map[string]string{
		"c1/o1-upstream": openAIMaxCompletionTokensField,
	}, true)
	if err != nil {
		t.Fatal(err)
	}

	configured := openAIChannel("c1", "http://example.invalid", 1)
	configured.Models = map[string]string{"public-model": "o1-upstream"}
	if err := resolver.ValidateChannels([]*routing.Channel{configured}); err != nil {
		t.Fatalf("显式映射应通过启动校验: %v", err)
	}

	missing := openAIChannel("c2", "http://example.invalid", 1)
	missing.Models = map[string]string{"public-model": "unknown-upstream"}
	if err := resolver.ValidateChannels([]*routing.Channel{missing}); err == nil {
		t.Fatal("release 模式缺少上游模型字段策略时应失败")
	}
}

func TestPatchOpenAIOutputLimitWritesOnlyConfiguredField(t *testing.T) {
	out, err := patchOpenAIOutputLimit(
		[]byte(`{"model":"o1","max_tokens":10,"max_completion_tokens":20,"custom":true}`),
		openAIMaxCompletionTokensField,
		64,
	)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body[openAIMaxTokensField]; ok {
		t.Fatal("不得同时向上游发送 max_tokens")
	}
	if got := body[openAIMaxCompletionTokensField]; got != float64(64) {
		t.Fatalf("max_completion_tokens = %v, want 64", got)
	}
	if body["custom"] != true {
		t.Fatal("补丁不应丢失未建模字段")
	}
}

func TestForwardUsesActualUpstreamModelOutputLimitField(t *testing.T) {
	received := make(chan map[string]any, 1)
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解析上游请求失败: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(openAIChatResp))
	})

	ch := openAIChannel("c1", up.URL, 1)
	ch.Models = map[string]string{"gpt-4o": "o1-upstream"}
	resolver, err := NewOpenAIOutputLimitResolver(map[string]string{
		"c1/o1-upstream": openAIMaxCompletionTokensField,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	env := newTestEnvWithResolver(t, []*routing.Channel{ch}, 1_000_000, resolver)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":123}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	body := <-received
	if body["model"] != "o1-upstream" {
		t.Fatalf("上游模型 = %v", body["model"])
	}
	if _, ok := body[openAIMaxTokensField]; ok {
		t.Fatal("客户端字段不得覆盖上游模型策略")
	}
	if got := body[openAIMaxCompletionTokensField]; got != float64(123) {
		t.Fatalf("max_completion_tokens = %v, want 123", got)
	}
}

func TestEnforceCandidateOutputLimitLeavesAnthropicUntouched(t *testing.T) {
	maxOutput := 64
	f := &Forwarder{}
	fc := &forwardCtx{clientModel: "claude", req: &canonical.Request{MaxTokens: &maxOutput}}
	ch := &routing.Channel{Format: routing.FormatAnthropic}
	raw := []byte(`{"max_tokens":64}`)
	out, err := f.enforceCandidateOutputLimit(ch, fc, raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(raw) {
		t.Fatalf("Anthropic 请求不应被 OpenAI 策略改写: %s", out)
	}
}
