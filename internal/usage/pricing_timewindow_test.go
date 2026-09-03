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
			if entry.InputCost == nil || !costsNearlyEqual(*entry.InputCost, tc.wantInput) {
				t.Fatalf("InputCost = %v, want %v", entry.InputCost, tc.wantInput)
			}
			if entry.TotalCost == nil || !costsNearlyEqual(*entry.TotalCost, tc.wantTotal) {
				t.Fatalf("TotalCost = %v, want %v", entry.TotalCost, tc.wantTotal)
			}
			if entry.CostSource != CostSourceModelPricing || entry.CostsCalculationCaveat != "" {
				t.Fatalf("source %q caveat %q", entry.CostSource, entry.CostsCalculationCaveat)
			}
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
	if entry.RewriteCostSaved == nil || !costsNearlyEqual(*entry.RewriteCostSaved, 0.22) {
		t.Fatalf("RewriteCostSaved = %v, want off-peak 0.22", entry.RewriteCostSaved)
	}
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
	if update := recalculateEntryCosts(peak, resolver); update.TotalCost == nil || !costsNearlyEqual(*update.TotalCost, 1.76) {
		t.Fatalf("peak TotalCost = %v, want 1.76", update.TotalCost)
	}

	offPeak := base
	offPeak.Timestamp = mondayOffPeakUTC
	if update := recalculateEntryCosts(offPeak, resolver); update.TotalCost == nil || !costsNearlyEqual(*update.TotalCost, 0.88) {
		t.Fatalf("off-peak TotalCost = %v, want 0.88", update.TotalCost)
	}

	// A row whose timestamp could not be read is priced at the base rates so
	// that it is never understated.
	if update := recalculateEntryCosts(base, resolver); update.TotalCost == nil || !costsNearlyEqual(*update.TotalCost, 1.76) {
		t.Fatalf("zero-timestamp TotalCost = %v, want base 1.76", update.TotalCost)
	}
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
	if err := store.WriteBatch(context.Background(), entries); err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}
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
	if err != nil {
		t.Fatalf("RecalculatePricing() error = %v", err)
	}
	if want := int64(len(timeWindowRecalculationEntries)); result.Recalculated != want || result.WithPricing != want {
		t.Fatalf("result = %+v, want %d recalculated rows with pricing", result, want)
	}
	for _, row := range timeWindowRecalculationEntries {
		if got := readTotal(row.ID); !costsNearlyEqual(got, row.WantTotal) {
			t.Fatalf("%s (%s) total_cost = %v, want %v", row.ID, row.At.Format(time.RFC3339), got, row.WantTotal)
		}
	}
}

func TestSQLiteStoreRecalculatePricingAppliesTimeWindowsFromStoredTimestamps(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}

	writeTimeWindowRecalculationEntries(t, store)
	assertTimeWindowRecalculation(t, store, func(id string) float64 {
		var total float64
		if err := db.QueryRowContext(context.Background(), "SELECT total_cost FROM usage WHERE id = ?", id).Scan(&total); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return total
	})
}

func TestPostgreSQLStoreRecalculatePricingAppliesTimeWindowsFromStoredTimestamps(t *testing.T) {
	pool := sqlxtest.NewPostgresPool(t)
	if pool == nil {
		return // skipped: no test server configured
	}

	store, err := NewPostgreSQLStore(pool, 0)
	if err != nil {
		t.Fatalf("NewPostgreSQLStore() error = %v", err)
	}

	writeTimeWindowRecalculationEntries(t, store)
	assertTimeWindowRecalculation(t, store, func(id string) float64 {
		var total float64
		if err := pool.QueryRow(context.Background(), "SELECT total_cost FROM usage WHERE id = $1::uuid", id).Scan(&total); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
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
	if err != nil {
		t.Fatalf("mongo.Connect: %v", err)
	}
	db := client.Database("gomodel_usage_test_" + time.Now().UTC().Format("20060102150405_000000000"))
	t.Cleanup(func() {
		_ = db.Drop(ctx)
		_ = client.Disconnect(ctx)
	})

	store, err := NewMongoDBStore(db, 0)
	if err != nil {
		t.Fatalf("NewMongoDBStore() error = %v", err)
	}

	writeTimeWindowRecalculationEntries(t, store)
	assertTimeWindowRecalculation(t, store, func(id string) float64 {
		var doc struct {
			TotalCost float64 `bson:"total_cost"`
		}
		if err := db.Collection("usage").FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return doc.TotalCost
	})
}
