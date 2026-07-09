package usage

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoUsageLogMatchFiltersAndSearchWithCacheMode(t *testing.T) {
	got, err := mongoUsageLogMatchFilters(UsageLogParams{
		UsageQueryParams: UsageQueryParams{
			CacheMode: CacheModeUncached,
		},
		Search: "gpt",
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
		}}},
	}}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mongoUsageLogMatchFilters() = %#v, want %#v", got, want)
	}
}

func TestMongoUsageLogMatchFiltersLabel(t *testing.T) {
	got, err := mongoUsageLogMatchFilters(UsageLogParams{
		UsageQueryParams: UsageQueryParams{
			CacheMode: CacheModeAll,
			Label:     "team-alpha",
		},
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
		UsageQueryParams: UsageQueryParams{
			CacheMode: CacheModeAll,
		},
		Search: "gpt.4+",
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
