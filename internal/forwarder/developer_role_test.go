package forwarder

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"linapi/internal/routing"
)

func TestOpenAIDeveloperRoleReachesSameFormatUpstreamInOrder(t *testing.T) {
	var upstreamRoles []string
	up := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("上游请求不是 JSON: %v; body=%s", err, body)
		}
		for _, message := range payload.Messages {
			upstreamRoles = append(upstreamRoles, message.Role)
		}
		_, _ = io.WriteString(w, openAIChatResp)
	})
	env := newTestEnv(t, []*routing.Channel{openAIChannel("c1", up.URL, 1)}, 1_000_000)
	w := env.doRequest(http.MethodPost, "/v1/chat/completions", `{
		"model":"gpt-4o",
		"messages":[
			{"role":"developer","content":"follow policy"},
			{"role":"system","content":"be concise"},
			{"role":"user","content":"hello"}
		]
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("developer 请求不应在直通前被拒绝: status=%d body=%s", w.Code, w.Body.String())
	}
	want := []string{"developer", "system", "user"}
	if len(upstreamRoles) != len(want) {
		t.Fatalf("上游 roles=%v", upstreamRoles)
	}
	for i := range want {
		if upstreamRoles[i] != want[i] {
			t.Fatalf("上游 roles=%v want=%v", upstreamRoles, want)
		}
	}
}
