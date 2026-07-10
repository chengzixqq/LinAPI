package anthropic

import "linapi/internal/canonical"

func canonicalUsageFromWire(in *usage) *canonical.Usage {
	if in == nil {
		return nil
	}
	out := &canonical.Usage{
		CacheCreationInputTokens: in.CacheCreationInputTokens,
		CacheReadInputTokens:     in.CacheReadInputTokens,
	}
	if in.InputTokens != nil {
		out.InputTokens = *in.InputTokens
		out.InputTokensKnown = true
	}
	if in.OutputTokens != nil {
		out.OutputTokens = *in.OutputTokens
		out.OutputTokensKnown = true
	}
	return out
}

func wireUsageFromCanonical(in canonical.Usage) *usage {
	out := &usage{
		CacheCreationInputTokens: in.CacheCreationInputTokens,
		CacheReadInputTokens:     in.CacheReadInputTokens,
	}
	if in.InputTokensKnown {
		out.InputTokens = intPointer(in.InputTokens)
	}
	if in.OutputTokensKnown {
		out.OutputTokens = intPointer(in.OutputTokens)
	}
	if out.InputTokens == nil && out.OutputTokens == nil &&
		out.CacheCreationInputTokens == 0 && out.CacheReadInputTokens == 0 {
		return nil
	}
	return out
}

func intPointer(v int) *int { return &v }
