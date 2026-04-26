package budget

import (
	"errors"
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestIsMongoTransactionCapabilityError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "standalone transaction message",
			err:  errors.New("Transaction numbers are only allowed on a replica set member or mongos"),
			want: true,
		},
		{
			name: "labeled unsupported transaction message",
			err: mongo.CommandError{
				Message: "transaction is not supported by this deployment",
				Labels:  []string{"TransientTransactionError"},
			},
			want: true,
		},
		{
			name: "ordinary transient transaction error",
			err: mongo.CommandError{
				Message: "temporary write conflict",
				Labels:  []string{"TransientTransactionError"},
			},
			want: false,
		},
		{
			name: "ordinary error",
			err:  errors.New("network timeout"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMongoTransactionCapabilityError(tt.err); got != tt.want {
				t.Fatalf("isMongoTransactionCapabilityError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestMongoUsagePathMatchIncludesLegacyRootRows(t *testing.T) {
	got := mongoUsagePathMatch("/")
	want := bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "user_path", Value: bson.D{{Key: "$exists", Value: false}}}},
		bson.D{{Key: "user_path", Value: bson.D{{Key: "$regex", Value: `^\s*$`}}}},
		bson.D{{Key: "user_path", Value: bson.D{{Key: "$regex", Value: "^/"}}}},
	}}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mongoUsagePathMatch(/) = %#v, want %#v", got, want)
	}
}

func TestMongoUsagePathMatchNestedPathRequiresPrefixBoundary(t *testing.T) {
	got := mongoUsagePathMatch("/team")
	want := bson.D{{Key: "user_path", Value: bson.D{{Key: "$regex", Value: `^/team(?:/|$)`}}}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mongoUsagePathMatch(/team) = %#v, want %#v", got, want)
	}
}
