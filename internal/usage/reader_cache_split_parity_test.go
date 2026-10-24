package usage

import (
	"context"
	"database/sql"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// cacheSplitFixture mixes every prompt-cache raw_data field with unrelated
// raw_data content, so the readers' raw_data projection must keep exactly the
// fields EntryInputSegments reads.
func cacheSplitFixture(ts time.Time) []*UsageEntry {
	noise := map[string]any{"reasoning_tokens": 7, "details": map[string]any{"cached_tokens": 999}, "blob": strings.Repeat("x", 4096)}
	withNoise := func(fields map[string]any) map[string]any {
		out := map[string]any{}
		maps.Copy(out, noise)
		maps.Copy(out, fields)
		return out
	}
	entry := func(id, provider string, input int, raw map[string]any) *UsageEntry {
		return &UsageEntry{
			ID: uuid.NewString(), RequestID: id, ProviderID: id, Timestamp: ts,
			Model: "m-" + provider, Provider: provider, Endpoint: "/v1/chat/completions",
			InputTokens: input, OutputTokens: 10, TotalTokens: input + 10,
			RawData: raw,
		}
	}
	local := entry("local", "openai", 999, withNoise(map[string]any{"prompt_cached_tokens": 500}))
	local.CacheType = CacheTypeExact
	return []*UsageEntry{
		entry("openai", "openai", 120, withNoise(map[string]any{"prompt_cached_tokens": 80})),
		entry("anthropic", "anthropic", 50, withNoise(map[string]any{"cache_read_input_tokens": 90, "cache_creation_input_tokens": 30})),
		entry("anthropic-bare", "anthropic", 50, nil),
		entry("gemini", "gemini", 200, withNoise(map[string]any{"cached_tokens": 120})),
		entry("bedrock", "bedrock", 100, withNoise(map[string]any{"cache_write_input_tokens": 40, "cached_tokens": 10})),
		entry("noise-only", "groq", 60, withNoise(nil)),
		local,
	}
}

// assertCacheSplitParity checks the summary, daily, by-model and throughput
// folds against the per-row EntryInputSegments oracle.
func assertCacheSplitParity(t *testing.T, store UsageStore, reader UsageReader) {
	t.Helper()
	ctx := context.Background()
	ts := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	entries := cacheSplitFixture(ts)
	require.NoError(t, store.WriteBatch(ctx, entries))

	var wantUncached, wantCached, wantWrite int64
	for _, e := range entries {
		if e.CacheType != "" {
			continue
		}
		u, c, w := EntryInputSegments(UsageLogEntry{InputTokens: e.InputTokens, Provider: e.Provider, RawData: e.RawData})
		wantUncached += u
		wantCached += c
		wantWrite += w
	}
	require.Equal(t, int64(300), wantCached)

	day := time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
	params := UsageQueryParams{StartDate: day, EndDate: day, TimeZone: "UTC", Interval: "daily"}

	summary, err := reader.GetSummary(ctx, params)
	require.NoError(t, err)
	assert.Equal(t, wantUncached, summary.UncachedInputTokens)
	assert.Equal(t, wantCached, summary.CachedInputTokens)
	assert.Equal(t, wantWrite, summary.CacheWriteInputTokens)

	daily, err := reader.GetDailyUsage(ctx, params)
	require.NoError(t, err)
	var dailyCached, dailyWrite int64
	for _, d := range daily {
		dailyCached += d.CachedInputTokens
		dailyWrite += d.CacheWriteInputTokens
	}
	assert.Equal(t, wantCached, dailyCached)
	assert.Equal(t, wantWrite, dailyWrite)

	byModel, err := reader.GetUsageByModel(ctx, params)
	require.NoError(t, err)
	var modelCached, modelWrite int64
	for _, m := range byModel {
		modelCached += m.CachedInputTokens
		modelWrite += m.CacheWriteInputTokens
	}
	assert.Equal(t, wantCached, modelCached)
	assert.Equal(t, wantWrite, modelWrite)

	gran, err := ParseThroughputGranularity("hour")
	require.NoError(t, err)
	throughput, err := reader.GetTokenThroughput(ctx, gran, time.Now().UTC(), 0)
	require.NoError(t, err)
	var promptCached int64
	for _, b := range throughput.Buckets {
		promptCached += b.PromptCachedTokens
	}
	assert.Equal(t, wantCached, promptCached)
}

func TestReaderCacheSplitParity(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		db.SetMaxOpenConns(1)
		store, err := NewSQLiteStore(db, 0)
		require.NoError(t, err)
		reader, err := NewSQLiteReader(db)
		require.NoError(t, err)
		assertCacheSplitParity(t, store, reader)
	})

	t.Run("postgresql", func(t *testing.T) {
		pool := sqlxtest.NewPostgresPool(t)
		if pool == nil {
			return
		}
		store, err := NewPostgreSQLStore(pool, 0)
		require.NoError(t, err)
		reader, err := NewPostgreSQLReader(pool)
		require.NoError(t, err)
		assertCacheSplitParity(t, store, reader)
	})

	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db, 0)
		require.NoError(t, err)
		reader, err := NewMongoDBReader(db)
		require.NoError(t, err)
		assertCacheSplitParity(t, store, reader)
	})
}

func TestPromptCacheRawData(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		name string
		raw  *string
		want map[string]any
	}{
		{"nil", nil, nil},
		{"empty", str(""), nil},
		{"no cache fields", str(`{"reasoning_tokens":3}`), nil},
		{"nested field ignored", str(`{"details":{"cached_tokens":9}}`), nil},
		{"non-numeric dropped", str(`{"cached_tokens":"12","prompt_cached_tokens":null}`), nil},
		{
			"cache fields kept",
			str(`{"cache_read_input_tokens":90,"cache_creation_input_tokens":30,"blob":"x"}`),
			map[string]any{"cache_read_input_tokens": float64(90), "cache_creation_input_tokens": float64(30)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, promptCacheRawData(tt.raw))
		})
	}
}
