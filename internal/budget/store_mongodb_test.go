package budget

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
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

func TestMongoDBStoreRoundTripsPerChild(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(ctx, db)
		if err != nil {
			t.Fatalf("NewMongoDBStore() failed: %v", err)
		}
		budgets := []Budget{
			{Scope: ScopeUserPath, Subject: "/shared", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
			{Scope: ScopeUserPath, Subject: "/customers", PerChild: true, PeriodSeconds: PeriodDailySeconds, Amount: 20, Source: SourceManual},
		}
		if err := store.UpsertBudgets(ctx, budgets); err != nil {
			t.Fatalf("UpsertBudgets() failed: %v", err)
		}

		got, err := store.ListBudgets(ctx)
		if err != nil {
			t.Fatalf("ListBudgets() failed: %v", err)
		}
		bySubject := make(map[string]Budget, len(got))
		for _, budget := range got {
			bySubject[budget.Subject] = budget
		}
		for _, want := range budgets {
			if persisted, ok := bySubject[want.Subject]; !ok || persisted.PerChild != want.PerChild {
				t.Errorf("budget %q = %+v, want per_child=%v", want.Subject, persisted, want.PerChild)
			}
		}
	})
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

// A MongoDB database written before budget scopes existed carries a unique
// index on (user_path, period_seconds). Unsetting user_path collapses every
// migrated document of the same period onto one index key, so the legacy index
// has to go before the rewrite rather than after it.
func TestMongoDBStoreMigratesPreScopeDocuments(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		budgets := db.Collection("budgets")
		if _, err := budgets.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: "user_path", Value: 1}, {Key: "period_seconds", Value: 1}},
			Options: options.Index().SetUnique(true),
		}); err != nil {
			t.Fatalf("create pre-scope index: %v", err)
		}
		now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
		// Two paths sharing one period: the case that collides.
		if _, err := budgets.InsertMany(ctx, []any{
			bson.D{{Key: "user_path", Value: "/team/alpha"}, {Key: "period_seconds", Value: PeriodDailySeconds},
				{Key: "amount", Value: 10.0}, {Key: "source", Value: SourceManual},
				{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}},
			bson.D{{Key: "user_path", Value: "/team/beta"}, {Key: "period_seconds", Value: PeriodDailySeconds},
				{Key: "amount", Value: 20.0}, {Key: "source", Value: SourceManual},
				{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}},
		}); err != nil {
			t.Fatalf("seed pre-scope documents: %v", err)
		}

		store, err := NewMongoDBStore(ctx, db)
		if err != nil {
			t.Fatalf("NewMongoDBStore() failed: %v", err)
		}
		got, err := store.ListBudgets(ctx)
		if err != nil {
			t.Fatalf("ListBudgets() failed: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("migrated budgets = %+v, want both pre-scope rows", got)
		}
		bySubject := map[string]Budget{}
		for _, budget := range got {
			if budget.Scope != ScopeUserPath {
				t.Fatalf("migrated budget %+v, want scope user_path", budget)
			}
			bySubject[budget.Subject] = budget
		}
		if bySubject["/team/alpha"].Amount != 10 || bySubject["/team/beta"].Amount != 20 {
			t.Fatalf("migrated budgets = %+v, want both amounts preserved", got)
		}

		// The scoped unique index must now allow a label budget spelled like an
		// existing user path.
		if err := store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeLabel, Subject: "/team/alpha", PeriodSeconds: PeriodDailySeconds, Amount: 1},
		}); err != nil {
			t.Fatalf("UpsertBudgets() after migration failed: %v", err)
		}
	})
}
