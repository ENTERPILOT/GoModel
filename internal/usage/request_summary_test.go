package usage

import "testing"

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
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if summary.InputTokens != 120 {
		t.Fatalf("InputTokens = %d, want 120", summary.InputTokens)
	}
	if summary.UncachedInputTokens != 40 {
		t.Fatalf("UncachedInputTokens = %d, want 40", summary.UncachedInputTokens)
	}
	if summary.CachedInputTokens != 80 {
		t.Fatalf("CachedInputTokens = %d, want 80", summary.CachedInputTokens)
	}
	if summary.TotalTokens != 150 {
		t.Fatalf("TotalTokens = %d, want 150", summary.TotalTokens)
	}
	if summary.EstimatedCachedCharacters != 320 {
		t.Fatalf("EstimatedCachedCharacters = %d, want 320", summary.EstimatedCachedCharacters)
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
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if summary.InputTokens != 170 {
		t.Fatalf("InputTokens = %d, want 170", summary.InputTokens)
	}
	if summary.UncachedInputTokens != 50 {
		t.Fatalf("UncachedInputTokens = %d, want 50", summary.UncachedInputTokens)
	}
	if summary.CachedInputTokens != 90 {
		t.Fatalf("CachedInputTokens = %d, want 90", summary.CachedInputTokens)
	}
	if summary.CacheWriteInputTokens != 30 {
		t.Fatalf("CacheWriteInputTokens = %d, want 30", summary.CacheWriteInputTokens)
	}
	if summary.TotalTokens != 190 {
		t.Fatalf("TotalTokens = %d, want 190", summary.TotalTokens)
	}
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
	if len(summaries) != 2 {
		t.Fatalf("len(summaries) = %d, want 2", len(summaries))
	}
	if summaries["req-1"].TotalTokens != 15 {
		t.Fatalf("summaries[req-1].TotalTokens = %d, want 15", summaries["req-1"].TotalTokens)
	}
	if summaries["req-2"].TotalTokens != 30 {
		t.Fatalf("summaries[req-2].TotalTokens = %d, want 30", summaries["req-2"].TotalTokens)
	}
}
