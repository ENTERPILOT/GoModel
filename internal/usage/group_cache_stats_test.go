package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func seedGroupCacheStatsFixture(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	t.Cleanup(func() { db.Close() })

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

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
	require.NoError(t, err)

	return db, ctx
}

func TestSQLiteGetUsageByModelIncludesGroupCacheStats(t *testing.T) {
	db, ctx := seedGroupCacheStatsFixture(t)
	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	got, err := reader.GetUsageByModel(ctx, UsageQueryParams{})
	require.NoError(t, err)
	require.Len(t, got, 1)

	row := got[0]
	require.Equal(t, int64(140), row.InputTokens)
	require.Equal(t, int64(60), row.CachedInputTokens)
	require.Equal(t, int64(80), row.UncachedInputTokens)
	require.Equal(t, int64(100), row.LocalCachedInputTokens)
	require.Equal(t, int64(20), row.LocalCachedOutputTokens)

	// The fixture's cached row completed on Tuesday 2026-04-07 at 10:00 UTC.
	key := CachedPricingKey{Model: "gpt-5", Provider: "openai", Timed: true, Weekday: time.Tuesday, Minute: 10 * 60}
	require.Equal(t, int64(60), row.CachedTokensByPricing[key], "expected 60 cached tokens under %+v, got %#v", key, row.CachedTokensByPricing)
}

func TestSQLiteGetUsageByUserPathAndLabelIncludeGroupCacheStats(t *testing.T) {
	db, ctx := seedGroupCacheStatsFixture(t)
	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	paths, err := reader.GetUsageByUserPath(ctx, UsageQueryParams{})
	require.NoError(t, err)
	require.Len(t, paths, 1)
	require.Equal(t, "/team/alpha", paths[0].UserPath)
	require.Equal(t, int64(60), paths[0].CachedInputTokens)
	require.Equal(t, int64(100), paths[0].LocalCachedInputTokens)
	require.Equal(t, int64(20), paths[0].LocalCachedOutputTokens, "unexpected user path cache stats: %+v", paths[0].GroupCacheFields)

	labels, err := reader.GetUsageByLabel(ctx, UsageQueryParams{})
	require.NoError(t, err)

	byLabel := map[string]LabelUsage{}
	for _, l := range labels {
		byLabel[l.Label] = l
	}
	prod, ok := byLabel["prod"]
	require.True(t, ok, "expected a prod label row, got %#v", labels)
	require.Equal(t, int64(60), prod.CachedInputTokens)
	require.Equal(t, int64(100), prod.LocalCachedInputTokens)
	require.Equal(t, int64(20), prod.LocalCachedOutputTokens, "unexpected prod label cache stats: %+v", prod.GroupCacheFields)

	batch, ok := byLabel["batch"]
	require.True(t, ok, "expected a batch label row, got %#v", labels)
	require.Equal(t, int64(0), batch.CachedInputTokens)
	require.Equal(t, int64(0), batch.LocalCachedInputTokens)
	require.Equal(t, int64(0), batch.LocalCachedOutputTokens, "unexpected batch label cache stats: %+v", batch.GroupCacheFields)
}

func TestSQLiteGroupCacheStatsFollowFilters(t *testing.T) {
	db, ctx := seedGroupCacheStatsFixture(t)
	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	got, err := reader.GetUsageByModel(ctx, UsageQueryParams{Label: "batch"})
	require.NoError(t, err)
	require.Len(t, got, 1)

	// Only the plain entry carries the batch label: no cached or local tokens.
	require.Equal(t, int64(0), got[0].CachedInputTokens)
	require.Equal(t, int64(0), got[0].LocalCachedInputTokens)
	require.Equal(t, int64(0), got[0].LocalCachedOutputTokens, "expected no cache stats under batch filter, got %+v", got[0].GroupCacheFields)
}

func TestSQLiteLocalOnlyGroupsMaterializeRows(t *testing.T) {
	db, ctx := seedGroupCacheStatsFixture(t)
	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	// A model served exclusively from the local cache in the period: the
	// uncached aggregate pass never produces a row for it.
	err = store.WriteBatch(ctx, []*UsageEntry{{
		ID: "usage-local-only", RequestID: "req-4", ProviderID: "p-1",
		Timestamp: time.Date(2026, 4, 7, 11, 0, 0, 0, time.UTC),
		Model:     "cache-only-model", Provider: "openai", Endpoint: "/v1/chat/completions",
		UserPath: "/team/cached", Labels: []string{"cache-only"}, CacheType: CacheTypeSemantic,
		InputTokens: 50, OutputTokens: 5,
	}})
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	models, err := reader.GetUsageByModel(ctx, UsageQueryParams{})
	require.NoError(t, err)

	var localOnly *ModelUsage
	for i := range models {
		if models[i].Model == "cache-only-model" {
			localOnly = &models[i]
		}
	}
	require.NotNil(t, localOnly, "expected a synthetic row for the local-only model, got %#v", models)
	require.Equal(t, int64(0), localOnly.InputTokens)
	require.Equal(t, int64(0), localOnly.OutputTokens, "synthetic row must carry no provider tokens: %+v", localOnly)
	require.Equal(t, int64(50), localOnly.LocalCachedInputTokens)
	require.Equal(t, int64(5), localOnly.LocalCachedOutputTokens, "unexpected local tokens on synthetic row: %+v", localOnly.GroupCacheFields)
	require.Equal(t, "openai", localOnly.ProviderName)

	labels, err := reader.GetUsageByLabel(ctx, UsageQueryParams{})
	require.NoError(t, err)

	for _, l := range labels {
		if l.Label == "cache-only" {
			require.Equal(t, 1, l.Requests)
			require.Equal(t, int64(50), l.LocalCachedInputTokens, "unexpected synthetic label row: %+v", l)
			return
		}
	}
	t.Fatalf("expected a synthetic cache-only label row, got %#v", labels)
}

func TestSQLiteCacheStatsSkippedOutsideUncachedMode(t *testing.T) {
	db, ctx := seedGroupCacheStatsFixture(t)
	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	for _, mode := range []string{CacheModeAll, CacheModeCached} {
		got, err := reader.GetUsageByModel(ctx, UsageQueryParams{CacheMode: mode})
		require.NoError(t, err)

		for _, row := range got {
			require.Equal(t, int64(0), row.CachedInputTokens)
			require.Equal(t, int64(0), row.LocalCachedInputTokens)
			require.Equal(t, int64(0), row.LocalCachedOutputTokens, "cache fields must stay zero in %s mode (local tokens are already inside the sums): %+v", mode, row.GroupCacheFields)
		}
	}
}

// Err completes the inputSegmentRows interface for the shared pgx-style
// fixture; the fake never fails mid-iteration.
func (f *fakePgxRows) Err() error { return nil }

// TestFoldUsageCacheRowsScansNullableColumns drives the SQL-backend scan
// path through the same pgx-style row interface the PostgreSQL reader uses,
// covering nil provider_name/user_path/labels/cache_type/raw_data columns,
// label expansion, and the local-vs-provider row split.
func TestFoldUsageCacheRowsScansNullableColumns(t *testing.T) {
	str := func(s string) *string { return &s }
	// 2026-08-24 is a Monday; PostgreSQL streams the timestamp as time.Time,
	// SQLite as stored text.
	monday := time.Date(2026, 8, 24, 12, 30, 0, 0, time.UTC)
	rows := &fakePgxRows{rows: [][]any{
		// model, provider, provider_name, user_path, labels, cache_type, input, output, raw_data, timestamp
		{"gpt-5", "openai", str(" primary "), str("/team/alpha"), str(`["prod","batch"]`), nil, 100, 20, str(`{"prompt_cached_tokens": 60}`), monday},
		{"gpt-5", "openai", str(" primary "), str("/team/alpha"), nil, str(CacheTypeExact), 100, 20, nil, "2026-08-24T12:31:00Z"},
		{"gpt-5", "openai", str(" primary "), str("/team/alpha"), nil, nil, 50, 20, str(`{"prompt_cached_tokens": 10}`), "2026-08-29T08:00:00Z"},
		{"gpt-5", "openai", str(" primary "), str("/team/alpha"), nil, nil, 30, 20, str(`{"prompt_cached_tokens": 5}`), "not a timestamp"},
		{"gpt-4o", "openai", nil, nil, nil, nil, 40, 10, nil, nil},
	}}

	stats, err := foldUsageCacheRows(rows, modelGroupKeys)
	require.NoError(t, err)

	gpt5 := stats[usageModelGroupKey("gpt-5", "openai", " primary ")]
	require.NotNil(t, gpt5, "expected gpt-5 stats, got %#v", stats)
	require.Equal(t, int64(75), gpt5.CachedInputTokens)
	require.Equal(t, int64(105), gpt5.UncachedInputTokens, "unexpected gpt-5 split: %+v", gpt5)
	require.Equal(t, int64(100), gpt5.LocalCachedInputTokens)
	require.Equal(t, int64(20), gpt5.LocalCachedOutputTokens)
	require.Equal(t, 1, gpt5.LocalRequests, "unexpected gpt-5 local stats: %+v", gpt5)
	require.Equal(t, "primary", gpt5.ProviderName)

	mondayKey := CachedPricingKey{Model: "gpt-5", Provider: "openai", ProviderName: "primary", Timed: true, Weekday: time.Monday, Minute: 12*60 + 30}
	saturdayKey := CachedPricingKey{Model: "gpt-5", Provider: "openai", ProviderName: "primary", Timed: true, Weekday: time.Saturday, Minute: 8 * 60}
	untimedKey := CachedPricingKey{Model: "gpt-5", Provider: "openai", ProviderName: "primary"}
	require.Equal(t, int64(60), gpt5.CachedTokensByPricing[mondayKey])
	require.Equal(t, int64(10), gpt5.CachedTokensByPricing[saturdayKey])
	require.Equal(t, int64(5), gpt5.CachedTokensByPricing[untimedKey])
	require.Len(t, gpt5.CachedTokensByPricing, 3)

	gpt4o := stats[usageModelGroupKey("gpt-4o", "openai", "")]
	require.NotNil(t, gpt4o)
	require.Equal(t, int64(40), gpt4o.UncachedInputTokens)
	require.Equal(t, int64(0), gpt4o.CachedInputTokens)

	// The same rows folded per label: only the labelled row contributes.
	labelRows := &fakePgxRows{rows: [][]any{
		{"gpt-5", "openai", nil, nil, str(`["prod","batch"]`), nil, 100, 20, str(`{"prompt_cached_tokens": 60}`), nil},
	}}
	labelStats, err := foldUsageCacheRows(labelRows, labelGroupKeys)
	require.NoError(t, err)
	require.NotNil(t, labelStats["prod"])
	require.NotNil(t, labelStats["batch"], "expected stats under both labels, got %#v", labelStats)
	require.Equal(t, int64(60), labelStats["prod"].CachedInputTokens)
	require.Equal(t, int64(60), labelStats["batch"].CachedInputTokens, "expected the row's cached tokens under each label, got %#v", labelStats)
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
			if tt.want == nil {
				require.Nil(t, got)
			} else {
				require.NotNil(t, got)
				require.Equal(t, *tt.want, *got)
			}
		})
	}
}

func TestEstimateCachedInputCostAppliesTimeWindows(t *testing.T) {
	peak, offPeak := 0.014, 0.007
	resolver := mapPricingResolver{
		"deepseek-v4-flash/deepseek": {
			CachedInputPerMtok: &peak,
			TimeWindows: []core.ModelPricingTimeWindow{{
				Label: "off_peak",
				UTCRanges: []core.ModelPricingUTCRange{
					{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "10:00", End: "24:00"},
					{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"},
				},
				Pricing: core.ModelPricingTimeWindowRates{CachedInputPerMtok: &offPeak},
			}},
		},
	}
	key := func(day time.Weekday, hour int) CachedPricingKey {
		return CachedPricingKey{Model: "deepseek-v4-flash", Provider: "deepseek", Timed: true, Weekday: day, Minute: hour * 60}
	}
	cost := EstimateCachedInputCost(map[CachedPricingKey]int64{
		key(time.Monday, 8):   1_000_000, // peak: 0.014
		key(time.Monday, 12):  1_000_000, // off-peak: 0.007
		key(time.Saturday, 8): 1_000_000, // weekend: 0.007
		key(time.Sunday, 0):   1_000_000, // Sunday midnight is a real weekend slot: 0.007
		{Model: "deepseek-v4-flash", Provider: "deepseek"}: 1_000_000, // untimed: base 0.014
	}, resolver)
	require.NotNil(t, cost)
	require.True(t, costsNearlyEqual(*cost, 0.014+0.007+0.007+0.007+0.014))
}

func TestEstimateCachedInputCostResolvesMinuteLevelWindowBoundaries(t *testing.T) {
	base, discounted := 0.02, 0.01
	resolver := mapPricingResolver{
		"m/p": {
			CachedInputPerMtok: &base,
			TimeWindows: []core.ModelPricingTimeWindow{{
				Label:     "half-hour",
				UTCRanges: []core.ModelPricingUTCRange{{Start: "10:30", End: "11:00"}},
				Pricing:   core.ModelPricingTimeWindowRates{CachedInputPerMtok: &discounted},
			}},
		},
	}
	// Rows on opposite sides of a mid-hour boundary must not share a rate.
	cost := EstimateCachedInputCost(map[CachedPricingKey]int64{
		{Model: "m", Provider: "p", Timed: true, Weekday: time.Monday, Minute: 10*60 + 29}: 1_000_000,
		{Model: "m", Provider: "p", Timed: true, Weekday: time.Monday, Minute: 10*60 + 30}: 1_000_000,
	}, resolver)
	require.NotNil(t, cost)
	require.True(t, costsNearlyEqual(*cost, 0.02+0.01))
}

func TestEstimateCachedInputCostPrefersProviderName(t *testing.T) {
	rate := 1.0
	resolver := mapPricingResolver{
		"gpt-5/my-azure": {CachedInputPerMtok: &rate},
	}
	cost := EstimateCachedInputCost(map[CachedPricingKey]int64{
		{Model: "gpt-5", Provider: "azure", ProviderName: "my-azure"}: 1_000_000,
	}, resolver)
	require.NotNil(t, cost)
	require.Equal(t, 1.0, *cost)
}
