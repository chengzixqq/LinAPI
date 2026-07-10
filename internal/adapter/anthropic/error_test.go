package anthropic

import (
	"encoding/json"
	"testing"

	"linapi/internal/canonical"
)

func TestErrorCodecRoundTrip(t *testing.T) {
	a := &Adapter{}
	parsed, err := a.ParseError([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"},"request_id":"req_upstream"}`))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Type != "overloaded_error" || parsed.Message != "busy" || parsed.RequestID != "req_upstream" {
		t.Fatalf("解析结果不符: %+v", parsed)
	}

	built, err := a.BuildError(&canonical.ErrorResponse{
		Type: "invalid_request_error", Message: "bad", RequestID: "req_123",
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Error     struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(built, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Type != "error" || envelope.RequestID != "req_123" ||
		envelope.Error.Type != "invalid_request_error" || envelope.Error.Message != "bad" {
		t.Fatalf("构造结果不符: %s", built)
	}
}

func TestParseErrorRejectsWrongEnvelope(t *testing.T) {
	if _, err := (&Adapter{}).ParseError([]byte(`{"type":"message","error":{"type":"api_error","message":"bad"}}`)); err == nil {
		t.Fatal("非 error envelope 必须拒绝")
	}
}
