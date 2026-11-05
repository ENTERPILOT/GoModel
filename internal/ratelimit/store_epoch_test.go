package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
)

// runCounterEpochSuite checks the Rule.CounterEpoch contract every store
// keeps: a new rule or a per-child switch draws a new epoch, any other edit
// keeps it, and saved rows round-trip the epoch they were written under.
func runCounterEpochSuite(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	epoch := func(t *testing.T) string {
		t.Helper()
		rules, err := store.ListRules(ctx)
		require.NoError(t, err)
		for _, rule := range rules {
			if rule.Subject == "/team" {
				return rule.CounterEpoch
			}
		}
		t.Fatal("rule /team not found")
		return ""
	}
	upsert := func(t *testing.T, rule Rule) {
		t.Helper()
		require.NoError(t, store.UpsertRules(ctx, []Rule{rule}))
	}

	upsert(t, teamRule(false, 100))
	created := epoch(t)
	require.NotEmpty(t, created, "a new rule draws an epoch")

	upsert(t, teamRule(false, 200))
	assert.Equal(t, created, epoch(t), "a limit edit keeps the epoch")

	upsert(t, teamRule(true, 200))
	perChild := epoch(t)
	assert.NotEqual(t, created, perChild, "a per-child switch draws a new epoch")

	upsert(t, teamRule(false, 200))
	shared := epoch(t)
	assert.NotEqual(t, created, shared, "switching back does not reuse the old epoch")
	assert.NotEqual(t, perChild, shared)

	require.NoError(t, store.DeleteRule(ctx, ScopeUserPath, "/team", PeriodDaySeconds))
	upsert(t, teamRule(false, 200))
	assert.NotEqual(t, shared, epoch(t), "a re-added rule draws a new epoch")

	// A config re-seed of an unchanged rule keeps its epoch, and one shadowed
	// by a manual rule leaves the manual rule's epoch alone.
	configRule := Rule{Scope: ScopeUserPath, Subject: "/seeded", PeriodSeconds: PeriodDaySeconds, MaxRequests: new(int64(5))}
	require.NoError(t, store.ReplaceConfigRules(ctx, []Rule{configRule}))
	seededEpoch := func(t *testing.T) string {
		t.Helper()
		rules, err := store.ListRules(ctx)
		require.NoError(t, err)
		for _, rule := range rules {
			if rule.Subject == "/seeded" {
				return rule.CounterEpoch
			}
		}
		t.Fatal("rule /seeded not found")
		return ""
	}
	seeded := seededEpoch(t)
	require.NotEmpty(t, seeded)
	require.NoError(t, store.ReplaceConfigRules(ctx, []Rule{configRule}))
	assert.Equal(t, seeded, seededEpoch(t), "a re-seed keeps the epoch")
	before := epoch(t)
	shadow := Rule{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDaySeconds, PerChild: true, MaxRequests: new(int64(5))}
	require.NoError(t, store.ReplaceConfigRules(ctx, []Rule{configRule, shadow}))
	assert.Equal(t, before, epoch(t), "a config seed shadowed by a manual rule changes nothing")
	rules, err := store.ListRules(ctx)
	require.NoError(t, err)
	for _, rule := range rules {
		if rule.Subject == "/team" {
			require.NotNil(t, rule.MaxRequests)
			assert.Equal(t, int64(200), *rule.MaxRequests, "the manual limit survives the shadowed seed")
			assert.False(t, rule.PerChild, "the manual mode survives the shadowed seed")
		}
	}

	snap := WindowSnapshot{
		Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: PeriodDaySeconds,
		Writer: "w1", Epoch: "e1", RequestsWindowStart: 1700000000, RequestsCurrent: 3,
	}
	require.NoError(t, store.SaveCounters(ctx, []WindowSnapshot{snap}))
	snap.Epoch = "e2"
	require.NoError(t, store.SaveCounters(ctx, []WindowSnapshot{snap}))
	rows, err := store.LoadCounters(ctx, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "e2", rows[0].Epoch, "a save rewrites the row's epoch")
}

func TestSQLStoreCounterEpochs(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		runCounterEpochSuite(t, store)
	})
}

func TestMongoDBStoreCounterEpochs(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(context.Background(), db)
		require.NoError(t, err)
		runCounterEpochSuite(t, store)
	})
}

// A rule stored before counter epochs existed has the empty epoch, as do the
// rows saved for it. An edit after the upgrade must keep it empty, or the
// first edit of every existing rule would drop its saved counts.
func TestSQLStoreKeepsEmptyEpochOfRulesFromBeforeEpochs(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		_, err := store.db.Exec(ctx, `
			INSERT INTO rate_limits (scope, subject, per_child, period_seconds, max_requests, source, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, ScopeUserPath, "/team", false, PeriodDaySeconds, 100, SourceManual, 1700000000, 1700000000)
		require.NoError(t, err)
		require.NoError(t, store.UpsertRules(ctx, []Rule{teamRule(false, 200)}))
		rules, err := store.ListRules(ctx)
		require.NoError(t, err)
		require.Len(t, rules, 1)
		assert.Empty(t, rules[0].CounterEpoch)
		assert.Equal(t, int64(200), *rules[0].MaxRequests)
	})
}

func TestMongoDBStoreKeepsEmptyEpochOfRulesFromBeforeEpochs(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(ctx, db)
		require.NoError(t, err)
		_, err = db.Collection("rate_limits").InsertOne(ctx, bson.D{
			{Key: "scope", Value: ScopeUserPath}, {Key: "subject", Value: "/team"},
			{Key: "period_seconds", Value: PeriodDaySeconds}, {Key: "max_requests", Value: int64(100)},
			{Key: "source", Value: SourceManual}, {Key: "created_at", Value: time.Unix(1700000000, 0)},
		})
		require.NoError(t, err)
		require.NoError(t, store.UpsertRules(ctx, []Rule{teamRule(false, 200)}))
		rules, err := store.ListRules(ctx)
		require.NoError(t, err)
		require.Len(t, rules, 1)
		assert.Empty(t, rules[0].CounterEpoch)
		assert.Equal(t, int64(200), *rules[0].MaxRequests)
		assert.Equal(t, time.Unix(1700000000, 0).UTC(), rules[0].CreatedAt.UTC(), "an update keeps created_at")
	})
}
