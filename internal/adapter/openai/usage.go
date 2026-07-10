package openai

import (
	"math"

	"linapi/internal/canonical"
)

// canonicalUsageFromWire 在保留字段存在性的前提下转换 OpenAI usage。
func canonicalUsageFromWire(in *usage) *canonical.Usage {
	if in == nil {
		return nil
	}
	out := &canonical.Usage{}
	if in.PromptTokens != nil {
		out.InputTokens = *in.PromptTokens
		out.InputTokensKnown = true
		if in.PromptTokenDetails != nil && in.PromptTokenDetails.CachedTokens != nil {
			cached := *in.PromptTokenDetails.CachedTokens
			if cached < 0 || cached > *in.PromptTokens {
				// 保留为非法值，让 Billing 走预授权上限，不能把冲突 usage
				// 当成普通输入或零缓存精确结算。
				out.CacheReadInputTokens = -1
			} else {
				out.InputTokens = *in.PromptTokens - cached
				out.CacheReadInputTokens = cached
			}
		}
	} else if in.PromptTokenDetails != nil && in.PromptTokenDetails.CachedTokens != nil {
		out.CacheReadInputTokens = *in.PromptTokenDetails.CachedTokens
	}
	if in.CompletionTokens != nil {
		out.OutputTokens = *in.CompletionTokens
		out.OutputTokensKnown = true
	}
	if in.TotalTokens != nil {
		out.ReportedTotalTokens = *in.TotalTokens
		out.TotalTokensKnown = true
	}
	return out
}

// wireUsageFromCanonical 仅输出 canonical 明确知道的 token 字段，绝不把缺失
// 字段伪造成 0。双边都已知且上游未报告 total 时可安全补出合计。
func wireUsageFromCanonical(in canonical.Usage) *usage {
	out := &usage{}
	if in.InputTokensKnown {
		prompt, ok := checkedTokenSum(in.InputTokens, in.CacheCreationInputTokens, in.CacheReadInputTokens)
		if ok {
			out.PromptTokens = intPointer(prompt)
			if in.CacheReadInputTokens > 0 {
				out.PromptTokenDetails = &promptTokenDetails{CachedTokens: intPointer(in.CacheReadInputTokens)}
			}
		}
	}
	if in.OutputTokensKnown {
		out.CompletionTokens = intPointer(in.OutputTokens)
	}
	if in.TotalTokensKnown {
		out.TotalTokens = intPointer(in.ReportedTotalTokens)
	} else if out.PromptTokens != nil && in.OutputTokensKnown && in.OutputTokens >= 0 &&
		*out.PromptTokens <= math.MaxInt-in.OutputTokens {
		out.TotalTokens = intPointer(*out.PromptTokens + in.OutputTokens)
	}
	if out.PromptTokens == nil && out.CompletionTokens == nil && out.TotalTokens == nil {
		return nil
	}
	return out
}

func checkedTokenSum(values ...int) (int, bool) {
	total := 0
	for _, value := range values {
		if value < 0 || total > math.MaxInt-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func intPointer(v int) *int { return &v }
