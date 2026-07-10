package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"
)

// normalizePassthroughRequest 为同格式直通请求补齐网关计费所需的最小字段。
// 始终重编码并覆盖安全相关字段，消除重复 JSON key 在网关与兼容上游采用
// first-wins/last-wins 差异时造成的边界绕过；json.RawMessage 保留未知字段语义。
func normalizePassthroughRequest(raw []byte, format string, maxOutput int, stream bool) ([]byte, error) {
	if format != "openai" && format != "anthropic" {
		return raw, nil
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("forwarder: 解析直通请求失败: %w", err)
	}
	if body == nil {
		return nil, fmt.Errorf("forwarder: 直通请求必须是 JSON 对象")
	}
	for key := range body {
		for _, protected := range []string{"model", "max_tokens", "max_completion_tokens", "n", "stream", "stream_options"} {
			if key != protected && strings.EqualFold(key, protected) {
				return nil, fmt.Errorf("forwarder: 安全字段 %q 必须使用规范小写名称", key)
			}
		}
	}

	switch format {
	case "openai":
		if hasNonNullJSON(body, "max_completion_tokens") {
			body["max_completion_tokens"] = marshalInt(maxOutput)
			delete(body, "max_tokens")
		} else {
			body["max_tokens"] = marshalInt(maxOutput)
			delete(body, "max_completion_tokens")
		}
		body["stream"] = marshalBool(stream)

		if stream {
			_, err := forceOpenAIStreamUsage(body)
			if err != nil {
				return nil, err
			}
		}

	case "anthropic":
		body["max_tokens"] = marshalInt(maxOutput)
		body["stream"] = marshalBool(stream)
	}

	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("forwarder: 编码直通请求失败: %w", err)
	}
	return out, nil
}

func hasNonNullJSON(body map[string]json.RawMessage, key string) bool {
	raw, ok := body[key]
	return ok && string(raw) != "null"
}

// forceOpenAIStreamUsage 强制 OpenAI 流返回最终 usage。
// stream_options 为 null 或非对象时以新对象替换；对象中的未知字段原样保留。
func forceOpenAIStreamUsage(body map[string]json.RawMessage) (bool, error) {
	rawOptions, exists := body["stream_options"]
	options := make(map[string]json.RawMessage)
	if exists {
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(rawOptions, &parsed); err == nil && parsed != nil {
			options = parsed
		}
	}
	for key := range options {
		if key != "include_usage" && strings.EqualFold(key, "include_usage") {
			return false, fmt.Errorf("forwarder: stream_options 安全字段 %q 必须使用规范小写名称", key)
		}
	}

	if rawInclude, ok := options["include_usage"]; ok {
		var include bool
		if err := json.Unmarshal(rawInclude, &include); err == nil && include {
			return false, nil
		}
	}

	options["include_usage"] = json.RawMessage("true")
	encoded, err := json.Marshal(options)
	if err != nil {
		return false, fmt.Errorf("forwarder: 编码 OpenAI stream_options 失败: %w", err)
	}
	body["stream_options"] = encoded
	return true, nil
}

func marshalInt(v int) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func marshalBool(v bool) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// rewriteNormalizedRequestModel 在同协议模型别名场景只改 model，保留已经规范化
// 的 max_completion_tokens 等能力字段，避免重新 BuildRequest 后退化为旧字段。
func rewriteNormalizedRequestModel(raw []byte, model string) ([]byte, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, fmt.Errorf("forwarder: 改写模型名时请求不是 JSON 对象")
	}
	encoded, _ := json.Marshal(model)
	body["model"] = encoded
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("forwarder: 编码模型别名请求失败: %w", err)
	}
	return out, nil
}
