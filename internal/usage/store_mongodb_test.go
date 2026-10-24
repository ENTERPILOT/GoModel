package usage

import (
	"context"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
)

// newMongoUsageStore builds the write store and the reader over one database
// so every test reads back what the store persisted through the public
// reader, as the dashboard does.
func newMongoUsageStore(t *testing.T, db *mongo.Database, retentionDays int) (*MongoDBStore, *MongoDBReader) {
	t.Helper()
	store, err := NewMongoDBStore(db, retentionDays)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	reader, err := NewMongoDBReader(db)
	require.NoError(t, err)
	return store, reader
}

func mongoUsageLogByID(t *testing.T, reader *MongoDBReader) map[string]UsageLogEntry {
	t.Helper()
	result, err := reader.GetUsageLog(context.Background(), UsageLogParams{
		CacheMode: CacheModeAll,
	})
	require.NoError(t, err)
	byID := make(map[string]UsageLogEntry, len(result.Entries))
	for _, entry := range result.Entries {
		byID[entry.ID] = entry
	}
	return byID
}

// Mirrors the SQL round-trip tests (labels_sqlite_test.go,
// session_sqlite_test.go): what WriteBatch persists must come back through
// the reader field for field, with the same storage-time normalization.
func TestMongoDBStoreWriteBatchRoundTripsEveryField(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, reader := newMongoUsageStore(t, db, 0)
		ctx := context.Background()
		cost := func(v float64) *float64 { return &v }
		now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

		full := &UsageEntry{
			ID: "full", RequestID: "req-full", ProviderID: "chatcmpl-1", Timestamp: now,
			Model: "gpt-5", Provider: "openai", ProviderName: " primary-openai ", Endpoint: "/v1/chat/completions",
			UserPath: "team/alpha/", SessionID: " session-1 ",
			Labels:      []string{"alpha", "prod"},
			InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
			RawData:                map[string]any{"cached_tokens": 3, "reasoning_tokens": 2},
			InputCost:              cost(0.10),
			OutputCost:             cost(0.05),
			TotalCost:              cost(0.15),
			CostSource:             " " + CostSourceModelPricing + " ",
			CostsCalculationCaveat: "tool tokens unpriced",
			RewriteTokensSaved:     42,
			RewriteCostSaved:       cost(0.0375),
		}
		// Single-entry batch, then a multi-entry batch.
		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{full}))
		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{
			{
				ID: "minimal", RequestID: "req-minimal", ProviderID: "msg-2", Timestamp: now.Add(time.Minute),
				Model: "claude-sonnet", Provider: "anthropic", Endpoint: "/v1/messages",
				CacheType:   "unexpected-cache-type",
				InputTokens: 20, OutputTokens: 8, TotalTokens: 28,
			},
			{
				ID: "cached", RequestID: "req-cached", ProviderID: "cache-3", Timestamp: now.Add(2 * time.Minute),
				Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
				CacheType:   " Semantic ",
				InputTokens: 7, OutputTokens: 3, TotalTokens: 10,
				InputCost: cost(0.07), OutputCost: cost(0.03), TotalCost: cost(0.10),
			},
		}))

		byID := mongoUsageLogByID(t, reader)
		require.Len(t, byID, 3)

		got := byID["full"]
		assert.Equal(t, "req-full", got.RequestID)
		assert.Equal(t, "chatcmpl-1", got.ProviderID)
		assert.True(t, now.Equal(got.Timestamp), "Timestamp = %v, want %v", got.Timestamp, now)
		assert.Equal(t, "gpt-5", got.Model)
		assert.Equal(t, "openai", got.Provider)
		assert.Equal(t, "primary-openai", got.ProviderName, "provider_name is trimmed before storage")
		assert.Equal(t, "/v1/chat/completions", got.Endpoint)
		assert.Equal(t, "/team/alpha", got.UserPath, "user_path is canonicalized before storage")
		assert.Equal(t, "session-1", got.SessionID, "session_id is trimmed before storage")
		assert.Empty(t, got.CacheType, "provider rows carry no cache type")
		assert.Equal(t, []string{"alpha", "prod"}, got.Labels)
		assert.Equal(t, 10, got.InputTokens)
		assert.Equal(t, 5, got.OutputTokens)
		assert.Equal(t, 15, got.TotalTokens)
		require.NotNil(t, got.InputCost)
		require.NotNil(t, got.OutputCost)
		require.NotNil(t, got.TotalCost)
		assert.Equal(t, 0.10, *got.InputCost)
		assert.Equal(t, 0.05, *got.OutputCost)
		assert.Equal(t, 0.15, *got.TotalCost)
		assert.Equal(t, CostSourceModelPricing, got.CostSource, "cost_source is trimmed before storage")
		assert.Equal(t, "tool tokens unpriced", got.CostsCalculationCaveat)
		assert.Equal(t, int64(42), got.RewriteTokensSaved)
		require.NotNil(t, got.RewriteCostSaved)
		assert.Equal(t, 0.0375, *got.RewriteCostSaved)
		rawData, err := json.Marshal(got.RawData)
		require.NoError(t, err)
		assert.JSONEq(t, `{"cached_tokens":3,"reasoning_tokens":2}`, string(rawData))

		minimal := byID["minimal"]
		assert.True(t, now.Add(time.Minute).Equal(minimal.Timestamp))
		assert.Equal(t, "anthropic", minimal.ProviderName, "empty provider_name displays as the provider")
		assert.Equal(t, "/", minimal.UserPath, "empty user_path is stored as the root path")
		assert.Empty(t, minimal.SessionID)
		assert.Empty(t, minimal.CacheType, "unknown cache types are dropped")
		assert.Nil(t, minimal.Labels)
		assert.Nil(t, minimal.RawData)
		assert.Nil(t, minimal.InputCost)
		assert.Nil(t, minimal.OutputCost)
		assert.Nil(t, minimal.TotalCost)
		assert.Empty(t, minimal.CostSource)
		assert.Empty(t, minimal.CostsCalculationCaveat)
		assert.Zero(t, minimal.RewriteTokensSaved)
		assert.Nil(t, minimal.RewriteCostSaved)

		assert.Equal(t, CacheTypeSemantic, byID["cached"].CacheType, "cache_type is normalized before storage")

		// The default (uncached) summary counts provider rows only: the
		// semantic-cache hit is excluded from requests, tokens and cost.
		summary, err := reader.GetSummary(ctx, UsageQueryParams{})
		require.NoError(t, err)
		assert.Equal(t, 2, summary.TotalRequests)
		assert.Equal(t, int64(30), summary.TotalInput)
		assert.Equal(t, int64(13), summary.TotalOutput)
		assert.Equal(t, int64(43), summary.TotalTokens)
		require.NotNil(t, summary.TotalCost)
		assert.InDelta(t, 0.15, *summary.TotalCost, 1e-9)
		assert.Equal(t, int64(42), summary.RewriteTokensSaved)
		require.NotNil(t, summary.RewriteCostSaved)
		assert.InDelta(t, 0.0375, *summary.RewriteCostSaved, 1e-9)
		// The provider prompt-cache split folds raw_data like the SQL readers.
		assert.Equal(t, int64(3), summary.CachedInputTokens)
		assert.Equal(t, int64(27), summary.UncachedInputTokens)

		all, err := reader.GetSummary(ctx, UsageQueryParams{CacheMode: CacheModeAll})
		require.NoError(t, err)
		assert.Equal(t, 3, all.TotalRequests)
		assert.Equal(t, int64(53), all.TotalTokens)
	})
}

func TestMongoDBStoreWriteBatchEmptyAndDuplicateIDs(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, reader := newMongoUsageStore(t, db, 0)
		ctx := context.Background()
		now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

		require.NoError(t, store.WriteBatch(ctx, nil))
		require.Empty(t, mongoUsageLogByID(t, reader))

		entry := func(id string) *UsageEntry {
			return &UsageEntry{
				ID: id, RequestID: "req-" + id, ProviderID: "p-" + id, Timestamp: now,
				Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", TotalTokens: 1,
			}
		}
		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{entry("first")}))

		// A replayed id is reported as a partial write while the rest of the
		// unordered batch still lands. (The SQL stores silently ignore the
		// duplicate instead.)
		err := store.WriteBatch(ctx, []*UsageEntry{entry("first"), entry("second")})
		require.ErrorIs(t, err, ErrPartialWrite)
		var partial *PartialWriteError
		require.ErrorAs(t, err, &partial)
		assert.Equal(t, 2, partial.TotalEntries)
		assert.Equal(t, 1, partial.FailedCount)

		byID := mongoUsageLogByID(t, reader)
		require.Len(t, byID, 2)
		assert.Contains(t, byID, "first")
		assert.Contains(t, byID, "second")
	})
}

// MongoDB enforces retention with a TTL index rather than a cleanup loop, so
// the observable contract is the index: expireAfterSeconds equals the
// retention window, and no TTL is attached when retention is disabled.
func TestMongoDBStoreRetentionConfiguresTTLIndex(t *testing.T) {
	cases := []struct {
		name          string
		retentionDays int
		wantTTL       *int32
	}{
		{name: "retention disabled", retentionDays: 0, wantTTL: nil},
		{name: "retention 7 days", retentionDays: 7, wantTTL: func() *int32 { v := int32(7 * 24 * 60 * 60); return &v }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
				store, _ := newMongoUsageStore(t, db, tc.retentionDays)

				ttl, found := mongoTimestampIndexTTL(t, store.collection)
				require.True(t, found, "timestamp index must exist")
				if tc.wantTTL == nil {
					assert.Nil(t, ttl, "no TTL when retention is disabled")
				} else {
					require.NotNil(t, ttl)
					assert.Equal(t, *tc.wantTTL, *ttl)
				}
			})
		})
	}
}

// mongoTimestampIndexTTL returns the expireAfterSeconds of the index keyed on
// timestamp, or nil when that index carries no TTL.
func mongoTimestampIndexTTL(t *testing.T, coll *mongo.Collection) (*int32, bool) {
	t.Helper()
	cursor, err := coll.Indexes().List(context.Background())
	require.NoError(t, err)
	var indexes []struct {
		Key                bson.D `bson:"key"`
		ExpireAfterSeconds *int32 `bson:"expireAfterSeconds"`
	}
	require.NoError(t, cursor.All(context.Background(), &indexes))
	for _, index := range indexes {
		if len(index.Key) == 1 && index.Key[0].Key == "timestamp" {
			return index.ExpireAfterSeconds, true
		}
	}
	return nil, false
}

func TestMongoDBStoreFlushAndCloseAreIdempotentNoOps(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, reader := newMongoUsageStore(t, db, 30)
		ctx := context.Background()

		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{{
			ID: "before-close", RequestID: "req", ProviderID: "p", Timestamp: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", TotalTokens: 1,
		}}))
		// Writes are synchronous, so Flush has nothing to wait for, and Close
		// leaves the shared client open: the shutdown order Flush, Close,
		// Close must succeed and the data must still be readable.
		require.NoError(t, store.Flush(ctx))
		require.NoError(t, store.Close())
		require.NoError(t, store.Close())
		require.Contains(t, mongoUsageLogByID(t, reader), "before-close")
	})
}
