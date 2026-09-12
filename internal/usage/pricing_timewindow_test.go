package usage

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deepSeekOffPeakPricing mirrors the ai-model-list entry for deepseek-v4-flash:
// base prices are the peak rates; the off-peak window halves them on weekdays
// outside 01:00-04:00 and 06:00-10:00 UTC and all day on weekends.
func deepSeekOffPeakPricing() *core.ModelPricing {
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	return &core.ModelPricing{
		Currency:           "USD",
		InputPerMtok:       new(0.44),
		OutputPerMtok:      new(1.32),
		CachedInputPerMtok: new(0.014),
		TimeWindows: []core.ModelPricingTimeWindow{{
			Label: "off_peak",
			UTCRanges: []core.ModelPricingUTCRange{
				{Days: weekdays, Start: "00:00", End: "01:00"},
				{Days: weekdays, Start: "04:00", End: "06:00"},
				{Days: weekdays, Start: "10:00", End: "24:00"},
				{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"},
			},
			Pricing: core.ModelPricingTimeWindowRates{
				InputPerMtok:       new(0.22),
				OutputPerMtok:      new(0.66),
				CachedInputPerMtok: new(0.007),
			},
		}},
	}
}

var (
	// 2026-08-24 is a Monday.
	mondayPeakUTC    = time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	mondayOffPeakUTC = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	saturdayUTC      = time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
)

func TestApplyUsageCostsUsesTimeWindowAtEntryTimestamp(t *testing.T) {
	tests := []struct {
		name      string
		at        time.Time
		wantInput float64
		wantTotal float64
	}{
		{"peak hour uses base rates", mondayPeakUTC, 0.44 - 0.5*(0.44-0.014), 0.44 - 0.5*(0.44-0.014) + 1.32},
		{"off-peak hour uses window rates", mondayOffPeakUTC, 0.22 - 0.5*(0.22-0.007), 0.22 - 0.5*(0.22-0.007) + 0.66},
		{"weekend peak hour is off-peak", saturdayUTC, 0.22 - 0.5*(0.22-0.007), 0.22 - 0.5*(0.22-0.007) + 0.66},
		{"Beijing timestamp is evaluated in UTC", mondayPeakUTC.In(time.FixedZone("CST", 8*3600)), 0.44 - 0.5*(0.44-0.014), 0.44 - 0.5*(0.44-0.014) + 1.32},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := &UsageEntry{
				Timestamp:    tc.at,
				Model:        "deepseek-v4-flash",
				Provider:     "deepseek",
				Endpoint:     "/v1/chat/completions",
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
				RawData:      map[string]any{"prompt_cache_hit_tokens": 500_000},
			}
			applyUsageCosts(entry, "deepseek", entry.Endpoint, deepSeekOffPeakPricing())
			require.NotNil(t, entry.InputCost)
			require.True(t, costsNearlyEqual(*entry.InputCost, tc.wantInput), "InputCost = %v, want %v", entry.InputCost, tc.wantInput)
			require.NotNil(t, entry.TotalCost)
			require.True(t, costsNearlyEqual(*entry.TotalCost, tc.wantTotal), "TotalCost = %v, want %v", entry.TotalCost, tc.wantTotal)
			require.Equal(t, CostSourceModelPricing, entry.CostSource)
			require.Empty(t, entry.CostsCalculationCaveat)
		})
	}
}

func TestApplyRewriteSavingsUsesTimeWindowAtEntryTimestamp(t *testing.T) {
	entry := &UsageEntry{
		Timestamp:   mondayOffPeakUTC,
		Provider:    "deepseek",
		Endpoint:    "/v1/chat/completions",
		InputTokens: 1_000_000,
	}
	ApplyRewriteSavings(entry, 1_000_000, deepSeekOffPeakPricing())
	require.NotNil(t, entry.RewriteCostSaved)
	require.True(t, costsNearlyEqual(*entry.RewriteCostSaved, 0.22))
}

func TestRecalculateEntryCostsUsesStoredTimestamp(t *testing.T) {
	resolver := &recordingPricingResolver{pricing: deepSeekOffPeakPricing()}
	base := recalculationEntry{
		ID:           "usage-1",
		Model:        "deepseek-v4-flash",
		Provider:     "deepseek",
		Endpoint:     "/v1/chat/completions",
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
	}

	peak := base
	peak.Timestamp = mondayPeakUTC
	update := recalculateEntryCosts(peak, resolver)
	require.NotNil(t, update.TotalCost)
	require.True(t, costsNearlyEqual(*update.TotalCost, 1.76))

	offPeak := base
	offPeak.Timestamp = mondayOffPeakUTC
	update = recalculateEntryCosts(offPeak, resolver)
	require.NotNil(t, update.TotalCost)
	require.True(t, costsNearlyEqual(*update.TotalCost, 0.88))
	// A row whose timestamp could not be read is priced at the base rates so
	// that it is never understated.
	update = recalculateEntryCosts(base, resolver)
	require.NotNil(t, update.TotalCost)
	require.True(t, costsNearlyEqual(*update.TotalCost, 1.76))
}

// timeWindowRecalculationEntries returns one stale-priced DeepSeek row per
// pricing situation, keyed by the total each should be re-priced to. IDs are
// UUIDs because PostgreSQL stores them in a UUID column.
var timeWindowRecalculationEntries = []struct {
	ID        string
	At        time.Time
	WantTotal float64
}{
	{"0d1b4c0a-1d3b-4c4f-9a1e-000000000001", mondayPeakUTC, 1.76},
	{"0d1b4c0a-1d3b-4c4f-9a1e-000000000002", mondayOffPeakUTC, 0.88},
	{"0d1b4c0a-1d3b-4c4f-9a1e-000000000003", saturdayUTC, 0.88},
}

// timeWindowRecalculationParams spans the week the fixture rows fall in.
var timeWindowRecalculationParams = RecalculatePricingParams{
	StartDate: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
	EndDate:   time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
}

// writeTimeWindowRecalculationEntries stores the fixture rows with a stale
// total so a recalculation that does nothing is caught.
func writeTimeWindowRecalculationEntries(t *testing.T, store UsageStore) {
	t.Helper()
	stale := 99.0
	entries := make([]*UsageEntry, 0, len(timeWindowRecalculationEntries))
	for _, row := range timeWindowRecalculationEntries {
		entries = append(entries, &UsageEntry{
			ID:           row.ID,
			RequestID:    "req-" + row.ID,
			ProviderID:   "deepseek",
			Timestamp:    row.At,
			Model:        "deepseek-v4-flash",
			Provider:     "deepseek",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  1_000_000,
			OutputTokens: 1_000_000,
			TotalTokens:  2_000_000,
			TotalCost:    &stale,
		})
	}
	err := store.WriteBatch(context.Background(), entries)
	require.NoError(t, err)
}

// recalculatingStore is a usage store that can re-price its rows.
type recalculatingStore interface {
	UsageStore
	PricingRecalculator
}

// assertTimeWindowRecalculation runs the recalculation and checks that every
// fixture row was re-priced at the rate in effect at its stored timestamp.
// readTotal returns the persisted total_cost for one row ID.
func assertTimeWindowRecalculation(t *testing.T, store recalculatingStore, readTotal func(id string) float64) {
	t.Helper()
	result, err := store.RecalculatePricing(context.Background(), timeWindowRecalculationParams,
		staticTestPricingResolver{"deepseek/deepseek-v4-flash": deepSeekOffPeakPricing()})
	require.NoError(t, err)
	want := int64(len(timeWindowRecalculationEntries))
	require.Equal(t, want, result.Recalculated)
	require.Equal(t, want, result.WithPricing)

	for _, row := range timeWindowRecalculationEntries {
		got := readTotal(row.ID)
		require.True(t, costsNearlyEqual(got, row.WantTotal), "%s (%s) total_cost = %v, want %v", row.ID, row.At.Format(time.RFC3339), got, row.WantTotal)
	}
}

func TestSQLiteStoreRecalculatePricingAppliesTimeWindowsFromStoredTimestamps(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	writeTimeWindowRecalculationEntries(t, store)
	assertTimeWindowRecalculation(t, store, func(id string) float64 {
		var total float64
		err := db.QueryRowContext(context.Background(), "SELECT total_cost FROM usage WHERE id = ?", id).Scan(&total)
		require.NoError(t, err, "read %s", id)

		return total
	})
}

func TestPostgreSQLStoreRecalculatePricingAppliesTimeWindowsFromStoredTimestamps(t *testing.T) {
	pool := sqlxtest.NewPostgresPool(t)
	if pool == nil {
		return // skipped: no test server configured
	}

	store, err := NewPostgreSQLStore(pool, 0)
	require.NoError(t, err)

	writeTimeWindowRecalculationEntries(t, store)
	assertTimeWindowRecalculation(t, store, func(id string) float64 {
		var total float64
		err := pool.QueryRow(context.Background(), "SELECT total_cost FROM usage WHERE id = $1::uuid", id).Scan(&total)
		require.NoError(t, err, "read %s", id)

		return total
	})
}

func TestMongoDBStoreRecalculatePricingAppliesTimeWindowsFromStoredTimestamps(t *testing.T) {
	dsn := os.Getenv("MONGO_TEST_DSN")
	if dsn == "" {
		t.Skip("MONGO_TEST_DSN is not set")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(dsn))
	require.NoError(t, err)

	db := client.Database("gomodel_usage_test_" + time.Now().UTC().Format("20060102150405_000000000"))
	t.Cleanup(func() {
		_ = db.Drop(ctx)
		_ = client.Disconnect(ctx)
	})

	store, err := NewMongoDBStore(db, 0)
	require.NoError(t, err)

	writeTimeWindowRecalculationEntries(t, store)
	assertTimeWindowRecalculation(t, store, func(id string) float64 {
		var doc struct {
			TotalCost float64 `bson:"total_cost"`
		}
		err := db.Collection("usage").FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc)
		require.NoError(t, err, "read %s", id)

		return doc.TotalCost
	})
}

func TestNewPostgreSQLStoreToleratesConcurrentStartup(t *testing.T) {
	pool := sqlxtest.NewPostgresPool(t)
	if pool == nil {
		return // skipped: no test server configured
	}

	// Several replicas construct the store against one fresh database at
	// once; every DDL statement must serialize instead of failing the
	// losing replica's startup.
	const workers = 8
	errs := make(chan error, workers)
	start := make(chan struct{})
	for range workers {
		go func() {
			<-start
			store, err := NewPostgreSQLStore(pool, 0)
			if err == nil {
				err = store.Close()
			}
			errs <- err
		}()
	}
	close(start)
	for range workers {
		err := <-errs
		assert.NoError(t, err)
	}
}
