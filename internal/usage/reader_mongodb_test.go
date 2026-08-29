package usage

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoSessionUsagePipelinesArePagedAndExcludeCachedCost(t *testing.T) {
	dataPipeline, countPipeline, limit, offset, err := mongoSessionUsagePipelines(SessionUsageParams{
		SessionID: "scoped-session", CacheMode: CacheModeUncached,
		Limit:  12,
		Offset: 7,
	})
	if err != nil {
		t.Fatalf("mongoSessionUsagePipelines: %v", err)
	}
	if limit != 12 || offset != 7 {
		t.Fatalf("pagination = %d/%d, want 12/7", limit, offset)
	}
	if len(dataPipeline) != 6 {
		t.Fatalf("data pipeline stages = %d, want 6: %#v", len(dataPipeline), dataPipeline)
	}
	if len(countPipeline) != 4 {
		t.Fatalf("count pipeline stages = %d, want 4: %#v", len(countPipeline), countPipeline)
	}
	match := fmt.Sprint(dataPipeline[0])
	if !strings.Contains(match, "scoped-session") || strings.Contains(match, "cache_type") {
		t.Fatalf("match stage has unexpected scope: %s", match)
	}
	group := fmt.Sprint(dataPipeline[2])
	for _, fragment := range []string{"provider_requests", CacheTypeExact, CacheTypeSemantic, "total_cost"} {
		if !strings.Contains(group, fragment) {
			t.Fatalf("group stage missing %q: %s", fragment, group)
		}
	}
	wantTail := bson.A{
		bson.D{{Key: "$sort", Value: bson.D{{Key: "latest", Value: -1}, {Key: "_id", Value: 1}}}},
		bson.D{{Key: "$skip", Value: 7}},
		bson.D{{Key: "$limit", Value: 12}},
	}
	if !reflect.DeepEqual(dataPipeline[3:], wantTail) {
		t.Fatalf("data pipeline tail = %#v, want %#v", dataPipeline[3:], wantTail)
	}
	wantCount := bson.D{{Key: "$count", Value: "count"}}
	if !reflect.DeepEqual(countPipeline[3], wantCount) {
		t.Fatalf("count stage = %#v, want %#v", countPipeline[3], wantCount)
	}
}

func TestSessionCostPtrUsesZeroForCacheOnlySession(t *testing.T) {
	if got := sessionCostPtr(0, 0, 123); got == nil || *got != 0 {
		t.Fatalf("cache-only session cost = %v, want 0", got)
	}
	if got := sessionCostPtr(1, 0, 0); got != nil {
		t.Fatalf("unpriced provider session cost = %v, want nil", got)
	}
}

func TestMongoUsageLogMatchFiltersAndSearchWithCacheMode(t *testing.T) {
	got, err := mongoUsageLogMatchFilters(UsageLogParams{
		CacheMode: CacheModeUncached,
		Search:    "gpt",
	})
	if err != nil {
		t.Fatalf("mongoUsageLogMatchFilters() error = %v", err)
	}

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

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mongoUsageLogMatchFilters() = %#v, want %#v", got, want)
	}
}

func TestMongoUsageLogMatchFiltersLabel(t *testing.T) {
	got, err := mongoUsageLogMatchFilters(UsageLogParams{
		CacheMode: CacheModeAll,
		Label:     "team-alpha",
	})
	if err != nil {
		t.Fatalf("mongoUsageLogMatchFilters() error = %v", err)
	}

	want := bson.D{{Key: "labels", Value: "team-alpha"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mongoUsageLogMatchFilters() = %#v, want %#v", got, want)
	}
}

func TestMongoUsageMatchFiltersDataFilters(t *testing.T) {
	got, err := mongoUsageMatchFilters(UsageQueryParams{
		CacheMode: CacheModeAll,
		Model:     "gpt-5",
		Provider:  "openai",
		Label:     "team-alpha",
	})
	if err != nil {
		t.Fatalf("mongoUsageMatchFilters() error = %v", err)
	}

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

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mongoUsageMatchFilters() = %#v, want %#v", got, want)
	}
}

func TestMongoUsageLogMatchFiltersEscapesSearchRegex(t *testing.T) {
	got, err := mongoUsageLogMatchFilters(UsageLogParams{
		CacheMode: CacheModeAll,
		Search:    "gpt.4+",
	})
	if err != nil {
		t.Fatalf("mongoUsageLogMatchFilters() error = %v", err)
	}

	regex := bson.D{{Key: "$regex", Value: `gpt\.4\+`}, {Key: "$options", Value: "i"}}
	want := bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "model", Value: regex}},
		bson.D{{Key: "provider", Value: regex}},
		bson.D{{Key: "provider_name", Value: regex}},
		bson.D{{Key: "request_id", Value: regex}},
		bson.D{{Key: "provider_id", Value: regex}},
		bson.D{{Key: "session_id", Value: regex}},
	}}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mongoUsageLogMatchFilters() = %#v, want %#v", got, want)
	}
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
			if err != nil {
				t.Fatalf("bson.Marshal() error = %v", err)
			}
			var row mongoUsageLogRow
			if err := bson.Unmarshal(raw, &row); err != nil {
				t.Fatalf("bson.Unmarshal() error = %v", err)
			}
			entry := row.toUsageLogEntry()
			if entry.RewriteTokensSaved != tc.wantTokens {
				t.Errorf("RewriteTokensSaved = %d, want %d", entry.RewriteTokensSaved, tc.wantTokens)
			}
			switch {
			case tc.wantCost == nil:
				if entry.RewriteCostSaved != nil {
					t.Errorf("RewriteCostSaved = %v, want nil", *entry.RewriteCostSaved)
				}
			case entry.RewriteCostSaved == nil:
				t.Errorf("RewriteCostSaved = nil, want %v", *tc.wantCost)
			case *entry.RewriteCostSaved != *tc.wantCost:
				t.Errorf("RewriteCostSaved = %v, want %v", *entry.RewriteCostSaved, *tc.wantCost)
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
	if err != nil {
		t.Fatalf("mongoUsageCacheStatsPipeline returned error: %v", err)
	}
	if len(pipeline) != 4 {
		t.Fatalf("expected 4 stages (match, addFields, canonical match, project), got %d: %#v", len(pipeline), pipeline)
	}

	// Stage 1: base filters with the raw user_path condition cleared — the
	// model filter stays, the subtree match moves to the canonical stage.
	wantBase, err := mongoUsageMatchFilters(UsageQueryParams{Model: "gpt-5", CacheMode: CacheModeAll})
	if err != nil {
		t.Fatalf("mongoUsageMatchFilters returned error: %v", err)
	}
	if !reflect.DeepEqual(pipeline[0], bson.D{{Key: "$match", Value: wantBase}}) {
		t.Fatalf("unexpected base match stage: %#v", pipeline[0])
	}

	// Stages 2-3: identical canonical stages to the aggregate's own.
	if !reflect.DeepEqual(pipeline[1], mongoCanonicalUserPathAddFieldsStage()) {
		t.Fatalf("unexpected addFields stage: %#v", pipeline[1])
	}
	if !reflect.DeepEqual(pipeline[2], mongoCanonicalUserPathMatchStage("/team/alpha")) {
		t.Fatalf("unexpected canonical match stage: %#v", pipeline[2])
	}
}

// Model/label folds keep filtering the raw user_path field, matching their
// aggregates; no canonical stages are inserted.
func TestMongoUsageCacheStatsPipelineRawUserPath(t *testing.T) {
	params := UsageQueryParams{UserPath: "/team/alpha"}

	pipeline, err := mongoUsageCacheStatsPipeline(params, false)
	if err != nil {
		t.Fatalf("mongoUsageCacheStatsPipeline returned error: %v", err)
	}
	if len(pipeline) != 2 {
		t.Fatalf("expected 2 stages (match, project), got %d: %#v", len(pipeline), pipeline)
	}

	wantParams := params
	wantParams.CacheMode = CacheModeAll
	want, err := mongoUsageMatchFilters(wantParams)
	if err != nil {
		t.Fatalf("mongoUsageMatchFilters returned error: %v", err)
	}
	if !reflect.DeepEqual(pipeline[0], bson.D{{Key: "$match", Value: want}}) {
		t.Fatalf("unexpected match stage: %#v", pipeline[0])
	}
}
