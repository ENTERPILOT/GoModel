package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"gomodel/internal/core"
)

// TestDashboardCostAggregation_EndToEnd validates the numbers shown on the
// dashboard / overview page across the full chain: cost extraction (the fixed
// providerMappings default + DeepSeek aliases), SQLite persistence, and the
// reader aggregates that back the overview cards.
//
//   - "Estimated Cost" card  -> GetSummary (default uncached mode): live spend only.
//   - per-model table        -> GetUsageByModel.
//   - "Saved Cost" card      -> GetCacheOverview (cached mode): cache-hit savings.
//
// It confirms cache-hit rows are excluded from live spend (no double count) and
// that cached-token discounts flow through for both standard (xiaomi) and
// non-standard top-level (deepseek) cache reporting.
func TestDashboardCostAggregation_EndToEnd(t *testing.T) {
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

	ts := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	xiaomiPricing := &core.ModelPricing{
		Currency: "USD", InputPerMtok: floatPtr(0.435), OutputPerMtok: floatPtr(0.87), CachedInputPerMtok: floatPtr(0.0036),
	}
	deepseekPricing := &core.ModelPricing{
		Currency: "USD", InputPerMtok: floatPtr(0.21), OutputPerMtok: floatPtr(0.79), CachedInputPerMtok: floatPtr(0.021),
	}

	// Live request 1: xiaomi, standard nested cache reporting (prompt_cached_tokens).
	// input 1M (900k cached) -> 0.04674 ; output 200k -> 0.174 ; total 0.22074
	live1 := ExtractFromSSEUsage("pid1", 1_000_000, 200_000, 1_200_000,
		map[string]any{"prompt_cached_tokens": 900_000},
		"req1", "mimo-v2.5-pro", "xiaomi", "/v1/chat/completions", xiaomiPricing)

	// Live request 2: deepseek, non-standard top-level cache hit/miss fields.
	// input 1M (900k hit) -> 0.0399 ; output 200k -> 0.158 ; total 0.1979
	live2 := ExtractFromSSEUsage("pid2", 1_000_000, 200_000, 1_200_000,
		map[string]any{"prompt_cache_hit_tokens": 900_000, "prompt_cache_miss_tokens": 100_000},
		"req2", "deepseek-v3.1", "deepseek", "/v1/chat/completions", deepseekPricing)

	// Cache hit (served locally): must be excluded from live spend, counted as saved.
	// input 500k (450k cached) -> 0.02337 ; output 100k -> 0.087 ; total 0.11037
	hit := ExtractFromSSEUsage("pid3", 500_000, 100_000, 600_000,
		map[string]any{"prompt_cached_tokens": 450_000},
		"req3", "mimo-v2.5-pro", "xiaomi", "/v1/chat/completions", xiaomiPricing)
	hit.CacheType = CacheTypeExact

	for _, e := range []*UsageEntry{live1, live2, hit} {
		e.Timestamp = ts
	}

	// Sanity: extraction produced the expected per-row costs (the cost.go fix).
	assertCostNear(t, "live1.TotalCost", live1.TotalCost, 0.22074)
	assertCostNear(t, "live2.TotalCost", live2.TotalCost, 0.1979)
	assertCostNear(t, "hit.TotalCost", hit.TotalCost, 0.11037)
	if live2.CostsCalculationCaveat != "" {
		t.Fatalf("deepseek live row carried a caveat: %q", live2.CostsCalculationCaveat)
	}

	ctx := context.Background()
	if err := store.WriteBatch(ctx, []*UsageEntry{live1, live2, hit}); err != nil {
		t.Fatalf("write batch: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}
	params := UsageQueryParams{
		StartDate: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC),
		TimeZone:  "UTC",
	}

	// --- "Estimated Cost" card: live spend only, cache hit excluded ---
	summary, err := reader.GetSummary(ctx, params)
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if summary.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want 2 (cache hit must be excluded)", summary.TotalRequests)
	}
	assertCostNear(t, "summary.TotalInputCost", summary.TotalInputCost, 0.04674+0.0399)
	assertCostNear(t, "summary.TotalOutputCost", summary.TotalOutputCost, 0.174+0.158)
	assertCostNear(t, "summary.TotalCost", summary.TotalCost, 0.22074+0.1979)

	// --- per-model table ---
	byModel, err := reader.GetUsageByModel(ctx, params)
	if err != nil {
		t.Fatalf("GetUsageByModel: %v", err)
	}
	got := make(map[string]*float64)
	for _, m := range byModel {
		got[m.Provider+"/"+m.Model] = m.TotalCost
	}
	if _, ok := got["xiaomi/mimo-v2.5-pro"]; !ok {
		t.Fatalf("missing xiaomi/mimo-v2.5-pro in by-model; got %v", got)
	}
	assertCostNear(t, "byModel xiaomi", got["xiaomi/mimo-v2.5-pro"], 0.22074)
	assertCostNear(t, "byModel deepseek", got["deepseek/deepseek-v3.1"], 0.1979)

	// --- "Saved Cost" card: cache hits only ---
	cacheOverview, err := reader.GetCacheOverview(ctx, params)
	if err != nil {
		t.Fatalf("GetCacheOverview: %v", err)
	}
	if cacheOverview.Summary.TotalHits != 1 {
		t.Fatalf("cache TotalHits = %d, want 1", cacheOverview.Summary.TotalHits)
	}
	assertCostNear(t, "cache TotalSavedCost", cacheOverview.Summary.TotalSavedCost, 0.11037)
}
