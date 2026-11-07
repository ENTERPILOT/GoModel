package usage

import (
	"context"
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
//     prompt_cached_tokens (OpenAI shape), cached_tokens (Gemini)
//   - cache writes: cache_creation_input_tokens (Anthropic),
//     cache_write_input_tokens (Bedrock Converse),
//     prompt_cache_write_tokens (OpenAI shape)
//
// The Anthropic-named counts come on top of input_tokens; every other name is
// part of it. A row's names therefore say how to split it: Claude rows recorded
// from translated traffic before input_tokens included the cache, and rows from
// native Anthropic traffic, carry the Anthropic names.
func EntryInputSegments(entry UsageLogEntry) (uncachedInput, cachedInput, cacheWriteInput int64) {
	cacheReadAnthropic := int64(extractInt(entry.RawData, "cache_read_input_tokens"))
	cacheWriteAnthropic := maxInt64(
		int64(extractInt(entry.RawData, "cache_creation_input_tokens")),
		int64(extractInt(entry.RawData, "cache_write_input_tokens")),
	)
	cachedInput = maxInt64(
		cacheReadAnthropic,
		int64(extractInt(entry.RawData, "prompt_cached_tokens")),
		int64(extractInt(entry.RawData, "cached_tokens")),
	)
	baseInput := int64(entry.InputTokens)

	cacheWriteInput = int64(extractInt(entry.RawData, "prompt_cache_write_tokens"))

	// A cache count larger than input_tokens cannot be part of it (a Claude
	// Responses body cached before input_tokens included the cache).
	if cacheReadAnthropic > 0 || cacheWriteAnthropic > 0 || cachedInput+cacheWriteInput > baseInput {
		return baseInput, cachedInput, maxInt64(cacheWriteAnthropic, cacheWriteInput)
	}
	return baseInput - cachedInput - cacheWriteInput, cachedInput, cacheWriteInput
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
