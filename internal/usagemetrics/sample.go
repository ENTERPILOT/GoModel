package usagemetrics

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/usage"
)

// Sample is what one usage entry adds to the token and cost metrics.
//
// InputTokens and OutputTokens are complete: input covers the uncached
// prompt, provider-cached reads and cache writes, and output covers reasoning.
// The three detail counts are parts of those totals, never additions to them.
type Sample struct {
	// Provider is the configured provider name, ProviderType its type.
	Provider     string
	ProviderType string
	// Model is the model that answered, RequestModel the one the request was
	// routed to (empty when the entry does not record it).
	Model        string
	RequestModel string
	Endpoint     string
	Caller       Caller

	InputTokens  int64
	OutputTokens int64

	CachedInputTokens     int64
	CacheWriteInputTokens int64
	ReasoningOutputTokens int64

	// ProviderCost is what the provider charges for the request and
	// BilledCost what the caller is billed, both in USD. Zero means free,
	// unknown, or (for BilledCost) that pricing markups are off.
	ProviderCost float64
	BilledCost   float64
}

// newSample reads a usage entry. A response-cache hit never reached a
// provider, so it carries no tokens and no provider cost; it keeps the billed
// cost, which is non-zero only when cache hits are billed.
func newSample(entry *usage.UsageEntry) Sample {
	s := Sample{
		Provider:     strings.TrimSpace(entry.ProviderName),
		ProviderType: strings.TrimSpace(entry.Provider),
		Model:        strings.TrimSpace(entry.Model),
		RequestModel: strings.TrimSpace(entry.RoutedModel),
		Endpoint:     strings.TrimSpace(entry.Endpoint),
	}
	if s.Provider == "" {
		// Entries logged without an instance name carry only the type, which
		// is also the default instance name.
		s.Provider = s.ProviderType
	}
	if entry.BilledCost != nil {
		s.BilledCost = *entry.BilledCost
	}
	if entry.CacheType != "" {
		return s
	}

	uncached, cached, cacheWrite := usage.EntryInputSegments(usage.UsageLogEntry{
		Provider:    entry.Provider,
		InputTokens: entry.InputTokens,
		RawData:     entry.RawData,
	})
	s.InputTokens = uncached + cached + cacheWrite
	s.CachedInputTokens = cached
	s.CacheWriteInputTokens = cacheWrite

	reasoning, included := usage.EntryReasoningTokens(s.ProviderType, entry.RawData)
	s.OutputTokens = int64(max(entry.OutputTokens, 0))
	if !included {
		s.OutputTokens += reasoning
	}
	s.ReasoningOutputTokens = min(reasoning, s.OutputTokens)

	if entry.TotalCost != nil {
		s.ProviderCost = *entry.TotalCost
	}
	return s
}

// empty reports whether the sample would change no metric, as for an MCP
// tool call, which records neither tokens nor cost.
func (s Sample) empty() bool {
	return s.InputTokens <= 0 && s.OutputTokens <= 0 && s.ProviderCost <= 0 && s.BilledCost <= 0
}
