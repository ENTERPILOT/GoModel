package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixed UUIDs: PostgreSQL stores usage ids as uuid.
const (
	billedReadProviderID = "00000000-0000-4000-8000-000000000011"
	billedReadHitID      = "00000000-0000-4000-8000-000000000012"
	billedReadHitOnlyID  = "00000000-0000-4000-8000-000000000013"
	billedReadUnpricedID = "00000000-0000-4000-8000-000000000014"
)

func billedReadEntry(id, model, userPath, label, cacheType string, totalCost, billedCost *float64) *UsageEntry {
	entry := &UsageEntry{
		ID:           id,
		RequestID:    "req-" + id,
		ProviderID:   "provider-" + id,
		Timestamp:    time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC),
		Model:        model,
		Provider:     "openai",
		ProviderName: "openai",
		Endpoint:     "/v1/chat/completions",
		UserPath:     userPath,
		Labels:       []string{label},
		CacheType:    cacheType,
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
		TotalCost:    totalCost,
		BilledCost:   billedCost,
	}
	if billedCost != nil && totalCost != nil && *billedCost != *totalCost {
		entry.MarkupPercent = new(20.0)
		entry.MarkupSelector = "openai/"
	}
	return entry
}

// assertBilledCostReads checks that billed totals include billed
// response-cache hits even in uncached mode, whose other costs exclude them.
func assertBilledCostReads(t *testing.T, store interface {
	WriteBatch(ctx context.Context, entries []*UsageEntry) error
}, reader UsageReader) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{
		billedReadEntry(billedReadProviderID, "gpt-4o", "/acme", "team-a", "", new(1.0), new(1.2)),
		billedReadEntry(billedReadHitID, "gpt-4o", "/acme", "team-a", CacheTypeExact, new(1.0), new(1.2)),
		billedReadEntry(billedReadHitOnlyID, "gpt-5", "/beta", "team-b", CacheTypeSemantic, new(0.5), new(0.5)),
		billedReadEntry(billedReadUnpricedID, "gpt-4o", "/acme", "team-a", "", nil, nil),
	}))

	t.Run("summary", func(t *testing.T) {
		for mode, want := range map[string]float64{CacheModeUncached: 2.9, CacheModeCached: 1.7, CacheModeAll: 2.9} {
			summary, err := reader.GetSummary(ctx, UsageQueryParams{CacheMode: mode})
			require.NoError(t, err, mode)
			require.NotNil(t, summary.TotalBilledCost, mode)
			assert.InDelta(t, want, *summary.TotalBilledCost, 1e-9, mode)
		}
		summary, err := reader.GetSummary(ctx, UsageQueryParams{})
		require.NoError(t, err)
		require.NotNil(t, summary.TotalCost)
		assert.InDelta(t, 1.0, *summary.TotalCost, 1e-9, "provider cost still excludes cache hits")
	})

	t.Run("by model", func(t *testing.T) {
		rows, err := reader.GetUsageByModel(ctx, UsageQueryParams{})
		require.NoError(t, err)
		billed := map[string]*float64{}
		for _, row := range rows {
			billed[row.Model] = row.BilledCost
		}
		require.NotNil(t, billed["gpt-4o"])
		assert.InDelta(t, 2.4, *billed["gpt-4o"], 1e-9)
		require.NotNil(t, billed["gpt-5"], "group served only from the cache")
		assert.InDelta(t, 0.5, *billed["gpt-5"], 1e-9)

		rows, err = reader.GetUsageByModel(ctx, UsageQueryParams{CacheMode: CacheModeCached})
		require.NoError(t, err)
		for _, row := range rows {
			if row.Model == "gpt-4o" {
				require.NotNil(t, row.BilledCost)
				assert.InDelta(t, 1.2, *row.BilledCost, 1e-9, "cached mode counts hits once")
			}
		}
	})

	t.Run("by user path", func(t *testing.T) {
		rows, err := reader.GetUsageByUserPath(ctx, UsageQueryParams{})
		require.NoError(t, err)
		billed := map[string]*float64{}
		for _, row := range rows {
			billed[row.UserPath] = row.BilledCost
		}
		require.NotNil(t, billed["/acme"])
		assert.InDelta(t, 2.4, *billed["/acme"], 1e-9)
		require.NotNil(t, billed["/beta"])
		assert.InDelta(t, 0.5, *billed["/beta"], 1e-9)
	})

	t.Run("by label", func(t *testing.T) {
		rows, err := reader.GetUsageByLabel(ctx, UsageQueryParams{})
		require.NoError(t, err)
		billed := map[string]*float64{}
		for _, row := range rows {
			billed[row.Label] = row.BilledCost
		}
		require.NotNil(t, billed["team-a"])
		assert.InDelta(t, 2.4, *billed["team-a"], 1e-9)
		require.NotNil(t, billed["team-b"])
		assert.InDelta(t, 0.5, *billed["team-b"], 1e-9)
	})

	t.Run("log", func(t *testing.T) {
		result, err := reader.GetUsageLog(ctx, UsageLogParams{CacheMode: CacheModeAll, Limit: 10})
		require.NoError(t, err)
		byID := map[string]UsageLogEntry{}
		for _, entry := range result.Entries {
			byID[entry.ID] = entry
		}
		marked := byID[billedReadProviderID]
		require.NotNil(t, marked.BilledCost)
		assert.InDelta(t, 1.2, *marked.BilledCost, 1e-9)
		require.NotNil(t, marked.MarkupPercent)
		assert.Equal(t, 20.0, *marked.MarkupPercent)
		assert.Equal(t, "openai/", marked.MarkupSelector)

		unpriced := byID[billedReadUnpricedID]
		assert.Nil(t, unpriced.BilledCost)
		assert.Nil(t, unpriced.MarkupPercent)
		assert.Empty(t, unpriced.MarkupSelector)

		grouped, err := reader.GetUsageByRequestIDs(ctx, []string{"req-" + billedReadProviderID})
		require.NoError(t, err)
		require.Len(t, grouped["req-"+billedReadProviderID], 1)
		require.NotNil(t, grouped["req-"+billedReadProviderID][0].BilledCost)
		assert.InDelta(t, 1.2, *grouped["req-"+billedReadProviderID][0].BilledCost, 1e-9)
	})
}

func TestReadersReportBilledCost(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		store, err := NewSQLiteStore(db, 0)
		require.NoError(t, err)
		reader, err := NewSQLiteReader(db)
		require.NoError(t, err)
		assertBilledCostReads(t, store, reader)
	})

	t.Run("postgresql", func(t *testing.T) {
		pool := sqlxtest.NewPostgresPool(t)
		if pool == nil {
			return // skipped: no test server configured
		}
		store, err := NewPostgreSQLStore(pool, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		reader, err := NewPostgreSQLReader(pool)
		require.NoError(t, err)
		assertBilledCostReads(t, store, reader)
	})

	t.Run("mongodb", func(t *testing.T) {
		mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
			store, reader := newMongoUsageStore(t, db, 0)
			assertBilledCostReads(t, store, reader)
		})
	})
}

func TestApplyCacheStatsKeepsBilledZeroTokenGroups(t *testing.T) {
	// A cache hit priced per request bills a charge without any tokens.
	billed := func() map[string]*GroupCacheStats {
		return map[string]*GroupCacheStats{"key": {Model: "dall-e-3", Provider: "openai", ProviderName: "openai", UserPath: "/acme", LocalRequests: 1, LocalBilledCost: new(0.04)}}
	}
	unbilled := map[string]*GroupCacheStats{"key": {Model: "dall-e-3", Provider: "openai", UserPath: "/acme", LocalRequests: 1, LocalBilledCost: new(0.0)}}

	models := applyModelCacheStats(nil, billed())
	require.Len(t, models, 1)
	require.NotNil(t, models[0].BilledCost)
	assert.Equal(t, 0.04, *models[0].BilledCost)

	paths := applyUserPathCacheStats(nil, billed())
	require.Len(t, paths, 1)
	require.NotNil(t, paths[0].BilledCost)
	assert.Equal(t, 0.04, *paths[0].BilledCost)

	labels := applyLabelCacheStats(nil, billed())
	require.Len(t, labels, 1)
	require.NotNil(t, labels[0].BilledCost)
	assert.Equal(t, 0.04, *labels[0].BilledCost)

	assert.Empty(t, applyModelCacheStats(nil, unbilled), "unbilled zero-token hits add no row")
}

func TestAddCost(t *testing.T) {
	assert.Nil(t, addCost(nil, nil))
	assert.Equal(t, 1.5, *addCost(new(1.5), nil))
	assert.Equal(t, 2.0, *addCost(nil, new(2.0)))
	assert.Equal(t, 3.5, *addCost(new(1.5), new(2.0)))
}
