package usage

import "strings"

const estimatedCharactersPerToken int64 = 4

// SummarizeUsageByRequestID aggregates usage log entries for each request ID.
func SummarizeUsageByRequestID(entriesByRequest map[string][]UsageLogEntry) map[string]*RequestUsageSummary {
	if len(entriesByRequest) == 0 {
		return nil
	}

	summaries := make(map[string]*RequestUsageSummary, len(entriesByRequest))
	for requestID, entries := range entriesByRequest {
		summary := SummarizeRequestUsage(entries)
		if summary == nil {
			continue
		}
		summaries[requestID] = summary
	}
	if len(summaries) == 0 {
		return nil
	}
	return summaries
}

// SummarizeRequestUsage aggregates one request's usage entries into a normalized summary.
func SummarizeRequestUsage(entries []UsageLogEntry) *RequestUsageSummary {
	if len(entries) == 0 {
		return nil
	}

	summary := &RequestUsageSummary{}
	for _, entry := range entries {
		uncachedInput, cachedInput, cacheWriteInput := requestInputSegments(entry)
		totalInput := uncachedInput + cachedInput + cacheWriteInput

		summary.Entries++
		summary.InputTokens += totalInput
		summary.UncachedInputTokens += uncachedInput
		summary.CachedInputTokens += cachedInput
		summary.CacheWriteInputTokens += cacheWriteInput
		summary.OutputTokens += int64(entry.OutputTokens)
	}

	summary.TotalTokens = summary.InputTokens + summary.OutputTokens
	if summary.InputTokens > 0 {
		summary.CachedInputRatio = float64(summary.CachedInputTokens) / float64(summary.InputTokens)
	}
	summary.EstimatedCachedCharacters = summary.CachedInputTokens * estimatedCharactersPerToken

	return summary
}

func requestInputSegments(entry UsageLogEntry) (uncachedInput, cachedInput, cacheWriteInput int64) {
	cacheReadTopLevel := int64(extractInt(entry.RawData, "cache_read_input_tokens"))
	cacheReadNormalized := int64(extractInt(entry.RawData, "prompt_cached_tokens"))
	cacheReadGeneric := int64(extractInt(entry.RawData, "cached_tokens"))
	cacheWriteInput = int64(extractInt(entry.RawData, "cache_creation_input_tokens"))

	cachedInput = maxInt64(cacheReadTopLevel, cacheReadNormalized, cacheReadGeneric)
	baseInput := int64(entry.InputTokens)

	if requestUsesSplitCacheAccounting(entry, cacheReadTopLevel, cacheWriteInput) {
		return baseInput, cachedInput, cacheWriteInput
	}

	if cachedInput > baseInput {
		cachedInput = baseInput
	}
	uncachedInput = baseInput - cachedInput
	return uncachedInput, cachedInput, cacheWriteInput
}

func requestUsesSplitCacheAccounting(entry UsageLogEntry, cacheReadInput, cacheWriteInput int64) bool {
	if cacheReadInput > 0 || cacheWriteInput > 0 {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(entry.Provider), "anthropic")
}

func maxInt64(values ...int64) int64 {
	var max int64
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}
