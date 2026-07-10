package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	"linapi/internal/routing"
)

const (
	openAIMaxTokensField           = "max_tokens"
	openAIMaxCompletionTokensField = "max_completion_tokens"
)

// OpenAIOutputLimitResolver 将“上游实际模型/渠道”绑定到其真正识别的输出上限
// 字段。release 模式不允许从客户端请求猜测，避免宽松兼容上游忽略字段后突破预授权。
type OpenAIOutputLimitResolver struct {
	fields map[string]string
	strict bool
}

func NewOpenAIOutputLimitResolver(fields map[string]string, strict bool) (*OpenAIOutputLimitResolver, error) {
	copyFields := make(map[string]string, len(fields))
	for key, value := range fields {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || !isOpenAIOutputLimitField(value) {
			return nil, fmt.Errorf("forwarder: OpenAI 输出上限策略 %q=%q 非法", key, value)
		}
		copyFields[key] = value
	}
	return &OpenAIOutputLimitResolver{fields: copyFields, strict: strict}, nil
}

// ValidateChannels 在 release 启动前验证全部启用 OpenAI 模型都有明确字段策略。
func (r *OpenAIOutputLimitResolver) ValidateChannels(channels []*routing.Channel) error {
	for _, ch := range channels {
		if err := r.ValidateChannel(ch); err != nil {
			return err
		}
	}
	return nil
}

func (r *OpenAIOutputLimitResolver) ValidateChannel(ch *routing.Channel) error {
	if ch == nil || !ch.Enabled || ch.Format != routing.FormatOpenAI {
		return nil
	}
	for publicModel := range ch.Models {
		if _, err := r.Resolve(ch.ID, ch.UpstreamModel(publicModel), ""); err != nil {
			return err
		}
	}
	return nil
}

func (r *OpenAIOutputLimitResolver) Resolve(channelID, upstreamModel, fallback string) (string, error) {
	if r != nil {
		if field, ok := r.fields[channelID+"/"+upstreamModel]; ok {
			return field, nil
		}
		if field, ok := r.fields[upstreamModel]; ok {
			return field, nil
		}
		if r.strict {
			return "", fmt.Errorf("forwarder: OpenAI 渠道 %q 的上游模型 %q 缺少输出上限字段策略", channelID, upstreamModel)
		}
	}
	if isOpenAIOutputLimitField(fallback) {
		return fallback, nil
	}
	return openAIMaxTokensField, nil
}

func isOpenAIOutputLimitField(field string) bool {
	return field == openAIMaxTokensField || field == openAIMaxCompletionTokensField
}

// patchOpenAIOutputLimit 删除客户端选定的两个同义字段，只写已由渠道策略验证的
// 唯一字段。它在每个候选的真实 upstream model 已知后执行，且发生在 MarkInFlight 前。
func patchOpenAIOutputLimit(raw []byte, field string, maxOutput int) ([]byte, error) {
	if !isOpenAIOutputLimitField(field) || maxOutput <= 0 {
		return nil, fmt.Errorf("forwarder: OpenAI 输出上限策略非法")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, fmt.Errorf("forwarder: 改写 OpenAI 输出上限时请求不是 JSON 对象")
	}
	delete(body, openAIMaxTokensField)
	delete(body, openAIMaxCompletionTokensField)
	body[field] = marshalInt(maxOutput)
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("forwarder: 编码 OpenAI 输出上限失败: %w", err)
	}
	return out, nil
}

func openAIOutputLimitFallback(raw []byte) string {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) == nil && hasNonNullJSON(body, openAIMaxCompletionTokensField) {
		return openAIMaxCompletionTokensField
	}
	return openAIMaxTokensField
}

// enforceCandidateOutputLimit 在候选的真实上游模型已知后写入唯一有效的 OpenAI
// 输出上限字段。该步骤必须发生在 MarkInFlight 和任何网络 I/O 之前。
func (f *Forwarder) enforceCandidateOutputLimit(ch *routing.Channel, fc *forwardCtx, body []byte) ([]byte, error) {
	if ch.Format != routing.FormatOpenAI {
		return body, nil
	}
	field, err := f.outputLimits.Resolve(
		ch.ID,
		ch.UpstreamModel(fc.clientModel),
		openAIOutputLimitFallback(body),
	)
	if err != nil {
		return nil, err
	}
	return patchOpenAIOutputLimit(body, field, *fc.req.MaxTokens)
}
