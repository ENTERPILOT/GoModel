package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

func seedGroupCacheStatsFixture(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

	ctx := context.Background()
	ts := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			// OpenAI-style prompt-cache read inside input_tokens.
			ID: "usage-cached", RequestID: "req-1", ProviderID: "p-1", Timestamp: ts,
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			UserPath: "/team/alpha", Labels: []string{"prod"},
			InputTokens: 100, OutputTokens: 20,
			RawData: map[string]any{"prompt_cached_tokens": 60},
		},
		{
			// No provider cache involvement.
			ID: "usage-plain", RequestID: "req-2", ProviderID: "p-1", Timestamp: ts.Add(time.Minute),
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			UserPath: "/team/alpha", Labels: []string{"prod", "batch"},
			InputTokens: 40, OutputTokens: 10,
		},
		{
			// Served from GoModel's local response cache: excluded from the
			// uncached aggregates but surfaced as LocalCachedTokens.
			ID: "usage-local", RequestID: "req-3", ProviderID: "p-1", Timestamp: ts.Add(2 * time.Minute),
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			UserPath: "/team/alpha", Labels: []string{"prod"}, CacheType: CacheTypeExact,
			InputTokens: 100, OutputTokens: 20,
		},
	})
	if err != nil {
		t.Fatalf("failed to seed usage entries: %v", err)
	}
	return db, ctx
}

func TestSQLiteGetUsageByModelIncludesGroupCacheStats(t *testing.T) {
	db, ctx := seedGroupCacheStatsFixture(t)
	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	got, err := reader.GetUsageByModel(ctx, UsageQueryParams{})
	if err != nil {
		t.Fatalf("GetUsageByModel returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 grouped usage row, got %d: %#v", len(got), got)
	}
	row := got[0]
	if row.InputTokens != 140 {
		t.Fatalf("expected 140 input tokens (local hit excluded), got %d", row.InputTokens)
	}
	if row.CachedInputTokens != 60 {
		t.Fatalf("expected 60 cached input tokens, got %d", row.CachedInputTokens)
	}
	if row.UncachedInputTokens != 80 {
		t.Fatalf("expected 80 uncached input tokens, got %d", row.UncachedInputTokens)
	}
	if row.LocalCachedInputTokens != 100 || row.LocalCachedOutputTokens != 20 {
		t.Fatalf("expected 100/20 local cached tokens, got %d/%d", row.LocalCachedInputTokens, row.LocalCachedOutputTokens)
	}
	key := CachedPricingKey{Model: "gpt-5", Provider: "openai"}
	if row.CachedTokensByPricing[key] != 60 {
		t.Fatalf("expected 60 cached tokens under %+v, got %#v", key, row.CachedTokensByPricing)
	}
}

func TestSQLiteGetUsageByUserPathAndLabelIncludeGroupCacheStats(t *testing.T) {
	db, ctx := seedGroupCacheStatsFixture(t)
	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	paths, err := reader.GetUsageByUserPath(ctx, UsageQueryParams{})
	if err != nil {
		t.Fatalf("GetUsageByUserPath returned error: %v", err)
	}
	if len(paths) != 1 || paths[0].UserPath != "/team/alpha" {
		t.Fatalf("expected one /team/alpha row, got %#v", paths)
	}
	if paths[0].CachedInputTokens != 60 || paths[0].LocalCachedInputTokens != 100 || paths[0].LocalCachedOutputTokens != 20 {
		t.Fatalf("unexpected user path cache stats: %+v", paths[0].GroupCacheFields)
	}

	labels, err := reader.GetUsageByLabel(ctx, UsageQueryParams{})
	if err != nil {
		t.Fatalf("GetUsageByLabel returned error: %v", err)
	}
	byLabel := map[string]LabelUsage{}
	for _, l := range labels {
		byLabel[l.Label] = l
	}
	prod, ok := byLabel["prod"]
	if !ok {
		t.Fatalf("expected a prod label row, got %#v", labels)
	}
	if prod.CachedInputTokens != 60 || prod.LocalCachedInputTokens != 100 || prod.LocalCachedOutputTokens != 20 {
		t.Fatalf("unexpected prod label cache stats: %+v", prod.GroupCacheFields)
	}
	batch, ok := byLabel["batch"]
	if !ok {
		t.Fatalf("expected a batch label row, got %#v", labels)
	}
	if batch.CachedInputTokens != 0 || batch.LocalCachedInputTokens != 0 || batch.LocalCachedOutputTokens != 0 {
		t.Fatalf("unexpected batch label cache stats: %+v", batch.GroupCacheFields)
	}
}

func TestSQLiteGroupCacheStatsFollowFilters(t *testing.T) {
	db, ctx := seedGroupCacheStatsFixture(t)
	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	got, err := reader.GetUsageByModel(ctx, UsageQueryParams{Label: "batch"})
	if err != nil {
		t.Fatalf("GetUsageByModel returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 grouped usage row, got %d", len(got))
	}
	// Only the plain entry carries the batch label: no cached or local tokens.
	if got[0].CachedInputTokens != 0 || got[0].LocalCachedInputTokens != 0 || got[0].LocalCachedOutputTokens != 0 {
		t.Fatalf("expected no cache stats under batch filter, got %+v", got[0].GroupCacheFields)
	}
}

type mapPricingResolver map[string]*core.ModelPricing

func (r mapPricingResolver) ResolvePricing(model, providerType string) *core.ModelPricing {
	return r[model+"/"+providerType]
}

func TestEstimateCachedInputCost(t *testing.T) {
	rate := 0.5 // $ per Mtok
	resolver := mapPricingResolver{
		"gpt-5/openai": {CachedInputPerMtok: &rate},
	}

	cost := EstimateCachedInputCost(map[CachedPricingKey]int64{
		{Model: "gpt-5", Provider: "openai"}:    2_000_000,
		{Model: "unpriced", Provider: "openai"}: 1_000_000,
	}, resolver)
	if cost == nil {
		t.Fatalf("expected an estimated cost, got nil")
	}
	if *cost != 1.0 {
		t.Fatalf("expected $1.00 estimate, got %v", *cost)
	}

	if got := EstimateCachedInputCost(nil, resolver); got != nil {
		t.Fatalf("expected nil for empty breakdown, got %v", *got)
	}
	if got := EstimateCachedInputCost(map[CachedPricingKey]int64{
		{Model: "unpriced", Provider: "openai"}: 100,
	}, resolver); got != nil {
		t.Fatalf("expected nil when nothing is priced, got %v", *got)
	}
	if got := EstimateCachedInputCost(map[CachedPricingKey]int64{
		{Model: "gpt-5", Provider: "openai"}: 100,
	}, nil); got != nil {
		t.Fatalf("expected nil without a resolver, got %v", *got)
	}
}

func TestEstimateCachedInputCostPrefersProviderName(t *testing.T) {
	rate := 1.0
	resolver := mapPricingResolver{
		"gpt-5/my-azure": {CachedInputPerMtok: &rate},
	}
	cost := EstimateCachedInputCost(map[CachedPricingKey]int64{
		{Model: "gpt-5", Provider: "azure", ProviderName: "my-azure"}: 1_000_000,
	}, resolver)
	if cost == nil || *cost != 1.0 {
		t.Fatalf("expected provider-name pricing to apply, got %v", cost)
	}
}
