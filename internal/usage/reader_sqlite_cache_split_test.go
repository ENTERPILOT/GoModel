package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestSQLiteReaderSummary_AggregatesProviderCacheSplit verifies that GetSummary's
// uncached/cached/cache-write fields equal the sum of per-row EntryInputSegments
// over a mixed-provider fixture, and that local-cache rows (cache_type set) are
// excluded by the default uncached cache mode. The fixture deliberately puts each
// row's cache-read in a different raw_data field so the aggregate cannot be faked
// by summing each field separately and taking the max of the sums.
func TestSQLiteReaderSummary_AggregatesProviderCacheSplit(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

	ts := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	// Provider (uncached-mode) rows — these feed both GetSummary and the oracle.
	providerEntries := []*UsageEntry{
		{
			ID: "openai-subset", RequestID: "r1", ProviderID: "p1", Timestamp: ts,
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			InputTokens: 120, OutputTokens: 30, TotalTokens: 150,
			RawData: map[string]any{"prompt_cached_tokens": 80},
		},
		{
			ID: "anthropic-split", RequestID: "r2", ProviderID: "p2", Timestamp: ts,
			Model: "claude-sonnet-4-6", Provider: "anthropic", Endpoint: "/v1/messages",
			InputTokens: 50, OutputTokens: 20, TotalTokens: 70,
			RawData: map[string]any{"cache_read_input_tokens": 90, "cache_creation_input_tokens": 30},
		},
		{
			ID: "anthropic-nofields", RequestID: "r3", ProviderID: "p3", Timestamp: ts,
			Model: "claude-sonnet-4-6", Provider: "anthropic", Endpoint: "/v1/messages",
			InputTokens: 50, OutputTokens: 20, TotalTokens: 70,
		},
		{
			ID: "gemini-subset", RequestID: "r4", ProviderID: "p4", Timestamp: ts,
			Model: "gemini-2.5-pro", Provider: "gemini", Endpoint: "/v1/chat/completions",
			InputTokens: 200, OutputTokens: 40, TotalTokens: 240,
			RawData: map[string]any{"cached_tokens": 120},
		},
		{
			ID: "groq-generic", RequestID: "r5", ProviderID: "p5", Timestamp: ts,
			Model: "llama-3.3", Provider: "groq", Endpoint: "/v1/chat/completions",
			InputTokens: 100, OutputTokens: 10, TotalTokens: 110,
			RawData: map[string]any{"cached_tokens": 10},
		},
	}

	// Local-cache hit — seeded but must be excluded from the uncached-mode summary.
	localHit := &UsageEntry{
		ID: "local-hit", RequestID: "r6", ProviderID: "p6", Timestamp: ts,
		Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
		CacheType: CacheTypeExact, InputTokens: 999, OutputTokens: 999, TotalTokens: 1998,
		RawData: map[string]any{"prompt_cached_tokens": 500},
	}

	ctx := context.Background()
	if err := store.WriteBatch(ctx, append(append([]*UsageEntry{}, providerEntries...), localHit)); err != nil {
		t.Fatalf("failed to seed usage entries: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	summary, err := reader.GetSummary(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC),
		TimeZone:  "UTC",
	})
	if err != nil {
		t.Fatalf("GetSummary returned error: %v", err)
	}

	// Local-cache row excluded by the default uncached mode.
	if summary.TotalRequests != len(providerEntries) {
		t.Fatalf("TotalRequests = %d, want %d (local-cache row must be excluded)", summary.TotalRequests, len(providerEntries))
	}

	// Oracle: sum EntryInputSegments over the same provider rows independently.
	var wantUncached, wantCached, wantWrite int64
	for _, e := range providerEntries {
		u, c, w := EntryInputSegments(UsageLogEntry{InputTokens: e.InputTokens, Provider: e.Provider, RawData: e.RawData})
		wantUncached += u
		wantCached += c
		wantWrite += w
	}

	if summary.UncachedInputTokens != wantUncached {
		t.Fatalf("UncachedInputTokens = %d, want %d", summary.UncachedInputTokens, wantUncached)
	}
	if summary.CachedInputTokens != wantCached {
		t.Fatalf("CachedInputTokens = %d, want %d", summary.CachedInputTokens, wantCached)
	}
	if summary.CacheWriteInputTokens != wantWrite {
		t.Fatalf("CacheWriteInputTokens = %d, want %d", summary.CacheWriteInputTokens, wantWrite)
	}

	// Explicit magic numbers guard against a regression that still happens to be
	// self-consistent with a broken oracle. cached = 80+90+0+120+10 = 300 can only
	// be reached by per-row max-coalescing, not max(sum-per-field).
	if summary.CachedInputTokens != 300 {
		t.Fatalf("CachedInputTokens = %d, want 300", summary.CachedInputTokens)
	}
	if summary.CacheWriteInputTokens != 30 {
		t.Fatalf("CacheWriteInputTokens = %d, want 30", summary.CacheWriteInputTokens)
	}
	if summary.UncachedInputTokens != 310 {
		t.Fatalf("UncachedInputTokens = %d, want 310", summary.UncachedInputTokens)
	}
}
