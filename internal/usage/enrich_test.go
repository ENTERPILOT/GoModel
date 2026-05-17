package usage

import (
	"math"
	"testing"
)

func TestEnrichUsageLogEntry_OpenAICachedTokens(t *testing.T) {
	entry := UsageLogEntry{
		Provider:    "openai",
		InputTokens: 120,
		RawData: map[string]any{
			"prompt_cached_tokens": 80,
		},
	}
	EnrichUsageLogEntry(&entry)
	if entry.UncachedInputTokens != 40 {
		t.Fatalf("UncachedInputTokens = %d, want 40", entry.UncachedInputTokens)
	}
	if entry.CachedInputTokens != 80 {
		t.Fatalf("CachedInputTokens = %d, want 80", entry.CachedInputTokens)
	}
	if entry.CacheWriteInputTokens != 0 {
		t.Fatalf("CacheWriteInputTokens = %d, want 0", entry.CacheWriteInputTokens)
	}
	want := 80.0 / 120.0
	if math.Abs(entry.CachedInputRatio-want) > 1e-9 {
		t.Fatalf("CachedInputRatio = %f, want %f", entry.CachedInputRatio, want)
	}
}

func TestEnrichUsageLogEntry_AnthropicSplitAccounting(t *testing.T) {
	entry := UsageLogEntry{
		Provider:    "anthropic",
		InputTokens: 50,
		RawData: map[string]any{
			"cache_read_input_tokens":     90,
			"cache_creation_input_tokens": 30,
		},
	}
	EnrichUsageLogEntry(&entry)
	if entry.UncachedInputTokens != 50 {
		t.Fatalf("UncachedInputTokens = %d, want 50", entry.UncachedInputTokens)
	}
	if entry.CachedInputTokens != 90 {
		t.Fatalf("CachedInputTokens = %d, want 90", entry.CachedInputTokens)
	}
	if entry.CacheWriteInputTokens != 30 {
		t.Fatalf("CacheWriteInputTokens = %d, want 30", entry.CacheWriteInputTokens)
	}
	want := 90.0 / 170.0
	if math.Abs(entry.CachedInputRatio-want) > 1e-9 {
		t.Fatalf("CachedInputRatio = %f, want %f", entry.CachedInputRatio, want)
	}
}

func TestEnrichUsageLogEntry_NoCacheData(t *testing.T) {
	entry := UsageLogEntry{
		Provider:    "openai",
		InputTokens: 100,
	}
	EnrichUsageLogEntry(&entry)
	if entry.UncachedInputTokens != 100 {
		t.Fatalf("UncachedInputTokens = %d, want 100", entry.UncachedInputTokens)
	}
	if entry.CachedInputTokens != 0 {
		t.Fatalf("CachedInputTokens = %d, want 0", entry.CachedInputTokens)
	}
	if entry.CachedInputRatio != 0 {
		t.Fatalf("CachedInputRatio = %f, want 0", entry.CachedInputRatio)
	}
}

func TestEnrichUsageLogEntry_BedrockCacheWriteField(t *testing.T) {
	entry := UsageLogEntry{
		Provider:    "bedrock",
		InputTokens: 40,
		RawData: map[string]any{
			"cache_read_input_tokens":  120,
			"cache_write_input_tokens": 60,
		},
	}
	EnrichUsageLogEntry(&entry)
	if entry.UncachedInputTokens != 40 {
		t.Fatalf("UncachedInputTokens = %d, want 40", entry.UncachedInputTokens)
	}
	if entry.CachedInputTokens != 120 {
		t.Fatalf("CachedInputTokens = %d, want 120", entry.CachedInputTokens)
	}
	if entry.CacheWriteInputTokens != 60 {
		t.Fatalf("CacheWriteInputTokens = %d, want 60", entry.CacheWriteInputTokens)
	}
	want := 120.0 / 220.0
	if math.Abs(entry.CachedInputRatio-want) > 1e-9 {
		t.Fatalf("CachedInputRatio = %f, want %f", entry.CachedInputRatio, want)
	}
}

func TestEnrichUsageLogEntry_BedrockCacheWriteOnly(t *testing.T) {
	// First request that writes to the cache reports only cache_write_input_tokens.
	entry := UsageLogEntry{
		Provider:    "bedrock",
		InputTokens: 100,
		RawData: map[string]any{
			"cache_write_input_tokens": 80,
		},
	}
	EnrichUsageLogEntry(&entry)
	if entry.UncachedInputTokens != 100 {
		t.Fatalf("UncachedInputTokens = %d, want 100", entry.UncachedInputTokens)
	}
	if entry.CachedInputTokens != 0 {
		t.Fatalf("CachedInputTokens = %d, want 0", entry.CachedInputTokens)
	}
	if entry.CacheWriteInputTokens != 80 {
		t.Fatalf("CacheWriteInputTokens = %d, want 80", entry.CacheWriteInputTokens)
	}
}

func TestEnrichUsageLogEntry_CoalescesCacheWriteFieldsByMax(t *testing.T) {
	// If a provider's raw_data ever carries both Anthropic-style
	// cache_creation_input_tokens and Bedrock-style cache_write_input_tokens,
	// EntryInputSegments must pick the larger value so the displayed
	// provider-cache totals are not undercounted.
	entry := UsageLogEntry{
		Provider:    "bedrock",
		InputTokens: 40,
		RawData: map[string]any{
			"cache_creation_input_tokens": 30,
			"cache_write_input_tokens":    60,
		},
	}
	EnrichUsageLogEntry(&entry)
	if entry.CacheWriteInputTokens != 60 {
		t.Fatalf("CacheWriteInputTokens = %d, want 60", entry.CacheWriteInputTokens)
	}
}

func TestEnrichUsageLogEntry_NilSafe(t *testing.T) {
	EnrichUsageLogEntry(nil)
}
