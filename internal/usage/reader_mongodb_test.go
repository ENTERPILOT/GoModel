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
