package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Verifies GetDailyUsage folds the per-period provider prompt-cache split from
// raw_data and aligns it with the grouped daily rows. Local-cache rows
// (cache_type set) are excluded by the default uncached cache mode, so they must
// not affect the split.
func TestSQLiteReaderGetDailyUsage_FoldsPromptCacheSplitPerPeriod(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

	ctx := context.Background()
	day1 := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 16, 10, 0, 0, 0, time.UTC)

	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID: "d1-a", RequestID: "r1", Timestamp: day1, Model: "gpt-5", Provider: "openai",
			Endpoint: "/v1/chat/completions", InputTokens: 100, OutputTokens: 40, TotalTokens: 140,
			RawData: map[string]any{"cached_tokens": 30}, // uncached 70, cached 30
		},
		{
			ID: "d1-b", RequestID: "r2", Timestamp: day1, Model: "gpt-5", Provider: "openai",
			Endpoint: "/v1/chat/completions", InputTokens: 50, OutputTokens: 10, TotalTokens: 60,
			// no cache fields => fully uncached
		},
		{
			ID: "d1-cache", RequestID: "r3", Timestamp: day1, Model: "gpt-5", Provider: "openai",
			Endpoint: "/v1/chat/completions", CacheType: CacheTypeExact, InputTokens: 999, OutputTokens: 999, TotalTokens: 1998,
		},
		{
			ID: "d2-a", RequestID: "r4", Timestamp: day2, Model: "gpt-5", Provider: "openai",
			Endpoint: "/v1/chat/completions", InputTokens: 200, OutputTokens: 20, TotalTokens: 220,
			RawData: map[string]any{"cached_tokens": 150},
		},
	})
	if err != nil {
		t.Fatalf("failed to seed usage entries: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	daily, err := reader.GetDailyUsage(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
		Interval:  "daily",
	})
	if err != nil {
		t.Fatalf("GetDailyUsage returned error: %v", err)
	}

	by := map[string]DailyUsage{}
	for _, d := range daily {
		by[d.Date] = d
	}

	d1 := by["2026-01-15"]
	// Provider rows only: input column sum = 150; split = uncached 120 (70+50) + cached 30.
	if d1.InputTokens != 150 {
		t.Errorf("day1 InputTokens = %d, want 150 (local-cache row excluded)", d1.InputTokens)
	}
	if d1.UncachedInputTokens != 120 {
		t.Errorf("day1 UncachedInputTokens = %d, want 120", d1.UncachedInputTokens)
	}
	if d1.CachedInputTokens != 30 {
		t.Errorf("day1 CachedInputTokens = %d, want 30", d1.CachedInputTokens)
	}

	d2 := by["2026-01-16"]
	if d2.CachedInputTokens != 150 || d2.UncachedInputTokens != 50 {
		t.Errorf("day2 split = {uncached:%d cached:%d}, want {50 150}", d2.UncachedInputTokens, d2.CachedInputTokens)
	}
}
