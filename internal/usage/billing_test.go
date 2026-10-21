package usage

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBilledCost(t *testing.T) {
	tests := []struct {
		name          string
		totalCost     *float64
		markupPercent *float64
		cacheHit      bool
		billCacheHits bool
		want          *float64
	}{
		{name: "unknown cost", markupPercent: new(20.0)},
		{name: "no markup", totalCost: new(1.5), want: new(1.5)},
		{name: "markup", totalCost: new(2.0), markupPercent: new(15.0), want: new(2.3)},
		{name: "zero markup", totalCost: new(2.0), markupPercent: new(0.0), want: new(2.0)},
		{name: "billed cache hit", totalCost: new(2.0), markupPercent: new(50.0), cacheHit: true, billCacheHits: true, want: new(3.0)},
		{name: "unbilled cache hit", totalCost: new(2.0), markupPercent: new(50.0), cacheHit: true, want: new(0.0)},
		{name: "unbilled cache hit with unknown cost", cacheHit: true, want: new(0.0)},
		{name: "billed cache hit with unknown cost", cacheHit: true, billCacheHits: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BilledCost(tt.totalCost, tt.markupPercent, tt.cacheHit, tt.billCacheHits)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.InDelta(t, *tt.want, *got, 1e-12)
		})
	}
}

func newBillingTestSQLiteStore(t *testing.T) (*SQLiteStore, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)
	return store, db
}

func billingTestEntry(id, cacheType string, markupPercent *float64) *UsageEntry {
	cost := 99.0
	return &UsageEntry{
		ID:            id,
		RequestID:     "req-" + id,
		ProviderID:    "provider-" + id,
		Timestamp:     time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC),
		Model:         "gpt-4o",
		Provider:      "openai",
		Endpoint:      "/v1/chat/completions",
		CacheType:     cacheType,
		InputTokens:   1_000_000,
		TotalTokens:   1_000_000,
		InputCost:     &cost,
		TotalCost:     &cost,
		MarkupPercent: markupPercent,
		BilledCost:    &cost,
	}
}

func TestSQLiteStoreWritesBillingColumns(t *testing.T) {
	store, db := newBillingTestSQLiteStore(t)
	entry := billingTestEntry("usage-billed", "", new(20.0))
	entry.MarkupSelector = "openai/"
	entry.BilledCost = new(118.8)

	require.NoError(t, store.WriteBatch(context.Background(), []*UsageEntry{entry, billingTestEntry("usage-plain", "", nil)}))

	var percent, billed float64
	var selector string
	err := db.QueryRow(`SELECT markup_percent, markup_selector, billed_cost FROM usage WHERE id = 'usage-billed'`).Scan(&percent, &selector, &billed)
	require.NoError(t, err)
	assert.Equal(t, 20.0, percent)
	assert.Equal(t, "openai/", selector)
	assert.Equal(t, 118.8, billed)

	var plainPercent sql.NullFloat64
	err = db.QueryRow(`SELECT markup_percent FROM usage WHERE id = 'usage-plain'`).Scan(&plainPercent)
	require.NoError(t, err)
	assert.False(t, plainPercent.Valid)
}

// Fixed UUIDs: PostgreSQL stores usage ids as uuid.
const (
	billingMarkedID    = "00000000-0000-4000-8000-000000000001"
	billingUnmarkedID  = "00000000-0000-4000-8000-000000000002"
	billingCacheHitID  = "00000000-0000-4000-8000-000000000003"
	billingSemanticID  = "00000000-0000-4000-8000-000000000004"
	billingLegacyHitID = "00000000-0000-4000-8000-000000000005"
)

type billingRecalculationStore interface {
	WriteBatch(ctx context.Context, entries []*UsageEntry) error
	PricingRecalculator
}

// assertBilledCostRecalculation checks that recalculation reprices billed cost
// with each row's stored markup and bills cache hits per the switch.
func assertBilledCostRecalculation(t *testing.T, store billingRecalculationStore, billCacheHits bool, readCosts func(id string) (total, billed float64)) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{
		billingTestEntry(billingMarkedID, "", new(20.0)),
		billingTestEntry(billingUnmarkedID, "", nil),
		billingTestEntry(billingCacheHitID, CacheTypeExact, new(20.0)),
		billingTestEntry(billingSemanticID, CacheTypeSemantic, new(20.0)),
		billingTestEntry(billingLegacyHitID, CacheTypeExact, nil),
	}))

	inputRate := 2.0
	_, err := store.RecalculatePricing(ctx, RecalculatePricingParams{BillCacheHits: billCacheHits},
		staticTestPricingResolver{"openai/gpt-4o": {InputPerMtok: &inputRate}})
	require.NoError(t, err)

	hit := func(billed float64) float64 {
		if billCacheHits {
			return billed
		}
		return 0
	}
	want := map[string]float64{
		billingMarkedID:    2.4, // stored 20% markup
		billingUnmarkedID:  2.0,
		billingCacheHitID:  hit(2.4),
		billingSemanticID:  hit(2.4),
		billingLegacyHitID: hit(2.0),
	}
	for id, wantBilled := range want {
		total, billed := readCosts(id)
		assert.Equal(t, 2.0, total, "%s total_cost", id)
		assert.InDelta(t, wantBilled, billed, 1e-12, "%s billed_cost", id)
	}
}

func TestRecalculatePricingRecomputesBilledCost(t *testing.T) {
	for _, billCacheHits := range []bool{true, false} {
		t.Run(fmt.Sprintf("sqlite/bill_cache_hits=%t", billCacheHits), func(t *testing.T) {
			store, db := newBillingTestSQLiteStore(t)
			assertBilledCostRecalculation(t, store, billCacheHits, func(id string) (total, billed float64) {
				err := db.QueryRow(`SELECT total_cost, billed_cost FROM usage WHERE id = ?`, id).Scan(&total, &billed)
				require.NoError(t, err, id)
				return total, billed
			})
		})

		t.Run(fmt.Sprintf("postgresql/bill_cache_hits=%t", billCacheHits), func(t *testing.T) {
			pool := sqlxtest.NewPostgresPool(t)
			if pool == nil {
				return // skipped: no test server configured
			}
			store, err := NewPostgreSQLStore(pool, 0)
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			assertBilledCostRecalculation(t, store, billCacheHits, func(id string) (total, billed float64) {
				err := pool.QueryRow(context.Background(), `SELECT total_cost, billed_cost FROM usage WHERE id = $1::uuid`, id).Scan(&total, &billed)
				require.NoError(t, err, id)
				return total, billed
			})
		})

		t.Run(fmt.Sprintf("mongodb/bill_cache_hits=%t", billCacheHits), func(t *testing.T) {
			mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
				store, _ := newMongoUsageStore(t, db, 0)
				assertBilledCostRecalculation(t, store, billCacheHits, func(id string) (total, billed float64) {
					var doc struct {
						TotalCost  float64 `bson:"total_cost"`
						BilledCost float64 `bson:"billed_cost"`
					}
					err := db.Collection("usage").FindOne(context.Background(), bson.D{{Key: "_id", Value: id}}).Decode(&doc)
					require.NoError(t, err, id)
					return doc.TotalCost, doc.BilledCost
				})
			})
		})
	}
}

func TestRecalculateEntryCostsClearsBilledCostWithoutPricing(t *testing.T) {
	update := recalculateEntryCosts(recalculationEntry{
		ID:            "usage-unpriced",
		Model:         "unknown-model",
		Provider:      "openai",
		InputTokens:   10,
		MarkupPercent: new(20.0),
	}, staticTestPricingResolver{"openai/gpt-4o": &core.ModelPricing{}})

	assert.Nil(t, update.TotalCost)
	assert.Nil(t, update.BilledCost)
}
