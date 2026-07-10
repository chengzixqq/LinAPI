package openai

import (
	"encoding/json"
	"testing"

	"linapi/internal/canonical"
)

func TestDeveloperAndSystemRolesPreserveMessageOrder(t *testing.T) {
	const raw = `{
		"model":"gpt-4o",
		"messages":[
			{"role":"developer","content":"developer-first"},
			{"role":"system","content":"system-second"},
			{"role":"user","content":"question"},
			{"role":"developer","content":"developer-after-user"}
		]
	}`
	a := &Adapter{}
	req, err := a.ParseRequest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.System) != 0 {
		t.Fatalf("OpenAI 有序指令不得提升到 Request.System: %+v", req.System)
	}
	wantRoles := []canonical.Role{
		canonical.RoleDeveloper, canonical.RoleSystem, canonical.RoleUser, canonical.RoleDeveloper,
	}
	if len(req.Messages) != len(wantRoles) {
		t.Fatalf("messages=%+v", req.Messages)
	}
	for i, role := range wantRoles {
		if req.Messages[i].Role != role {
			t.Fatalf("message[%d].role=%q want=%q", i, req.Messages[i].Role, role)
		}
	}

	built, err := a.BuildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(built, &wire); err != nil {
		t.Fatal(err)
	}
	for i, role := range wantRoles {
		if wire.Messages[i].Role != string(role) {
			t.Fatalf("wire message[%d].role=%q want=%q; body=%s", i, wire.Messages[i].Role, role, built)
		}
	}
}

func TestBuildRequestPrependsLegacyTopLevelSystemBeforeOrderedMessages(t *testing.T) {
	a := &Adapter{}
	built, err := a.BuildRequest(&canonical.Request{
		Model:  "gpt-4o",
		System: []canonical.ContentBlock{{Type: canonical.BlockText, Text: "top-level"}},
		Messages: []canonical.Message{
			{Role: canonical.RoleDeveloper, Content: []canonical.ContentBlock{{Type: canonical.BlockText, Text: "developer"}}},
			{Role: canonical.RoleUser, Content: []canonical.ContentBlock{{Type: canonical.BlockText, Text: "question"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(built, &wire); err != nil {
		t.Fatal(err)
	}
	want := []string{"system", "developer", "user"}
	for i := range want {
		if wire.Messages[i].Role != want[i] {
			t.Fatalf("roles=%+v body=%s", wire.Messages, built)
		}
	}
}
