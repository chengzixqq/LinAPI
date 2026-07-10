package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"linapi/internal/canonical"
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param,omitempty"`
	Code    any    `json:"code,omitempty"`
}

// ParseError 把 OpenAI 风格 HTTP 错误体解析为规范错误。
func (a *Adapter) ParseError(raw []byte) (*canonical.ErrorResponse, error) {
	var envelope errorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("openai: 解析错误响应失败: %w", err)
	}
	if err := ensureErrorEOF(decoder); err != nil {
		return nil, err
	}
	if envelope.Error.Message == "" {
		return nil, fmt.Errorf("openai: 错误响应缺少 error.message")
	}
	errType := envelope.Error.Type
	if errType == "" {
		errType = "upstream_error"
	}
	return &canonical.ErrorResponse{
		Type:    errType,
		Message: envelope.Error.Message,
		Param:   envelope.Error.Param,
		Code:    envelope.Error.Code,
	}, nil
}

// BuildError 把规范错误编码为 OpenAI 风格 HTTP 错误体。
func (a *Adapter) BuildError(errResp *canonical.ErrorResponse) ([]byte, error) {
	if errResp == nil {
		return nil, fmt.Errorf("openai: 规范错误为空")
	}
	return json.Marshal(errorEnvelope{Error: errorBody{
		Message: errResp.Message,
		Type:    errResp.Type,
		Param:   errResp.Param,
		Code:    errResp.Code,
	}})
}

func ensureErrorEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("openai: 错误响应包含多个 JSON 值")
		}
		return fmt.Errorf("openai: 解析错误响应尾部失败: %w", err)
	}
	return nil
}
