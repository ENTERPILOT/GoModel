package usage

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoUsageGroupedProviderNameExpr_CollapsesBlankProviderName(t *testing.T) {
	got := mongoUsageGroupedProviderNameExpr()
	want := bson.D{{Key: "$cond", Value: bson.A{
		bson.D{{Key: "$ne", Value: bson.A{
			bson.D{{Key: "$trim", Value: bson.D{
				{Key: "input", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$provider_name", ""}}}},
			}}},
			"",
		}}},
		bson.D{{Key: "$trim", Value: bson.D{
			{Key: "input", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$provider_name", ""}}}},
		}}},
		bson.D{{Key: "$trim", Value: bson.D{{Key: "input", Value: "$provider"}}}},
	}}}

	require.Equal(t, want, got)
}
