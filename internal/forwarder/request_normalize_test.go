package forwarder

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizePassthroughRequestOpenAIPreservesUnknownFields(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-4o",
		"messages":[{"role":"user","content":"hi"}],
		"stream":true,
		"stream_options":{"include_usage":false,"x_vendor":{"mode":"precise"}},
		"x_top_level":{"request_tag":123}
	}`)

	got, err := normalizePassthroughRequest(raw, "openai", 4096, true)
	if err != nil {
		t.Fatalf("normalizePassthroughRequest 失败: %v", err)
	}
	body := decodeNormalizedBody(t, got)

	assertRawJSONEqual(t, body["max_tokens"], json.RawMessage(`4096`), "max_tokens")
	assertRawJSONEqual(t, body["x_top_level"], json.RawMessage(`{"request_tag":123}`), "x_top_level")

	var options map[string]json.RawMessage
	if err := json.Unmarshal(body["stream_options"], &options); err != nil {
		t.Fatalf("stream_options 不是对象: %v", err)
	}
	assertRawJSONEqual(t, options["include_usage"], json.RawMessage(`true`), "include_usage")
	assertRawJSONEqual(t, options["x_vendor"], json.RawMessage(`{"mode":"precise"}`), "x_vendor")
}

func TestNormalizePassthroughRequestOpenAIStreamOptions(t *testing.T) {
	tests := []struct {
		name          string
		streamOptions string
		wantVendor    bool
	}{
		{name: "缺失"},
		{name: "显式 false", streamOptions: `{"include_usage":false,"x_vendor":"keep"}`, wantVendor: true},
		{name: "null", streamOptions: `null`},
		{name: "字符串", streamOptions: `"invalid"`},
		{name: "数组", streamOptions: `[]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			optionsField := ""
			if tt.streamOptions != "" {
				optionsField = `,"stream_options":` + tt.streamOptions
			}
			raw := []byte(fmt.Sprintf(
				`{"model":"gpt-4o","max_completion_tokens":2048,"stream":true%s}`,
				optionsField,
			))
			got, err := normalizePassthroughRequest(raw, "openai", 4096, true)
			if err != nil {
				t.Fatalf("normalizePassthroughRequest 失败: %v", err)
			}

			body := decodeNormalizedBody(t, got)
			var options map[string]json.RawMessage
			if err := json.Unmarshal(body["stream_options"], &options); err != nil {
				t.Fatalf("stream_options 应被规范为对象: %v", err)
			}
			assertRawJSONEqual(t, options["include_usage"], json.RawMessage(`true`), "include_usage")
			if tt.wantVendor {
				assertRawJSONEqual(t, options["x_vendor"], json.RawMessage(`"keep"`), "x_vendor")
			}
		})
	}
}

func TestNormalizePassthroughRequestInjectsMissingOutputLimit(t *testing.T) {
	tests := []struct {
		name   string
		format string
		raw    string
	}{
		{
			name:   "OpenAI",
			format: "openai",
			raw:    `{"model":"gpt-4o","messages":[]}`,
		},
		{
			name:   "Anthropic",
			format: "anthropic",
			raw:    `{"model":"claude","messages":[]}`,
		},
		{
			name:   "OpenAI null",
			format: "openai",
			raw:    `{"model":"gpt-4o","max_tokens":null,"messages":[]}`,
		},
		{
			name:   "Anthropic null",
			format: "anthropic",
			raw:    `{"model":"claude","max_tokens":null,"messages":[]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizePassthroughRequest([]byte(tt.raw), tt.format, 3072, false)
			if err != nil {
				t.Fatalf("normalizePassthroughRequest 失败: %v", err)
			}
			body := decodeNormalizedBody(t, got)
			assertRawJSONEqual(t, body["max_tokens"], json.RawMessage(`3072`), "max_tokens")
		})
	}
}

func TestNormalizePassthroughRequestAlwaysCanonicalizesSecurityFields(t *testing.T) {
	tests := []struct {
		name            string
		format          string
		raw             string
		stream          bool
		completionField bool
	}{
		{
			name:   "OpenAI 非流请求已有 max_tokens",
			format: "openai",
			raw:    "{ \n  \"x_unknown\": true, \"max_tokens\": 512, \"model\": \"gpt-4o\" \n}",
		},
		{
			name:            "OpenAI 非流请求已有 max_completion_tokens",
			format:          "openai",
			raw:             `{"max_completion_tokens":512,"model":"o3"}`,
			completionField: true,
		},
		{
			name:   "OpenAI 流请求已经满足约束",
			format: "openai",
			raw:    `{"stream_options":{"include_usage":true},"max_tokens":512,"model":"gpt-4o"}`,
			stream: true,
		},
		{
			name:   "Anthropic 已有 max_tokens",
			format: "anthropic",
			raw:    `{"max_tokens":512,"model":"claude"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(tt.raw)
			got, err := normalizePassthroughRequest(raw, tt.format, 4096, tt.stream)
			if err != nil {
				t.Fatalf("normalizePassthroughRequest 失败: %v", err)
			}
			body := decodeNormalizedBody(t, got)
			field := "max_tokens"
			if tt.completionField {
				field = "max_completion_tokens"
			}
			assertRawJSONEqual(t, body[field], json.RawMessage(`4096`), field)
			assertRawJSONEqual(t, body["stream"], json.RawMessage(fmt.Sprintf("%t", tt.stream)), "stream")
			if tt.completionField {
				if _, exists := body["max_tokens"]; exists {
					t.Fatal("选择 max_completion_tokens 后应删除重复 max_tokens")
				}
			}
		})
	}
}

func TestNormalizePassthroughRequestRejectsNonObject(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `"text"`} {
		if _, err := normalizePassthroughRequest([]byte(raw), "openai", 4096, true); err == nil {
			t.Errorf("非对象请求 %s 应返回错误", raw)
		}
	}
}

func TestNormalizePassthroughRequestCollapsesDuplicateLimits(t *testing.T) {
	raw := []byte(`{"model":"gpt-4o","max_tokens":999999,"max_tokens":100,"stream":false}`)
	got, err := normalizePassthroughRequest(raw, "openai", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), `"max_tokens"`) != 1 {
		t.Fatalf("安全字段必须折叠为单一键: %s", got)
	}
	body := decodeNormalizedBody(t, got)
	assertRawJSONEqual(t, body["max_tokens"], json.RawMessage(`100`), "max_tokens")
}

func TestNormalizePassthroughRequestRejectsCaseAlias(t *testing.T) {
	raw := []byte(`{"model":"gpt-4o","MAX_TOKENS":999999,"max_tokens":100}`)
	if _, err := normalizePassthroughRequest(raw, "openai", 100, false); err == nil {
		t.Fatal("大小写别名可能导致上下游解析分歧，必须拒绝")
	}
}

func TestNormalizePassthroughRequestRejectsNestedIncludeUsageAlias(t *testing.T) {
	_, err := normalizePassthroughRequest([]byte(`{
		"model":"gpt-4o",
		"stream":true,
		"stream_options":{"INCLUDE_USAGE":false}
	}`), "openai", 64, true)
	if err == nil {
		t.Fatal("stream_options.include_usage 的大小写别名必须拒绝")
	}
}

func decodeNormalizedBody(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("规范化结果不是 JSON 对象: %v；body=%s", err, raw)
	}
	return body
}

func assertRawJSONEqual(t *testing.T, got, want json.RawMessage, field string) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("字段 %s 的实际值不是合法 JSON: %v", field, err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("字段 %s 的期望值不是合法 JSON: %v", field, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("字段 %s = %s，期望 %s", field, got, want)
	}
}
