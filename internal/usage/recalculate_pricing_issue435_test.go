package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestSQLiteStoreRecalculatePricing_CorrectsStaleCachedCosts validates that the
// admin "recalculate costs" action repairs rows that were mis-costed before the
// issue #435 fix. A xiaomi row whose raw_data carries cached tokens but was
// stored with the full-input-rate cost (and a stale "unmapped token field"
// caveat) must be recomputed with the cached discount and a cleared caveat.
// It also checks that a row whose model no longer resolves to pricing has its
// cost nulled out and is counted as WithoutPricing.
func TestSQLiteStoreRecalculatePricing_CorrectsStaleCachedCosts(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Pin to one connection so every operation shares the same in-memory DB;
	// ":memory:" gives each pooled connection its own private database.
	db.SetMaxOpenConns(1)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	ts := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	staleInput, staleOutput, staleTotal := 0.435, 0.174, 0.609 // pre-fix: cached billed at full input rate

	ctx := context.Background()
	if err := store.WriteBatch(ctx, []*UsageEntry{
		{
			ID: "xiaomi-stale", RequestID: "r1", ProviderID: "p1", Timestamp: ts,
			Model: "mimo-v2.5-pro", Provider: "xiaomi", Endpoint: "/v1/chat/completions",
			InputTokens: 1_000_000, OutputTokens: 200_000, TotalTokens: 1_200_000,
			RawData:                map[string]any{"prompt_cached_tokens": 900_000},
			InputCost:              &staleInput,
			OutputCost:             &staleOutput,
			TotalCost:              &staleTotal,
			CostSource:             CostSourceModelPricing,
			CostsCalculationCaveat: "unmapped token field: prompt_cached_tokens",
		},
		{
			ID: "ghost-nopricing", RequestID: "r2", ProviderID: "p2", Timestamp: ts,
			Model: "retired-model", Provider: "openai", Endpoint: "/v1/chat/completions",
			InputTokens: 1_000_000, OutputTokens: 0, TotalTokens: 1_000_000,
			InputCost: &staleInput, TotalCost: &staleInput, CostSource: CostSourceModelPricing,
		},
	}); err != nil {
		t.Fatalf("write batch: %v", err)
	}

	resolver := staticTestPricingResolver{
		"xiaomi/mimo-v2.5-pro": {
			Currency: "USD", InputPerMtok: floatPtr(0.435), OutputPerMtok: floatPtr(0.87), CachedInputPerMtok: floatPtr(0.0036),
		},
		// "openai/retired-model" intentionally absent -> resolves to nil.
	}

	result, err := store.RecalculatePricing(ctx, RecalculatePricingParams{
		UsageQueryParams: UsageQueryParams{
			StartDate: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC),
			EndDate:   time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC),
			TimeZone:  "UTC",
		},
	}, resolver)
	if err != nil {
		t.Fatalf("RecalculatePricing: %v", err)
	}

	if result.Matched != 2 || result.Recalculated != 2 || result.WithPricing != 1 || result.WithoutPricing != 1 {
		t.Fatalf("result = %+v, want Matched=2 Recalculated=2 WithPricing=1 WithoutPricing=1", result)
	}

	// xiaomi row: cached discount now applied, caveat cleared.
	var in, out, total float64
	var caveat, source string
	if err := db.QueryRow(`SELECT input_cost, output_cost, total_cost, costs_calculation_caveat, cost_source FROM usage WHERE id = 'xiaomi-stale'`).
		Scan(&in, &out, &total, &caveat, &source); err != nil {
		t.Fatalf("query xiaomi row: %v", err)
	}
	// input: 1M*0.435/1M + 900k*(0.0036-0.435)/1M = 0.04674
	assertCostNear(t, "recalc InputCost", &in, 0.04674)
	assertCostNear(t, "recalc OutputCost", &out, 0.174)
	assertCostNear(t, "recalc TotalCost", &total, 0.22074)
	if caveat != "" {
		t.Fatalf("xiaomi caveat = %q, want cleared", caveat)
	}
	if source != CostSourceModelPricing {
		t.Fatalf("xiaomi cost_source = %q, want %q", source, CostSourceModelPricing)
	}

	// ghost row: no current pricing -> cost nulled out.
	var nIn, nOut, nTotal sql.NullFloat64
	if err := db.QueryRow(`SELECT input_cost, output_cost, total_cost FROM usage WHERE id = 'ghost-nopricing'`).
		Scan(&nIn, &nOut, &nTotal); err != nil {
		t.Fatalf("query ghost row: %v", err)
	}
	if nIn.Valid || nOut.Valid || nTotal.Valid {
		t.Fatalf("ghost row costs = in %v out %v total %v, want all NULL", nIn, nOut, nTotal)
	}
}
