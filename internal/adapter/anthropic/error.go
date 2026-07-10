package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"linapi/internal/canonical"
)

type errorEnvelope struct {
	Type      string    `json:"type"`
	Error     errorBody `json:"error"`
	RequestID string    `json:"request_id,omitempty"`
}

type errorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ParseError 把 Anthropic 风格 HTTP 错误体解析为规范错误。
func (a *Adapter) ParseError(raw []byte) (*canonical.ErrorResponse, error) {
	var envelope errorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("anthropic: 解析错误响应失败: %w", err)
	}
	if err := ensureErrorEOF(decoder); err != nil {
		return nil, err
	}
	if envelope.Type != "" && envelope.Type != "error" {
		return nil, fmt.Errorf("anthropic: 错误响应 type=%q", envelope.Type)
	}
	if envelope.Error.Message == "" {
		return nil, fmt.Errorf("anthropic: 错误响应缺少 error.message")
	}
	errType := envelope.Error.Type
	if errType == "" {
		errType = "upstream_error"
	}
	return &canonical.ErrorResponse{
		Type:      errType,
		Message:   envelope.Error.Message,
		RequestID: envelope.RequestID,
	}, nil
}

// BuildError 把规范错误编码为 Anthropic 风格 HTTP 错误体。
func (a *Adapter) BuildError(errResp *canonical.ErrorResponse) ([]byte, error) {
	if errResp == nil {
		return nil, fmt.Errorf("anthropic: 规范错误为空")
	}
	return json.Marshal(errorEnvelope{
		Type: "error",
		Error: errorBody{
			Type:    errResp.Type,
			Message: errResp.Message,
		},
		RequestID: errResp.RequestID,
	})
}

func ensureErrorEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("anthropic: 错误响应包含多个 JSON 值")
		}
		return fmt.Errorf("anthropic: 解析错误响应尾部失败: %w", err)
	}
	return nil
}
