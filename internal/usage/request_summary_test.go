package usage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSummarizeRequestUsage_OpenAICompatibleCachedTokens(t *testing.T) {
	summary := SummarizeRequestUsage([]UsageLogEntry{
		{
			Provider:     "openai",
			InputTokens:  120,
			OutputTokens: 30,
			RawData: map[string]any{
				"prompt_cached_tokens": 80,
			},
		},
	})
	require.NotNil(t, summary)
	require.Equal(t, int64(120), summary.InputTokens)
	require.Equal(t, int64(40), summary.UncachedInputTokens)
	require.Equal(t, int64(80), summary.CachedInputTokens)
	require.Equal(t, int64(150), summary.TotalTokens)
	require.Equal(t, int64(320), summary.EstimatedCachedCharacters)
}

func TestSummarizeRequestUsage_RewriteSavings(t *testing.T) {
	cost1 := 0.03125
	cost2 := 0.015625
	costSum := cost1 + cost2
	cases := []struct {
		name       string
		entries    []UsageLogEntry
		wantTokens int64
		wantCost   *float64
	}{
		{
			name: "aggregates tokens and cost across entries",
			entries: []UsageLogEntry{
				{Provider: "openai", InputTokens: 100, RewriteTokensSaved: 89, RewriteCostSaved: &cost1},
				{Provider: "openai", InputTokens: 50, RewriteTokensSaved: 11, RewriteCostSaved: &cost2},
			},
			wantTokens: 100,
			wantCost:   &costSum,
		},
		{
			name: "cost priced on only one entry",
			entries: []UsageLogEntry{
				{Provider: "openai", InputTokens: 100, RewriteTokensSaved: 89, RewriteCostSaved: &cost1},
				{Provider: "openai", InputTokens: 50, RewriteTokensSaved: 11},
			},
			wantTokens: 100,
			wantCost:   &cost1,
		},
		{
			name:       "no savings leaves cost nil",
			entries:    []UsageLogEntry{{Provider: "openai", InputTokens: 100}},
			wantTokens: 0,
			wantCost:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := SummarizeRequestUsage(tc.entries)
			require.NotNil(t, summary)
			require.Equal(t, tc.wantTokens, summary.RewriteTokensSaved)

			if tc.wantCost == nil {
				require.Nil(t, summary.RewriteCostSaved)
			} else {
				require.NotNil(t, summary.RewriteCostSaved)
				require.Equal(t, *tc.wantCost, *summary.RewriteCostSaved)
			}
		})
	}
}

func TestSummarizeRequestUsage_AnthropicSplitCacheAccounting(t *testing.T) {
	summary := SummarizeRequestUsage([]UsageLogEntry{
		{
			Provider:     "anthropic",
			InputTokens:  50,
			OutputTokens: 20,
			RawData: map[string]any{
				"cache_read_input_tokens":     90,
				"cache_creation_input_tokens": 30,
			},
		},
	})
	require.NotNil(t, summary)
	require.Equal(t, int64(170), summary.InputTokens)
	require.Equal(t, int64(50), summary.UncachedInputTokens)
	require.Equal(t, int64(90), summary.CachedInputTokens)
	require.Equal(t, int64(30), summary.CacheWriteInputTokens)
	require.Equal(t, int64(190), summary.TotalTokens)
}

func TestSummarizeRequestUsage_AnthropicSplitCacheAccountingWithoutCacheFields(t *testing.T) {
	summary := SummarizeRequestUsage([]UsageLogEntry{
		{
			Provider:     "anthropic",
			InputTokens:  50,
			OutputTokens: 20,
		},
	})
	require.NotNil(t, summary)
	require.Equal(t, int64(50), summary.InputTokens)
	require.Equal(t, int64(50), summary.UncachedInputTokens)
	require.Equal(t, int64(0), summary.CachedInputTokens)
	require.Equal(t, int64(0), summary.CacheWriteInputTokens)
	require.Equal(t, int64(70), summary.TotalTokens)
}

func TestSummarizeUsageByRequestID(t *testing.T) {
	summaries := SummarizeUsageByRequestID(map[string][]UsageLogEntry{
		"req-1": {
			{Provider: "openai", InputTokens: 10, OutputTokens: 5},
		},
		"req-2": {
			{Provider: "openai", InputTokens: 20, OutputTokens: 10},
		},
	})
	require.Len(t, summaries, 2)
	require.Equal(t, int64(15), summaries["req-1"].TotalTokens)
	require.Equal(t, int64(30), summaries["req-2"].TotalTokens)
}
