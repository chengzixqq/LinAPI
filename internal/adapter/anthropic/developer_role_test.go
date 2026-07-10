package anthropic

import (
	"encoding/json"
	"testing"

	"linapi/internal/canonical"
)

func TestBuildRequestMergesLeadingOrderedInstructions(t *testing.T) {
	built, err := (&Adapter{}).BuildRequest(&canonical.Request{
		Model:  "claude",
		System: []canonical.ContentBlock{{Type: canonical.BlockText, Text: "native-system"}},
		Messages: []canonical.Message{
			{Role: canonical.RoleDeveloper, Content: []canonical.ContentBlock{{Type: canonical.BlockText, Text: "developer"}}},
			{Role: canonical.RoleSystem, Content: []canonical.ContentBlock{{Type: canonical.BlockText, Text: "ordered-system"}}},
			{Role: canonical.RoleUser, Content: []canonical.ContentBlock{{Type: canonical.BlockText, Text: "question"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		System []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(built, &wire); err != nil {
		t.Fatal(err)
	}
	want := []string{"native-system", "developer", "ordered-system"}
	if len(wire.System) != len(want) {
		t.Fatalf("system=%+v body=%s", wire.System, built)
	}
	for i := range want {
		if wire.System[i].Text != want[i] {
			t.Fatalf("system[%d]=%q want=%q; body=%s", i, wire.System[i].Text, want[i], built)
		}
	}
	if len(wire.Messages) != 1 || wire.Messages[0].Role != "user" {
		t.Fatalf("messages=%+v body=%s", wire.Messages, built)
	}
}

func TestBuildRequestRejectsInstructionAfterConversationStart(t *testing.T) {
	_, err := (&Adapter{}).BuildRequest(&canonical.Request{
		Model: "claude",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentBlock{{Type: canonical.BlockText, Text: "question"}}},
			{Role: canonical.RoleDeveloper, Content: []canonical.ContentBlock{{Type: canonical.BlockText, Text: "late"}}},
		},
	})
	if err == nil {
		t.Fatal("Anthropic 无法表达正文后的 developer 指令，必须显式拒绝")
	}
}
