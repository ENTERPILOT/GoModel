package usage

import (
	"testing"

	"github.com/stretchr/testify/require"
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
	require.Equal(t, int64(40), entry.UncachedInputTokens)
	require.Equal(t, int64(80), entry.CachedInputTokens)
	require.Equal(t, int64(0), entry.CacheWriteInputTokens)

	want := 80.0 / 120.0
	require.InDelta(t, want, entry.CachedInputRatio, 1e-9)
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
	require.Equal(t, int64(50), entry.UncachedInputTokens)
	require.Equal(t, int64(90), entry.CachedInputTokens)
	require.Equal(t, int64(30), entry.CacheWriteInputTokens)

	want := 90.0 / 170.0
	require.InDelta(t, want, entry.CachedInputRatio, 1e-9)
}

func TestEnrichUsageLogEntry_NoCacheData(t *testing.T) {
	entry := UsageLogEntry{
		Provider:    "openai",
		InputTokens: 100,
	}
	EnrichUsageLogEntry(&entry)
	require.Equal(t, int64(100), entry.UncachedInputTokens)
	require.Equal(t, int64(0), entry.CachedInputTokens)
	require.Equal(t, float64(0), entry.CachedInputRatio)
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
	require.Equal(t, int64(40), entry.UncachedInputTokens)
	require.Equal(t, int64(120), entry.CachedInputTokens)
	require.Equal(t, int64(60), entry.CacheWriteInputTokens)

	want := 120.0 / 220.0
	require.InDelta(t, want, entry.CachedInputRatio, 1e-9)
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
	require.Equal(t, int64(100), entry.UncachedInputTokens)
	require.Equal(t, int64(0), entry.CachedInputTokens)
	require.Equal(t, int64(80), entry.CacheWriteInputTokens)
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
	require.Equal(t, int64(60), entry.CacheWriteInputTokens)
}

func TestEnrichUsageLogEntry_NilSafe(t *testing.T) {
	EnrichUsageLogEntry(nil)
}
