package usage

import (
	"context"
	"strings"
)

const estimatedCharactersPerToken int64 = 4

// RequestUsageLoader is the slice of the usage reader needed to summarize
// usage per request.
type RequestUsageLoader interface {
	GetUsageByRequestIDs(ctx context.Context, requestIDs []string) (map[string][]UsageLogEntry, error)
}

// SummarizeUsageForRequestIDs loads usage entries for requestIDs from reader
// and returns per-request summaries keyed by request ID. A nil reader or an
// empty ID list yields nil with no error.
func SummarizeUsageForRequestIDs(ctx context.Context, reader RequestUsageLoader, requestIDs []string) (map[string]*RequestUsageSummary, error) {
	if reader == nil || len(requestIDs) == 0 {
		return nil, nil
	}
	entries, err := reader.GetUsageByRequestIDs(ctx, requestIDs)
	if err != nil {
		return nil, err
	}
	return SummarizeUsageByRequestID(entries), nil
}

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
		uncachedInput, cachedInput, cacheWriteInput := EntryInputSegments(entry)
		totalInput := uncachedInput + cachedInput + cacheWriteInput

		summary.Entries++
		summary.InputTokens += totalInput
		summary.UncachedInputTokens += uncachedInput
		summary.CachedInputTokens += cachedInput
		summary.CacheWriteInputTokens += cacheWriteInput
		summary.OutputTokens += int64(entry.OutputTokens)
		summary.RewriteTokensSaved += entry.RewriteTokensSaved
		if entry.RewriteCostSaved != nil {
			total := *entry.RewriteCostSaved
			if summary.RewriteCostSaved != nil {
				total += *summary.RewriteCostSaved
			}
			summary.RewriteCostSaved = &total
		}
	}

	summary.TotalTokens = summary.InputTokens + summary.OutputTokens
	if summary.InputTokens > 0 {
		summary.CachedInputRatio = float64(summary.CachedInputTokens) / float64(summary.InputTokens)
	}
	summary.EstimatedCachedCharacters = summary.CachedInputTokens * estimatedCharactersPerToken

	return summary
}

// EntryInputSegments splits one usage log entry's input tokens into the
// provider-uncached prompt, the provider-cached read, and the provider cache
// write portions. Provider-specific quirks are handled here so callers —
// request summaries, the admin usage log, and the live SSE preview — stay in
// sync. The various provider field names are coalesced via max:
//   - cached reads: cache_read_input_tokens (Anthropic, Bedrock),
//     prompt_cached_tokens (OpenAI, DeepSeek), cached_tokens (Gemini)
//   - cache writes: cache_creation_input_tokens (Anthropic),
//     cache_write_input_tokens (Bedrock Converse)
func EntryInputSegments(entry UsageLogEntry) (uncachedInput, cachedInput, cacheWriteInput int64) {
	cacheReadTopLevel := int64(extractInt(entry.RawData, "cache_read_input_tokens"))
	cacheReadNormalized := int64(extractInt(entry.RawData, "prompt_cached_tokens"))
	cacheReadGeneric := int64(extractInt(entry.RawData, "cached_tokens"))
	cacheWriteCreation := int64(extractInt(entry.RawData, "cache_creation_input_tokens"))
	cacheWriteGeneric := int64(extractInt(entry.RawData, "cache_write_input_tokens"))
	cacheWriteInput = maxInt64(cacheWriteCreation, cacheWriteGeneric)

	cachedInput = maxInt64(cacheReadTopLevel, cacheReadNormalized, cacheReadGeneric)
	baseInput := int64(entry.InputTokens)

	if entryUsesSplitPromptCacheAccounting(entry, cacheReadTopLevel, cacheWriteInput) {
		return baseInput, cachedInput, cacheWriteInput
	}

	if cachedInput > baseInput {
		cachedInput = baseInput
	}
	uncachedInput = baseInput - cachedInput
	return uncachedInput, cachedInput, cacheWriteInput
}

func entryUsesSplitPromptCacheAccounting(entry UsageLogEntry, cacheReadInput, cacheWriteInput int64) bool {
	if cacheReadInput > 0 || cacheWriteInput > 0 {
		return true
	}
	// Anthropic reports input_tokens as uncached prompt input; prompt-cache
	// reads and writes are separate fields when present.
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
