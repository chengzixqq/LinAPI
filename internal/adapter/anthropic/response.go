package anthropic

import (
	"encoding/json"
	"fmt"

	"linapi/internal/canonical"
)

// ParseResponse 把 Anthropic 非流式响应解析为规范响应。
func (a *Adapter) ParseResponse(raw []byte) (*canonical.Response, error) {
	var resp messagesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("anthropic: 解析响应失败: %w", err)
	}

	out := &canonical.Response{
		ID:         resp.ID,
		Model:      resp.Model,
		Role:       canonical.RoleAssistant,
		StopReason: mapStopReasonToCanonical(resp.StopReason),
	}
	for _, b := range resp.Content {
		out.Content = append(out.Content, blockToCanonical(b))
	}
	if usage := canonicalUsageFromWire(resp.Usage); usage != nil {
		out.Usage = *usage
	}
	return out, nil
}

// BuildResponse 把规范响应构造为 Anthropic 非流式响应体。
func (a *Adapter) BuildResponse(resp *canonical.Response) ([]byte, error) {
	out := messagesResponse{
		ID:         resp.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      resp.Model,
		StopReason: mapStopReasonToWire(resp.StopReason),
		Usage:      wireUsageFromCanonical(resp.Usage),
	}
	for _, b := range resp.Content {
		wire, err := canonicalToBlock(b)
		if err != nil {
			return nil, fmt.Errorf("anthropic: 编码响应 block 失败: %w", err)
		}
		out.Content = append(out.Content, wire)
	}
	return json.Marshal(out)
}

// mapStopReasonToCanonical 把 Claude stop_reason 映射为规范停止原因。
func mapStopReasonToCanonical(r string) canonical.StopReason {
	switch r {
	case "end_turn":
		return canonical.StopEndTurn
	case "max_tokens":
		return canonical.StopMaxTokens
	case "tool_use":
		return canonical.StopToolUse
	case "stop_sequence":
		return canonical.StopStop
	default:
		return canonical.StopEndTurn
	}
}

// mapStopReasonToWire 把规范停止原因映射回 Claude stop_reason。
func mapStopReasonToWire(r canonical.StopReason) string {
	switch r {
	case canonical.StopEndTurn:
		return "end_turn"
	case canonical.StopMaxTokens:
		return "max_tokens"
	case canonical.StopToolUse:
		return "tool_use"
	case canonical.StopStop:
		return "stop_sequence"
	default:
		return "end_turn"
	}
}
