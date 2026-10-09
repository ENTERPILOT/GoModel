package usage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// sqlReaderFixture spans the Europe/Warsaw switch to summer time at
// 2026-03-29 01:00 UTC, so the per-day grouping has to apply a different UTC
// offset on each side of it. IDs are UUIDs because PostgreSQL stores them in a
// UUID column.
var sqlReaderFixture = []*UsageEntry{
	{
		ID: "6f1c3a52-0000-4000-8000-000000000001", RequestID: "req-1", ProviderID: "chatcmpl-1",
		Timestamp: time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC),
		Model:     "gpt-5", Provider: "openai", ProviderName: "primary", Endpoint: "/v1/chat/completions",
		UserPath: "/team/a", SessionID: "s1", Labels: []string{"alpha", "prod"},
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
		InputCost: new(0.1), OutputCost: new(0.2), TotalCost: new(0.3),
		RawData: map[string]any{"cached_tokens": 10},
	},
	{
		// 00:30 on March 29 in Warsaw, still on winter time (+01:00).
		ID: "6f1c3a52-0000-4000-8000-000000000002", RequestID: "req-2", ProviderID: "chatcmpl-2",
		Timestamp: time.Date(2026, 3, 28, 23, 30, 0, 0, time.UTC),
		Model:     "gpt-5", Provider: "openai", ProviderName: "primary", Endpoint: "/v1/chat/completions",
		UserPath: "/team/a", SessionID: "s1", Labels: []string{"alpha"},
		InputTokens: 200, OutputTokens: 100, TotalTokens: 300,
		InputCost: new(0.2), OutputCost: new(0.4), TotalCost: new(0.6),
		RewriteTokensSaved: 40, RewriteCostSaved: new(0.05),
	},
	{
		// 00:30 on March 30 in Warsaw, now on summer time (+02:00). A fixed
		// +01:00 offset would put it on March 29.
		ID: "6f1c3a52-0000-4000-8000-000000000003", RequestID: "req-3", ProviderID: "msg-3",
		Timestamp: time.Date(2026, 3, 29, 22, 30, 0, 0, time.UTC),
		Model:     "claude-sonnet", Provider: "anthropic", Endpoint: "/v1/chat/completions",
		UserPath: "/team/b", SessionID: "s2",
		InputTokens: 300, OutputTokens: 150, TotalTokens: 450,
	},
	{
		// A local cache hit: excluded from the default (uncached) aggregates.
		ID: "6f1c3a52-0000-4000-8000-000000000004", RequestID: "req-4", ProviderID: "chatcmpl-4",
		Timestamp: time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC),
		Model:     "gpt-5", Provider: "openai", ProviderName: "primary", Endpoint: "/v1/chat/completions",
		UserPath: "/team/a", CacheType: CacheTypeExact,
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150, TotalCost: new(0.3),
	},
}

var sqlReaderFixtureRange = UsageQueryParams{
	StartDate: time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC),
	EndDate:   time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
}

func newSQLReaderFixture(t *testing.T, db sqlx.DB) (*SQLStore, *SQLReader) {
	t.Helper()
	ctx := context.Background()
	store, err := NewSQLStore(ctx, db, 0)
	require.NoError(t, err)
	require.NoError(t, store.WriteBatch(ctx, sqlReaderFixture))
	reader, err := NewSQLReader(db)
	require.NoError(t, err)
	return store, reader
}

func TestSQLReaderAggregates(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		_, reader := newSQLReaderFixture(t, db)

		summary, err := reader.GetSummary(ctx, sqlReaderFixtureRange)
		require.NoError(t, err)
		assert.Equal(t, 3, summary.TotalRequests)
		assert.Equal(t, int64(600), summary.TotalInput)
		assert.Equal(t, int64(300), summary.TotalOutput)
		assert.Equal(t, int64(900), summary.TotalTokens)
		require.NotNil(t, summary.TotalCost)
		assert.InDelta(t, 0.9, *summary.TotalCost, 1e-9)
		assert.Equal(t, int64(40), summary.RewriteTokensSaved)

		byModel, err := reader.GetUsageByModel(ctx, sqlReaderFixtureRange)
		require.NoError(t, err)
		models := map[string]ModelUsage{}
		for _, m := range byModel {
			models[m.Model+"|"+m.ProviderName] = m
		}
		assert.Len(t, models, 2)
		assert.Equal(t, int64(300), models["gpt-5|primary"].InputTokens)
		assert.Equal(t, int64(300), models["claude-sonnet|anthropic"].InputTokens)

		byUserPath, err := reader.GetUsageByUserPath(ctx, sqlReaderFixtureRange)
		require.NoError(t, err)
		paths := map[string]int64{}
		for _, u := range byUserPath {
			paths[u.UserPath] = u.InputTokens
		}
		assert.Equal(t, map[string]int64{"/team/a": 300, "/team/b": 300}, paths)

		byLabel, err := reader.GetUsageByLabel(ctx, sqlReaderFixtureRange)
		require.NoError(t, err)
		require.Len(t, byLabel, 2)
		assert.Equal(t, "alpha", byLabel[0].Label)
		assert.Equal(t, 2, byLabel[0].Requests)
		assert.Equal(t, "prod", byLabel[1].Label)
		assert.Equal(t, 1, byLabel[1].Requests)

		filtered := sqlReaderFixtureRange
		filtered.Label = "prod"
		labelled, err := reader.GetSummary(ctx, filtered)
		require.NoError(t, err)
		assert.Equal(t, 1, labelled.TotalRequests)

		filtered = sqlReaderFixtureRange
		filtered.UserPath = "/team"
		subtree, err := reader.GetSummary(ctx, filtered)
		require.NoError(t, err)
		assert.Equal(t, 3, subtree.TotalRequests)

		cache, err := reader.GetCacheOverview(ctx, sqlReaderFixtureRange)
		require.NoError(t, err)
		assert.Equal(t, 1, cache.Summary.TotalHits)
		assert.Equal(t, 1, cache.Summary.ExactHits)
		require.Len(t, cache.Daily, 1)
		assert.Equal(t, "2026-03-29", cache.Daily[0].Date)
	})
}

func TestSQLReaderSessionsAndLog(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		_, reader := newSQLReaderFixture(t, db)

		sessions, err := reader.GetUsageBySession(ctx, SessionUsageParams{UsageQueryParams: sqlReaderFixtureRange})
		require.NoError(t, err)
		assert.Equal(t, 2, sessions.Total)
		require.Len(t, sessions.Entries, 2)
		assert.Equal(t, "s2", sessions.Entries[0].SessionID, "latest session first")
		assert.Equal(t, "s1", sessions.Entries[1].SessionID)
		assert.Equal(t, 2, sessions.Entries[1].Requests)

		page, err := reader.GetUsageLog(ctx, UsageLogParams{UsageQueryParams: sqlReaderFixtureRange, Limit: 2, Offset: 1})
		require.NoError(t, err)
		assert.Equal(t, 3, page.Total)
		require.Len(t, page.Entries, 2)
		assert.Equal(t, "req-2", page.Entries[0].RequestID)
		assert.Equal(t, "req-1", page.Entries[1].RequestID)

		first := page.Entries[1]
		assert.True(t, first.Timestamp.Equal(sqlReaderFixture[0].Timestamp), "timestamp %v", first.Timestamp)
		assert.Equal(t, []string{"alpha", "prod"}, first.Labels)
		assert.Equal(t, "primary", first.ProviderName)
		assert.Equal(t, "/team/a", first.UserPath)
		assert.EqualValues(t, 10, first.RawData["cached_tokens"])
		require.NotNil(t, page.Entries[0].RewriteCostSaved)
		assert.InDelta(t, 0.05, *page.Entries[0].RewriteCostSaved, 1e-9)

		// Search ignores case on both engines.
		search, err := reader.GetUsageLog(ctx, UsageLogParams{UsageQueryParams: sqlReaderFixtureRange, Search: "CLAUDE"})
		require.NoError(t, err)
		require.Len(t, search.Entries, 1)
		assert.Equal(t, "req-3", search.Entries[0].RequestID)

		byRequest, err := reader.GetUsageByRequestIDs(ctx, []string{"req-1", "req-4", "missing"})
		require.NoError(t, err)
		assert.Len(t, byRequest, 2)
		require.Len(t, byRequest["req-4"], 1)
		assert.Equal(t, CacheTypeExact, byRequest["req-4"][0].CacheType)
	})
}

func TestSQLReaderGroupsPeriodsAcrossDaylightSavingChange(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		_, reader := newSQLReaderFixture(t, db)

		dates := func(params UsageQueryParams) map[string]int64 {
			t.Helper()
			daily, err := reader.GetDailyUsage(ctx, params)
			require.NoError(t, err)
			out := map[string]int64{}
			for _, d := range daily {
				out[d.Date] = d.InputTokens
			}
			return out
		}

		assert.Equal(t, map[string]int64{"2026-03-28": 300, "2026-03-29": 300}, dates(sqlReaderFixtureRange))

		warsaw := sqlReaderFixtureRange
		warsaw.TimeZone = "Europe/Warsaw"
		assert.Equal(t, map[string]int64{"2026-03-28": 100, "2026-03-29": 200, "2026-03-30": 300}, dates(warsaw))

		// Without a closed range SQLite derives the split points from the data.
		warsaw.StartDate, warsaw.EndDate = time.Time{}, time.Time{}
		assert.Equal(t, map[string]int64{"2026-03-28": 100, "2026-03-29": 200, "2026-03-30": 300}, dates(warsaw))

		warsaw = sqlReaderFixtureRange
		warsaw.TimeZone = "Europe/Warsaw"
		warsaw.Interval = "weekly"
		assert.Equal(t, map[string]int64{"2026-W13": 300, "2026-W14": 300}, dates(warsaw))

		monthly := sqlReaderFixtureRange
		monthly.Interval = "monthly"
		assert.Equal(t, map[string]int64{"2026-03": 600}, dates(monthly))
	})
}

func TestSQLReaderTokenThroughputMatchesGoBucketing(t *testing.T) {
	gran, err := ParseThroughputGranularity("hour")
	require.NoError(t, err)
	end := time.Date(2026, 3, 29, 23, 0, 0, 0, time.UTC)
	const offset = 2 * 60 * 60

	// The reference folds the fixture with the Go bucketing MongoDB uses.
	want := newThroughputAccumulator(gran, end, offset)
	bucketSeconds, first, upper := throughputWindow(gran, end, offset)
	for _, e := range sqlReaderFixture {
		if ts := e.Timestamp.Unix(); ts >= first && ts < upper {
			want.add(throughputBucketStart(ts, bucketSeconds, offset), e.CacheType, e.Provider, e.InputTokens, e.OutputTokens, e.TotalTokens, e.RawData)
		}
	}

	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		_, reader := newSQLReaderFixture(t, db)
		got, err := reader.GetTokenThroughput(context.Background(), gran, end, offset)
		require.NoError(t, err)
		assert.Equal(t, want.result(gran), got)
	})
}

func TestSQLStoreWriteBatch(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, reader := newSQLReaderFixture(t, db)

		// Rewriting stored ids is a no-op rather than an error.
		require.NoError(t, store.WriteBatch(ctx, sqlReaderFixture))

		// A batch larger than one statement's bind limit spans several chunks.
		rows := store.dialect.maxBindParameters/usageInsertColumnCount + 5
		batch := make([]*UsageEntry, rows)
		for i := range batch {
			batch[i] = &UsageEntry{
				ID:        fmt.Sprintf("6f1c3a52-0000-4000-9000-%012d", i),
				RequestID: "bulk", ProviderID: "bulk", Timestamp: time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC),
				Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", InputTokens: 1, TotalTokens: 1,
			}
		}
		require.NoError(t, store.WriteBatch(ctx, batch))

		summary, err := reader.GetSummary(ctx, sqlReaderFixtureRange)
		require.NoError(t, err)
		assert.Equal(t, 3+rows, summary.TotalRequests)
	})
}

func TestSQLStoreCleanupDeletesExpiredRows(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, reader := newSQLReaderFixture(t, db)
		recent := &UsageEntry{
			ID: "6f1c3a52-0000-4000-8000-000000000005", RequestID: "req-5", ProviderID: "chatcmpl-5",
			Timestamp: time.Now().Add(-time.Hour), Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
		}
		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{recent}))

		store.retentionDays = 30
		store.cleanup()

		log, err := reader.GetUsageLog(ctx, UsageLogParams{CacheMode: CacheModeAll})
		require.NoError(t, err)
		require.Len(t, log.Entries, 1)
		assert.Equal(t, "req-5", log.Entries[0].RequestID)
	})
}

// A batch size below the row count makes the recalculation page by id, which
// on PostgreSQL compares string arguments against the UUID primary key.
func TestSQLStoreRecalculatePricingPagesByID(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, _ := newSQLReaderFixture(t, db)
		store.recalculationBatchSize = 1

		result, err := store.RecalculatePricing(ctx, RecalculatePricingParams{UsageQueryParams: sqlReaderFixtureRange},
			staticTestPricingResolver{"primary/gpt-5": &core.ModelPricing{InputPerMtok: new(1.0), OutputPerMtok: new(2.0)}})
		require.NoError(t, err)
		assert.Equal(t, int64(len(sqlReaderFixture)), result.Matched)
		assert.Equal(t, int64(3), result.WithPricing, "the three gpt-5 rows")

		var total float64
		require.NoError(t, db.QueryRow(ctx, "SELECT total_cost FROM usage WHERE id = ?", sqlReaderFixture[1].ID).Scan(&total))
		assert.InDelta(t, 0.0004, total, 1e-12, "200 input and 100 output tokens at $1/$2 per Mtok")
	})
}
