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

func TestSQLiteLocalOnlyGroupsMaterializeRows(t *testing.T) {
	db, ctx := seedGroupCacheStatsFixture(t)
	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	// A model served exclusively from the local cache in the period: the
	// uncached aggregate pass never produces a row for it.
	err = store.WriteBatch(ctx, []*UsageEntry{{
		ID: "usage-local-only", RequestID: "req-4", ProviderID: "p-1",
		Timestamp: time.Date(2026, 4, 7, 11, 0, 0, 0, time.UTC),
		Model:     "cache-only-model", Provider: "openai", Endpoint: "/v1/chat/completions",
		UserPath: "/team/cached", Labels: []string{"cache-only"}, CacheType: CacheTypeSemantic,
		InputTokens: 50, OutputTokens: 5,
	}})
	if err != nil {
		t.Fatalf("failed to seed local-only entry: %v", err)
	}
	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	models, err := reader.GetUsageByModel(ctx, UsageQueryParams{})
	if err != nil {
		t.Fatalf("GetUsageByModel returned error: %v", err)
	}
	var localOnly *ModelUsage
	for i := range models {
		if models[i].Model == "cache-only-model" {
			localOnly = &models[i]
		}
	}
	if localOnly == nil {
		t.Fatalf("expected a synthetic row for the local-only model, got %#v", models)
	}
	if localOnly.InputTokens != 0 || localOnly.OutputTokens != 0 {
		t.Fatalf("synthetic row must carry no provider tokens: %+v", localOnly)
	}
	if localOnly.LocalCachedInputTokens != 50 || localOnly.LocalCachedOutputTokens != 5 {
		t.Fatalf("unexpected local tokens on synthetic row: %+v", localOnly.GroupCacheFields)
	}
	if localOnly.ProviderName != "openai" {
		t.Fatalf("expected grouped provider name, got %q", localOnly.ProviderName)
	}

	labels, err := reader.GetUsageByLabel(ctx, UsageQueryParams{})
	if err != nil {
		t.Fatalf("GetUsageByLabel returned error: %v", err)
	}
	for _, l := range labels {
		if l.Label == "cache-only" {
			if l.Requests != 1 || l.LocalCachedInputTokens != 50 {
				t.Fatalf("unexpected synthetic label row: %+v", l)
			}
			return
		}
	}
	t.Fatalf("expected a synthetic cache-only label row, got %#v", labels)
}

func TestSQLiteCacheStatsSkippedOutsideUncachedMode(t *testing.T) {
	db, ctx := seedGroupCacheStatsFixture(t)
	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	for _, mode := range []string{CacheModeAll, CacheModeCached} {
		got, err := reader.GetUsageByModel(ctx, UsageQueryParams{CacheMode: mode})
		if err != nil {
			t.Fatalf("GetUsageByModel(%s) returned error: %v", mode, err)
		}
		for _, row := range got {
			if row.CachedInputTokens != 0 || row.LocalCachedInputTokens != 0 || row.LocalCachedOutputTokens != 0 {
				t.Fatalf("cache fields must stay zero in %s mode (local tokens are already inside the sums): %+v", mode, row.GroupCacheFields)
			}
		}
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
	want := func(v float64) *float64 { return &v }

	tests := []struct {
		name      string
		byPricing map[CachedPricingKey]int64
		resolver  PricingResolver
		want      *float64
	}{
		{
			name: "prices known models and skips unpriced ones",
			byPricing: map[CachedPricingKey]int64{
				{Model: "gpt-5", Provider: "openai"}:    2_000_000,
				{Model: "unpriced", Provider: "openai"}: 1_000_000,
			},
			resolver: resolver,
			want:     want(1.0),
		},
		{
			name:     "nil for empty breakdown",
			resolver: resolver,
		},
		{
			name: "nil when nothing is priced",
			byPricing: map[CachedPricingKey]int64{
				{Model: "unpriced", Provider: "openai"}: 100,
			},
			resolver: resolver,
		},
		{
			name: "nil without a resolver",
			byPricing: map[CachedPricingKey]int64{
				{Model: "gpt-5", Provider: "openai"}: 100,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateCachedInputCost(tt.byPricing, tt.resolver)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("EstimateCachedInputCost = %v, want %v", got, tt.want)
			}
			if got != nil && *got != *tt.want {
				t.Fatalf("EstimateCachedInputCost = %v, want %v", *got, *tt.want)
			}
		})
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
