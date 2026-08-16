package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
)

func TestIsOnlyDuplicateKeyErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "single duplicate key write error",
			err: mongo.BulkWriteException{
				WriteErrors: []mongo.BulkWriteError{
					{WriteError: mongo.WriteError{Code: 11000, Message: "E11000 duplicate key error"}},
				},
			},
			want: true,
		},
		{
			name: "all duplicate key write errors",
			err: mongo.BulkWriteException{
				WriteErrors: []mongo.BulkWriteError{
					{WriteError: mongo.WriteError{Code: 11000}},
					{WriteError: mongo.WriteError{Code: 11000}},
				},
			},
			want: true,
		},
		{
			name: "mixed write errors keep failing",
			err: mongo.BulkWriteException{
				WriteErrors: []mongo.BulkWriteError{
					{WriteError: mongo.WriteError{Code: 11000}},
					{WriteError: mongo.WriteError{Code: 121, Message: "document validation failure"}},
				},
			},
			want: false,
		},
		{
			name: "write concern error keeps failing",
			err: mongo.BulkWriteException{
				WriteErrors: []mongo.BulkWriteError{
					{WriteError: mongo.WriteError{Code: 11000}},
				},
				WriteConcernError: &mongo.WriteConcernError{Code: 64},
			},
			want: false,
		},
		{
			name: "empty bulk exception",
			err:  mongo.BulkWriteException{},
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection reset"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOnlyDuplicateKeyErrors(tt.err); got != tt.want {
				t.Fatalf("isOnlyDuplicateKeyErrors() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDuplicateKeyErrorsOnConfigRulesOnly(t *testing.T) {
	configRule := Rule{Scope: ScopeUserPath, Subject: "/seed", PeriodSeconds: PeriodMinuteSeconds, Source: SourceConfig}
	manualRule := Rule{Scope: ScopeUserPath, Subject: "/manual", PeriodSeconds: PeriodMinuteSeconds, Source: SourceManual}
	dupAt := func(indexes ...int) mongo.BulkWriteException {
		exc := mongo.BulkWriteException{}
		for _, index := range indexes {
			exc.WriteErrors = append(exc.WriteErrors, mongo.BulkWriteError{
				WriteError: mongo.WriteError{Index: index, Code: 11000},
			})
		}
		return exc
	}

	tests := []struct {
		name  string
		err   error
		rules []Rule
		want  bool
	}{
		{
			name:  "duplicate on config rule is the intended shadowing",
			err:   dupAt(0),
			rules: []Rule{configRule},
			want:  true,
		},
		{
			name:  "duplicate on manual rule is a real insert race",
			err:   dupAt(1),
			rules: []Rule{configRule, manualRule},
			want:  false,
		},
		{
			name:  "mixed batch with only config duplicates passes",
			err:   dupAt(0),
			rules: []Rule{configRule, manualRule},
			want:  true,
		},
		{
			name:  "index out of range keeps failing",
			err:   dupAt(5),
			rules: []Rule{configRule},
			want:  false,
		},
		{
			name:  "non duplicate-key code keeps failing",
			err:   mongo.BulkWriteException{WriteErrors: []mongo.BulkWriteError{{WriteError: mongo.WriteError{Index: 0, Code: 121}}}},
			rules: []Rule{configRule},
			want:  false,
		},
		{
			name:  "unrelated error keeps failing",
			err:   errors.New("network down"),
			rules: []Rule{configRule},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := duplicateKeyErrorsOnConfigRulesOnly(tt.err, tt.rules); got != tt.want {
				t.Fatalf("duplicateKeyErrorsOnConfigRulesOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestClassifyBulkWriteError pins the upsert error-handling contract: config
// duplicates are benign shadowing, manual duplicates earn one retry, and
// everything else fails. The retry itself lands as a plain update because the
// conflicting documents exist by then.
func TestClassifyBulkWriteError(t *testing.T) {
	configRule := Rule{Scope: ScopeUserPath, Subject: "/seed", PeriodSeconds: PeriodMinuteSeconds, Source: SourceConfig}
	manualRule := Rule{Scope: ScopeUserPath, Subject: "/manual", PeriodSeconds: PeriodMinuteSeconds, Source: SourceManual}
	dup := func(index int) mongo.BulkWriteException {
		return mongo.BulkWriteException{WriteErrors: []mongo.BulkWriteError{
			{WriteError: mongo.WriteError{Index: index, Code: 11000}},
		}}
	}

	tests := []struct {
		name  string
		err   error
		rules []Rule
		want  bulkWriteOutcome
	}{
		{"success", nil, []Rule{manualRule}, bulkWriteOK},
		{"config duplicate is shadowing", dup(0), []Rule{configRule}, bulkWriteShadowedByManual},
		{"manual duplicate is a race worth retrying", dup(1), []Rule{configRule, manualRule}, bulkWriteRetryManualRace},
		{"non-duplicate error fails", mongo.BulkWriteException{WriteErrors: []mongo.BulkWriteError{
			{WriteError: mongo.WriteError{Index: 0, Code: 121}},
		}}, []Rule{manualRule}, bulkWriteFailed},
		{"unrelated error fails", errors.New("network down"), []Rule{manualRule}, bulkWriteFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyBulkWriteError(tt.err, tt.rules); got != tt.want {
				t.Fatalf("classifyBulkWriteError() = %d, want %d", got, tt.want)
			}
		})
	}
}

// A MongoDB database written before rule scopes existed carries a unique index
// on (user_path, period_seconds). Unsetting user_path collapses every migrated
// document of the same period onto one index key, so the legacy index has to go
// before the rewrite rather than after it.
func TestMongoDBStoreMigratesPreScopeDocuments(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		rules := db.Collection("rate_limits")
		if _, err := rules.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: "user_path", Value: 1}, {Key: "period_seconds", Value: 1}},
			Options: options.Index().SetUnique(true),
		}); err != nil {
			t.Fatalf("create pre-scope index: %v", err)
		}
		now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
		// Two paths sharing one period: the case that collides.
		if _, err := rules.InsertMany(ctx, []any{
			bson.D{{Key: "user_path", Value: "/team/alpha"}, {Key: "period_seconds", Value: PeriodMinuteSeconds},
				{Key: "max_requests", Value: int64(10)}, {Key: "source", Value: SourceManual},
				{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}},
			bson.D{{Key: "user_path", Value: "/team/beta"}, {Key: "period_seconds", Value: PeriodMinuteSeconds},
				{Key: "max_requests", Value: int64(20)}, {Key: "source", Value: SourceManual},
				{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}},
		}); err != nil {
			t.Fatalf("seed pre-scope documents: %v", err)
		}

		store, err := NewMongoDBStore(ctx, db)
		if err != nil {
			t.Fatalf("NewMongoDBStore() failed: %v", err)
		}
		got, err := store.ListRules(ctx)
		if err != nil {
			t.Fatalf("ListRules() failed: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("migrated rules = %+v, want both pre-scope rows", got)
		}
		for _, rule := range got {
			if rule.Scope != ScopeUserPath || rule.Subject == "" {
				t.Fatalf("migrated rule %+v, want a user_path scope and a subject", rule)
			}
		}
	})
}

// TestMongoDBStoreCounterRoundTrip runs the shared counter suite: MongoDB has
// its own upsert and staleness-collection queries, so it has to prove the same
// behaviour the SQL backends do.
func TestMongoDBStoreCounterRoundTrip(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewMongoDBStore() failed: %v", err)
		}
		runCounterStoreSuite(t, store, func(t *testing.T, snap WindowSnapshot, updatedAt int64) {
			t.Helper()
			doc := counterDocument{WindowSnapshot: snap, UpdatedAt: updatedAt}
			if _, err := db.Collection("rate_limit_counters").InsertOne(context.Background(), doc); err != nil {
				t.Fatalf("seed stale counter: %v", err)
			}
		})
	})
}
