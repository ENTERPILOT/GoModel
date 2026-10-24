package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

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
	err = store.WriteBatch(ctx, append(append([]*UsageEntry{}, providerEntries...), localHit))
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	summary, err := reader.GetSummary(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC),
		TimeZone:  "UTC",
	})
	require.NoError(t, err)

	// Local-cache row excluded by the default uncached mode.
	require.Equal(t, len(providerEntries), summary.TotalRequests)

	// Oracle: sum EntryInputSegments over the same provider rows independently.
	var wantUncached, wantCached, wantWrite int64
	for _, e := range providerEntries {
		u, c, w := EntryInputSegments(UsageLogEntry{InputTokens: e.InputTokens, Provider: e.Provider, RawData: e.RawData})
		wantUncached += u
		wantCached += c
		wantWrite += w
	}

	require.Equal(t, wantUncached, summary.UncachedInputTokens)
	require.Equal(t, wantCached, summary.CachedInputTokens)
	require.Equal(t, wantWrite, summary.CacheWriteInputTokens)

	// Explicit magic numbers guard against a regression that still happens to be
	// self-consistent with a broken oracle. cached = 80+90+0+120+10 = 300 can only
	// be reached by per-row max-coalescing, not max(sum-per-field).
	require.Equal(t, int64(300), summary.CachedInputTokens)
	require.Equal(t, int64(30), summary.CacheWriteInputTokens)
	require.Equal(t, int64(310), summary.UncachedInputTokens)
}
