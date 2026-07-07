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
			if summary == nil {
				t.Fatal("expected non-nil summary")
			}
			if summary.RewriteTokensSaved != tc.wantTokens {
				t.Fatalf("RewriteTokensSaved = %d, want %d", summary.RewriteTokensSaved, tc.wantTokens)
			}
			switch {
			case tc.wantCost == nil:
				if summary.RewriteCostSaved != nil {
					t.Fatalf("RewriteCostSaved = %v, want nil", *summary.RewriteCostSaved)
				}
			case summary.RewriteCostSaved == nil:
				t.Fatalf("RewriteCostSaved = nil, want %v", *tc.wantCost)
			case *summary.RewriteCostSaved != *tc.wantCost:
				t.Fatalf("RewriteCostSaved = %v, want %v", *summary.RewriteCostSaved, *tc.wantCost)
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

func TestSummarizeRequestUsage_AnthropicSplitCacheAccountingWithoutCacheFields(t *testing.T) {
	summary := SummarizeRequestUsage([]UsageLogEntry{
		{
			Provider:     "anthropic",
			InputTokens:  50,
			OutputTokens: 20,
		},
	})
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if summary.InputTokens != 50 {
		t.Fatalf("InputTokens = %d, want 50", summary.InputTokens)
	}
	if summary.UncachedInputTokens != 50 {
		t.Fatalf("UncachedInputTokens = %d, want 50", summary.UncachedInputTokens)
	}
	if summary.CachedInputTokens != 0 {
		t.Fatalf("CachedInputTokens = %d, want 0", summary.CachedInputTokens)
	}
	if summary.CacheWriteInputTokens != 0 {
		t.Fatalf("CacheWriteInputTokens = %d, want 0", summary.CacheWriteInputTokens)
	}
	if summary.TotalTokens != 70 {
		t.Fatalf("TotalTokens = %d, want 70", summary.TotalTokens)
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
