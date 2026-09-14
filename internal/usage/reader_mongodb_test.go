package usage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoSessionUsagePipelinesArePagedAndExcludeCachedCost(t *testing.T) {
	dataPipeline, countPipeline, limit, offset, err := mongoSessionUsagePipelines(SessionUsageParams{
		SessionID: "scoped-session", CacheMode: CacheModeUncached,
		Limit:  12,
		Offset: 7,
	})
	require.NoError(t, err)
	require.Equal(t, 12, limit)
	require.Equal(t, 7, offset)
	require.Len(t, dataPipeline, 6)
	require.Len(t, countPipeline, 4)

	match := fmt.Sprint(dataPipeline[0])
	require.Contains(t, match, "scoped-session")
	require.NotContains(t, match, "cache_type")

	group := fmt.Sprint(dataPipeline[2])
	for _, fragment := range []string{"provider_requests", CacheTypeExact, CacheTypeSemantic, "total_cost"} {
		require.Contains(t, group, fragment)
	}
	wantTail := bson.A{
		bson.D{{Key: "$sort", Value: bson.D{{Key: "latest", Value: -1}, {Key: "_id", Value: 1}}}},
		bson.D{{Key: "$skip", Value: 7}},
		bson.D{{Key: "$limit", Value: 12}},
	}
	require.Equal(t, wantTail, dataPipeline[3:])

	wantCount := bson.D{{Key: "$count", Value: "count"}}
	require.Equal(t, wantCount, countPipeline[3])
}

func TestSessionCostPtrUsesZeroForCacheOnlySession(t *testing.T) {
	got := sessionCostPtr(0, 0, 123)
	require.NotNil(t, got)
	require.Equal(t, float64(0), *got)
	got = sessionCostPtr(1, 0, 0)
	require.Nil(t, got)
}

func TestMongoUsageLogMatchFiltersAndSearchWithCacheMode(t *testing.T) {
	got, err := mongoUsageLogMatchFilters(UsageLogParams{
		CacheMode: CacheModeUncached,
		Search:    "gpt",
	})
	require.NoError(t, err)

	regex := bson.D{{Key: "$regex", Value: "gpt"}, {Key: "$options", Value: "i"}}
	want := bson.D{{Key: "$and", Value: bson.A{
		bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "cache_type", Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "cache_type", Value: nil}},
			bson.D{{Key: "cache_type", Value: ""}},
		}}},
		bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "model", Value: regex}},
			bson.D{{Key: "provider", Value: regex}},
			bson.D{{Key: "provider_name", Value: regex}},
			bson.D{{Key: "request_id", Value: regex}},
			bson.D{{Key: "provider_id", Value: regex}},
			bson.D{{Key: "session_id", Value: regex}},
		}}},
	}}}

	require.Equal(t, want, got)
}

func TestMongoUsageLogMatchFiltersLabel(t *testing.T) {
	got, err := mongoUsageLogMatchFilters(UsageLogParams{
		CacheMode: CacheModeAll,
		Label:     "team-alpha",
	})
	require.NoError(t, err)

	want := bson.D{{Key: "labels", Value: "team-alpha"}}
	require.Equal(t, want, got)
}

func TestMongoUsageMatchFiltersDataFilters(t *testing.T) {
	got, err := mongoUsageMatchFilters(UsageQueryParams{
		CacheMode: CacheModeAll,
		Model:     "gpt-5",
		Provider:  "openai",
		Label:     "team-alpha",
	})
	require.NoError(t, err)

	// The provider clause matches provider or provider_name, so it is ANDed
	// with the scalar filters.
	want := bson.D{{Key: "$and", Value: bson.A{
		bson.D{
			{Key: "model", Value: "gpt-5"},
			{Key: "labels", Value: "team-alpha"},
		},
		bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "provider", Value: "openai"}},
			bson.D{{Key: "provider_name", Value: "openai"}},
		}}},
	}}}

	require.Equal(t, want, got)
}

func TestMongoUsageLogMatchFiltersEscapesSearchRegex(t *testing.T) {
	got, err := mongoUsageLogMatchFilters(UsageLogParams{
		CacheMode: CacheModeAll,
		Search:    "gpt.4+",
	})
	require.NoError(t, err)

	regex := bson.D{{Key: "$regex", Value: `gpt\.4\+`}, {Key: "$options", Value: "i"}}
	want := bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "model", Value: regex}},
		bson.D{{Key: "provider", Value: regex}},
		bson.D{{Key: "provider_name", Value: regex}},
		bson.D{{Key: "request_id", Value: regex}},
		bson.D{{Key: "provider_id", Value: regex}},
		bson.D{{Key: "session_id", Value: regex}},
	}}}

	require.Equal(t, want, got)
}

// Locks the BSON-to-field mapping for the rewrite-savings columns on the
// Mongo decode path: rewrite_tokens_saved and rewrite_cost_saved must reach
// UsageLogEntry, and a document without them must decode to zero/nil.
func TestMongoUsageLogRowDecodesRewriteSavings(t *testing.T) {
	cost := 0.0375
	cases := []struct {
		name       string
		doc        bson.D
		wantTokens int64
		wantCost   *float64
	}{
		{
			name: "with priced savings",
			doc: bson.D{
				{Key: "_id", Value: "with-savings"},
				{Key: "request_id", Value: "req-saved"},
				{Key: "rewrite_tokens_saved", Value: int64(89)},
				{Key: "rewrite_cost_saved", Value: cost},
			},
			wantTokens: 89,
			wantCost:   &cost,
		},
		{
			name: "without savings",
			doc: bson.D{
				{Key: "_id", Value: "without-savings"},
				{Key: "request_id", Value: "req-plain"},
			},
			wantTokens: 0,
			wantCost:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := bson.Marshal(tc.doc)
			require.NoError(t, err)

			var row mongoUsageLogRow
			err = bson.Unmarshal(raw, &row)
			require.NoError(t, err)

			entry := row.toUsageLogEntry()
			assert.Equal(t, tc.wantTokens, entry.RewriteTokensSaved)

			if tc.wantCost == nil {
				assert.Nil(t, entry.RewriteCostSaved)
			} else {
				require.NotNil(t, entry.RewriteCostSaved)
				assert.Equal(t, *tc.wantCost, *entry.RewriteCostSaved)
			}
		})
	}
}

// The user-path fold must select the same row set as the user-path aggregate:
// the raw-field filter is cleared and the subtree match runs against the
// materialized canonical (trimmed, root-normalized) path instead — via the
// same shared stages the aggregate itself uses.
func TestMongoUsageCacheStatsPipelineCanonicalUserPath(t *testing.T) {
	params := UsageQueryParams{UserPath: "/team/alpha", Model: "gpt-5"}

	pipeline, err := mongoUsageCacheStatsPipeline(params, true)
	require.NoError(t, err)
	require.Len(t, pipeline, 4)

	// Stage 1: base filters with the raw user_path condition cleared — the
	// model filter stays, the subtree match moves to the canonical stage.
	wantBase, err := mongoUsageMatchFilters(UsageQueryParams{Model: "gpt-5", CacheMode: CacheModeAll})
	require.NoError(t, err)
	require.Equal(t, bson.D{{Key: "$match", Value: wantBase}}, pipeline[0])

	// Stages 2-3: identical canonical stages to the aggregate's own.
	require.Equal(t, mongoCanonicalUserPathAddFieldsStage(), pipeline[1])
	require.Equal(t, mongoCanonicalUserPathMatchStage("/team/alpha"), pipeline[2])
}

// Model/label folds keep filtering the raw user_path field, matching their
// aggregates; no canonical stages are inserted.
func TestMongoUsageCacheStatsPipelineRawUserPath(t *testing.T) {
	params := UsageQueryParams{UserPath: "/team/alpha"}

	pipeline, err := mongoUsageCacheStatsPipeline(params, false)
	require.NoError(t, err)
	require.Len(t, pipeline, 2)

	wantParams := params
	wantParams.CacheMode = CacheModeAll
	want, err := mongoUsageMatchFilters(wantParams)
	require.NoError(t, err)
	require.Equal(t, bson.D{{Key: "$match", Value: want}}, pipeline[0])
}
