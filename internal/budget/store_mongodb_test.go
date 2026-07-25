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
			name: "illegal operation command code",
			err: mongo.CommandError{
				Code:    20,
				Message: "transaction is not supported by this deployment",
				Labels:  []string{"TransientTransactionError"},
			},
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

func TestUsagePathRegexBoundaries(t *testing.T) {
	// The root pattern must also catch the rows a blank user path normalizes
	// to; nested paths must not leak into a sibling that shares their prefix.
	if got, want := usagePathRegex("/"), "^/"; got != want {
		t.Fatalf("usagePathRegex(/) = %q, want %q", got, want)
	}
	if got, want := usagePathRegex("/team"), `^/team(?:/|$)`; got != want {
		t.Fatalf("usagePathRegex(/team) = %q, want %q", got, want)
	}
}

func TestMongoSubjectMatchUsesLabelMembership(t *testing.T) {
	got, err := mongoSubjectMatch(SpendWindow{Scope: ScopeLabel, Subject: "iOS"})
	if err != nil {
		t.Fatalf("mongoSubjectMatch() failed: %v", err)
	}
	want := bson.D{{Key: "$in", Value: bson.A{
		"iOS",
		bson.D{{Key: "$ifNull", Value: bson.A{"$labels", bson.A{}}}},
	}}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mongoSubjectMatch(label iOS) = %#v, want %#v", got, want)
	}
}

func TestMongoSubjectMatchRejectsBlankLabel(t *testing.T) {
	if _, err := mongoSubjectMatch(SpendWindow{Scope: ScopeLabel, Subject: "  "}); err == nil {
		t.Fatal("mongoSubjectMatch() error = nil, want blank label rejection")
	}
}

func TestMongoSubjectMatchNormalizesMissingUserPathToRoot(t *testing.T) {
	got, err := mongoSubjectMatch(SpendWindow{Scope: ScopeUserPath, Subject: "/team"})
	if err != nil {
		t.Fatalf("mongoSubjectMatch() failed: %v", err)
	}
	expression, ok := bsonField(got, "$regexMatch").(bson.D)
	if !ok {
		t.Fatalf("mongoSubjectMatch(user_path) = %#v, want a $regexMatch expression", got)
	}
	if regex := bsonField(expression, "regex"); regex != usagePathRegex("/team") {
		t.Fatalf("regex = %v, want %q", regex, usagePathRegex("/team"))
	}
	// The input must fold missing and blank paths to "/" so a root budget still
	// sees rows recorded without a user path.
	if _, ok := bsonField(expression, "input").(bson.D); !ok {
		t.Fatalf("input = %#v, want the normalizing $let expression", bsonField(expression, "input"))
	}
}

func bsonField(document bson.D, key string) any {
	for _, element := range document {
		if element.Key == key {
			return element.Value
		}
	}
	return nil
}

func TestBsonNumberReadsEveryNumericShape(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  float64
		ok    bool
	}{
		{name: "double", value: 1.5, want: 1.5, ok: true},
		{name: "int64", value: int64(3), want: 3, ok: true},
		{name: "int32", value: int32(2), want: 2, ok: true},
		{name: "missing", value: nil},
		{name: "unexpected type", value: "7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := bsonNumber(tt.value)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("bsonNumber(%v) = %v/%v, want %v/%v", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}
