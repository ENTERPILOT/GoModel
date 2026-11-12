package budget

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/internal/usage"
)

var alertTestNow = time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)

func testAlertClaim() AlertClaim {
	start := time.Date(2026, time.April, 25, 0, 0, 0, 0, time.UTC)
	return AlertClaim{
		Scope: ScopeUserPath, Subject: "/customers", EffectiveSubject: "/customers/alice",
		PeriodSeconds: PeriodDailySeconds, PeriodStart: start, PeriodEnd: start.AddDate(0, 0, 1), Threshold: 80,
	}
}

// assertAlertClaims is the claim contract every backend has to keep: the
// first claim of an alert wins, and every part of its identity separates it
// from another alert. first and second are two store instances on one
// database, as two gateway replicas would open.
func assertAlertClaims(t *testing.T, first, second AlertStore) {
	t.Helper()
	ctx := context.Background()
	claim := testAlertClaim()

	won, err := first.ClaimAlert(ctx, claim)
	require.NoError(t, err)
	require.True(t, won, "the first claim wins")

	for name, store := range map[string]AlertStore{"same instance": first, "other instance": second} {
		won, err = store.ClaimAlert(ctx, claim)
		require.NoError(t, err)
		assert.False(t, won, "%s: a claimed alert must not fire again", name)
	}

	distinct := map[string]func(*AlertClaim){
		"threshold":         func(c *AlertClaim) { c.Threshold = 100 },
		"period start":      func(c *AlertClaim) { c.PeriodStart = c.PeriodStart.Add(time.Hour) },
		"period":            func(c *AlertClaim) { c.PeriodSeconds = PeriodWeeklySeconds },
		"effective subject": func(c *AlertClaim) { c.EffectiveSubject = "/customers/bob" },
		"subject":           func(c *AlertClaim) { c.Subject = "/customers/alice" },
		"scope":             func(c *AlertClaim) { c.Scope = ScopeLabel },
	}
	for name, change := range distinct {
		other := claim
		change(&other)
		won, err = second.ClaimAlert(ctx, other)
		require.NoError(t, err)
		assert.True(t, won, "a different %s is a different alert", name)
	}

	// Racing replicas: exactly one claim of a fresh alert wins.
	fresh := claim
	fresh.Threshold = 50
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		store := first
		if i%2 == 1 {
			store = second
		}
		wg.Go(func() {
			won, err := store.ClaimAlert(ctx, fresh)
			if assert.NoError(t, err) && won {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load())

	// Pruning forgets only the alerts of periods that ended before the cutoff.
	require.NoError(t, first.PruneAlerts(ctx, claim.PeriodEnd))
	won, err = first.ClaimAlert(ctx, claim)
	require.NoError(t, err)
	assert.False(t, won, "an alert whose period ends at the cutoff is kept")

	require.NoError(t, first.PruneAlerts(ctx, claim.PeriodEnd.Add(time.Second)))
	won, err = first.ClaimAlert(ctx, claim)
	require.NoError(t, err)
	assert.True(t, won, "a pruned alert can be claimed again")
}

func TestSQLStoreAlertClaims(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		first, err := NewSQLStore(ctx, db)
		require.NoError(t, err)
		second, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		assertAlertClaims(t, first, second)
	})
}

func TestMongoDBStoreAlertClaims(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		first, err := NewMongoDBStore(ctx, db)
		require.NoError(t, err)
		second, err := NewMongoDBStore(ctx, db)
		require.NoError(t, err)

		assertAlertClaims(t, first, second)
	})
}

// wantSpendByUserPath is what both backends must return for the usage the two
// tests below write: largest first, grouped by recorded path, with the rows a
// user-path budget does not count left out.
var wantSpendByUserPath = []PathSpend{
	{UserPath: "/customers/carol", Total: 10.5},
	{UserPath: "/customers/alice", Total: 5},
	{UserPath: "/customers/alice/app", Total: 4},
	{UserPath: "/customers/bob", Total: 3},
	{UserPath: "/customers", Total: 1},
}

func spendByUserPathWindow() SpendWindow {
	return SpendWindow{
		Scope: ScopeUserPath, Subject: "/customers",
		Start: alertTestNow.Add(-12 * time.Hour), End: alertTestNow,
	}
}

func TestSQLStoreSumSpendByUserPath(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		usageStore, err := usage.NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		at := alertTestNow.Add(-2 * time.Hour)
		err = usageStore.WriteBatch(ctx, []*usage.UsageEntry{
			usageEntryWithCost("alice-1", "/customers/alice", "", at, 2),
			usageEntryWithCost("alice-2", "/customers/alice", "", at, 3),
			usageEntryWithCost("alice-app", "/customers/alice/app", "", at, 4),
			usageEntryWithCost("bob", "/customers/bob", "", at, 3),
			usageEntryWithCost("carol", "/customers/carol", "", at, 10.5),
			usageEntryWithCost("template", "/customers", "", at, 1),
			usageEntryWithCost("sibling", "/customers-eu/dave", "", at, 50),
			usageEntryWithCost("cached", "/customers/erin", usage.CacheTypeExact, at, 50),
			usageEntryWithCost("yesterday", "/customers/frank", "", alertTestNow.Add(-36*time.Hour), 50),
			usageEntryNonBillable("byok", "/customers/gina", at, 50),
		})
		require.NoError(t, err)

		got, err := store.SumSpendByUserPath(ctx, spendByUserPathWindow(), 100)
		require.NoError(t, err)
		assert.Equal(t, wantSpendByUserPath, got)

		got, err = store.SumSpendByUserPath(ctx, spendByUserPathWindow(), 2)
		require.NoError(t, err)
		assert.Equal(t, wantSpendByUserPath[:2], got, "the limit keeps the largest spenders")

		_, err = store.SumSpendByUserPath(ctx, SpendWindow{Scope: ScopeLabel, Subject: "prod"}, 10)
		require.Error(t, err)
	})
}

func TestMongoDBStoreSumSpendByUserPath(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(ctx, db)
		require.NoError(t, err)

		at := alertTestNow.Add(-2 * time.Hour)
		row := func(userPath string, cost float64, extra ...bson.E) bson.D {
			return append(bson.D{{Key: "timestamp", Value: at}, {Key: "user_path", Value: userPath}, {Key: "total_cost", Value: cost}}, extra...)
		}
		_, err = db.Collection("usage").InsertMany(ctx, []any{
			row("/customers/alice", 2),
			row("/customers/alice", 3),
			row("/customers/alice/app", 4),
			row("/customers/bob", 3),
			row("/customers/carol", 10.5),
			row("/customers", 1),
			row("/customers-eu/dave", 50),
			row("/customers/erin", 50, bson.E{Key: "cache_type", Value: "exact"}),
			bson.D{{Key: "timestamp", Value: alertTestNow.Add(-36 * time.Hour)}, {Key: "user_path", Value: "/customers/frank"}, {Key: "total_cost", Value: 50.0}},
			row("/customers/gina", 50, bson.E{Key: "billable", Value: false}),
			// Unpriced usage is not spend.
			bson.D{{Key: "timestamp", Value: at}, {Key: "user_path", Value: "/customers/henry"}},
		})
		require.NoError(t, err)

		got, err := store.SumSpendByUserPath(ctx, spendByUserPathWindow(), 100)
		require.NoError(t, err)
		assert.Equal(t, wantSpendByUserPath, got)

		got, err = store.SumSpendByUserPath(ctx, spendByUserPathWindow(), 2)
		require.NoError(t, err)
		assert.Equal(t, wantSpendByUserPath[:2], got, "the limit keeps the largest spenders")
	})
}

func TestSQLStoreRoundTripsAlertThresholds(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/own", PeriodSeconds: PeriodDailySeconds, Amount: 10, AlertThresholds: []int{90, 50, 90}, Source: SourceManual},
			{Scope: ScopeUserPath, Subject: "/default", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
		})
		require.NoError(t, err)

		budgets, err := store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, budgets, 2)
		assert.Nil(t, budgets[0].AlertThresholds, "/default keeps the defaults")
		assert.Equal(t, []int{50, 90}, budgets[1].AlertThresholds, "stored sorted, without duplicates")

		// A config re-seed must not overwrite thresholds edited by hand.
		err = store.ReplaceConfigBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/own", PeriodSeconds: PeriodDailySeconds, Amount: 10, AlertThresholds: []int{75}},
		})
		require.NoError(t, err)
		budgets, err = store.ListBudgets(ctx)
		require.NoError(t, err)
		assert.Equal(t, []int{50, 90}, budgets[1].AlertThresholds)

		err = store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/own", PeriodSeconds: PeriodDailySeconds, Amount: 10, AlertThresholds: []int{101}, Source: SourceManual},
		})
		require.ErrorContains(t, err, "between 1 and 100")
	})
}

func TestSQLStoreMigratesBudgetsWithoutAlertThresholds(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// The budgets table as it was before alert thresholds existed.
		err := db.Schema(ctx, `CREATE TABLE budgets (
			scope TEXT NOT NULL DEFAULT 'user_path',
			subject TEXT NOT NULL,
			per_child `+sqlx.TypeBool+` NOT NULL DEFAULT FALSE,
			cost_basis TEXT NOT NULL DEFAULT 'provider',
			period_seconds `+sqlx.TypeInt64+` NOT NULL,
			amount `+sqlx.TypeFloat+` NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			last_reset_at `+sqlx.TypeInt64+`,
			created_at `+sqlx.TypeInt64+` NOT NULL,
			updated_at `+sqlx.TypeInt64+` NOT NULL,
			PRIMARY KEY (scope, subject, period_seconds)
		)`)
		require.NoError(t, err)
		_, err = db.Exec(ctx, `
			INSERT INTO budgets (scope, subject, period_seconds, amount, source, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, ScopeUserPath, "/existing", PeriodDailySeconds, 10.0, SourceManual, int64(1), int64(1))
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		budgets, err := store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, budgets, 1)
		assert.Nil(t, budgets[0].AlertThresholds)

		won, err := store.ClaimAlert(ctx, testAlertClaim())
		require.NoError(t, err)
		assert.True(t, won)
	})
}

func TestMongoDBStoreRoundTripsAlertThresholds(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(ctx, db)
		require.NoError(t, err)

		err = store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/own", PeriodSeconds: PeriodDailySeconds, Amount: 10, AlertThresholds: []int{90, 50}, Source: SourceManual},
			{Scope: ScopeUserPath, Subject: "/default", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
		})
		require.NoError(t, err)

		budgets, err := store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, budgets, 2)
		assert.Empty(t, budgets[0].AlertThresholds, "/default keeps the defaults")
		assert.Equal(t, []int{50, 90}, budgets[1].AlertThresholds)

		// Saving without thresholds returns the budget to the defaults.
		err = store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/own", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
		})
		require.NoError(t, err)
		budgets, err = store.ListBudgets(ctx)
		require.NoError(t, err)
		assert.Empty(t, budgets[1].AlertThresholds)
	})
}
