package openai

import (
	"encoding/json"
	"testing"

	"linapi/internal/canonical"
)

func TestErrorCodecRoundTrip(t *testing.T) {
	a := &Adapter{}
	parsed, err := a.ParseError([]byte(`{"error":{"message":"bad parameter","type":"invalid_request_error","param":"temperature","code":4001}}`))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Type != "invalid_request_error" || parsed.Message != "bad parameter" || parsed.Param != "temperature" {
		t.Fatalf("解析结果不符: %+v", parsed)
	}
	if number, ok := parsed.Code.(json.Number); !ok || number.String() != "4001" {
		t.Fatalf("code 应保留为 json.Number: %#v", parsed.Code)
	}

	built, err := a.BuildError(&canonical.ErrorResponse{
		Type: "rate_limit_error", Message: "slow down", Code: "rate_limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]map[string]any
	if err := json.Unmarshal(built, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["error"]["type"] != "rate_limit_error" || envelope["error"]["message"] != "slow down" {
		t.Fatalf("构造结果不符: %s", built)
	}
}

func TestParseErrorRejectsNonJSON(t *testing.T) {
	if _, err := (&Adapter{}).ParseError([]byte("upstream exploded")); err == nil {
		t.Fatal("非 JSON 错误体必须报告解析失败")
	}
}
