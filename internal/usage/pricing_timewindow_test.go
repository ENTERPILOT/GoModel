package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/core"
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

	ctx := context.Background()
	stale := 99.0
	entry := func(id string, at time.Time) *UsageEntry {
		return &UsageEntry{
			ID:           id,
			RequestID:    "req-" + id,
			Timestamp:    at,
			Model:        "deepseek-v4-flash",
			Provider:     "deepseek",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  1_000_000,
			OutputTokens: 1_000_000,
			TotalTokens:  2_000_000,
			TotalCost:    &stale,
		}
	}
	if err := store.WriteBatch(ctx, []*UsageEntry{
		entry("peak", mondayPeakUTC),
		entry("off-peak", mondayOffPeakUTC),
		entry("weekend", saturdayUTC),
	}); err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}

	result, err := store.RecalculatePricing(ctx, RecalculatePricingParams{
		StartDate: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
	}, staticTestPricingResolver{"deepseek/deepseek-v4-flash": deepSeekOffPeakPricing()})
	if err != nil {
		t.Fatalf("RecalculatePricing() error = %v", err)
	}
	if result.Recalculated != 3 || result.WithPricing != 3 {
		t.Fatalf("result = %+v, want 3 recalculated rows with pricing", result)
	}

	want := map[string]float64{"peak": 1.76, "off-peak": 0.88, "weekend": 0.88}
	for id, wantTotal := range want {
		var total float64
		if err := db.QueryRowContext(ctx, "SELECT total_cost FROM usage WHERE id = ?", id).Scan(&total); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if !costsNearlyEqual(total, wantTotal) {
			t.Fatalf("%s total_cost = %v, want %v", id, total, wantTotal)
		}
	}
}
