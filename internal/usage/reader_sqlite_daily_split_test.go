package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Verifies GetDailyUsage folds the per-period provider prompt-cache split from
// raw_data and aligns it with the grouped daily rows. Local-cache rows
// (cache_type set) are excluded by the default uncached cache mode, so they must
// not affect the split.
func TestSQLiteReaderGetDailyUsage_FoldsPromptCacheSplitPerPeriod(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

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
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	daily, err := reader.GetDailyUsage(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
		Interval:  "daily",
	})
	require.NoError(t, err)

	by := map[string]DailyUsage{}
	for _, d := range daily {
		by[d.Date] = d
	}

	d1 := by["2026-01-15"]
	// Provider rows only: input column sum = 150; split = uncached 120 (70+50) + cached 30.
	assert.Equal(t, int64(150), d1.InputTokens)
	assert.Equal(t, int64(120), d1.UncachedInputTokens)
	assert.Equal(t, int64(30), d1.CachedInputTokens)

	d2 := by["2026-01-16"]
	assert.Equal(t, int64(150), d2.CachedInputTokens)
	assert.Equal(t, int64(50), d2.UncachedInputTokens)
}

// Under a non-default cache mode (all), local-cache rows are included in the
// period totals but must NOT pollute the provider prompt-cache split.
func TestSQLiteReaderGetDailyUsage_SplitExcludesLocalCacheUnderAllMode(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	day := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID: "p1", RequestID: "r1", Timestamp: day, Model: "gpt-5", Provider: "openai",
			Endpoint: "/v1/chat/completions", InputTokens: 100, OutputTokens: 40, TotalTokens: 140,
			RawData: map[string]any{"cached_tokens": 30}, // uncached 70 + cached 30
		},
		{
			ID: "c1", RequestID: "r2", Timestamp: day, Model: "gpt-5", Provider: "openai",
			Endpoint: "/v1/chat/completions", CacheType: CacheTypeExact, InputTokens: 12, OutputTokens: 8, TotalTokens: 20,
		},
	})
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	daily, err := reader.GetDailyUsage(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Interval:  "daily",
		CacheMode: CacheModeAll,
	})
	require.NoError(t, err)
	require.Len(t, daily, 1)

	d := daily[0]
	assert.Equal(t, int64(112), d.InputTokens)
	assert.Equal(t, int64(70), d.UncachedInputTokens)
	assert.Equal(t, int64(30), d.CachedInputTokens)
	assert.Equal(t, int64(0), d.CacheWriteInputTokens)
}
