package budget

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// storedRevision reads the alert revision of the one budget in store.
func storedRevision(t *testing.T, store Store) string {
	t.Helper()
	budgets, err := store.ListBudgets(context.Background())
	require.NoError(t, err)
	require.Len(t, budgets, 1)
	return budgets[0].alertRevision()
}

// assertBudgetRevisions is the revision contract every backend has to keep: a
// budget gets a revision no budget had before whenever it is created or its
// amount or cost basis changes, and keeps it through every other write. first
// and second are two store instances on one database, as two gateway replicas
// would open: every write goes through first and is read through second.
func assertBudgetRevisions(t *testing.T, first, second Store) {
	t.Helper()
	ctx := context.Background()
	// Every write carries the same creation time, so nothing but the stored
	// revision can tell a recreated budget from the one it replaces.
	created := time.Date(2026, time.April, 24, 12, 0, 0, 0, time.UTC)
	budget := func(amount float64, basis CostBasis, thresholds ...int) Budget {
		return Budget{
			Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: amount,
			CostBasis: basis, AlertThresholds: thresholds, Source: SourceManual, CreatedAt: created,
		}
	}
	upsert := func(b Budget) func() error {
		return func() error { return first.UpsertBudgets(ctx, []Budget{b}) }
	}

	steps := []struct {
		name    string
		write   func() error
		wantNew bool
	}{
		{name: "created", write: upsert(budget(100, CostBasisProvider)), wantNew: true},
		{name: "saved unchanged", write: upsert(budget(100, CostBasisProvider))},
		{name: "thresholds edited", write: upsert(budget(100, CostBasisProvider, 50, 90))},
		{name: "reset", write: func() error {
			return first.ResetBudget(ctx, ScopeUserPath, "/team", PeriodDailySeconds, created.Add(time.Hour))
		}},
		{name: "config re-seed of a manual budget", write: func() error {
			return first.ReplaceConfigBudgets(ctx, []Budget{budget(500, CostBasisProvider)})
		}},
		{name: "amount changed", write: upsert(budget(200, CostBasisProvider)), wantNew: true},
		{name: "amount changed back", write: upsert(budget(100, CostBasisProvider)), wantNew: true},
		{name: "cost basis changed", write: upsert(budget(100, CostBasisBilled)), wantNew: true},
		{name: "deleted and recreated within the same second", write: func() error {
			if err := first.DeleteBudget(ctx, ScopeUserPath, "/team", PeriodDailySeconds); err != nil {
				return err
			}
			return first.UpsertBudgets(ctx, []Budget{budget(100, CostBasisBilled)})
		}, wantNew: true},
	}
	seen := make(map[string]string)
	previous := ""
	for _, step := range steps {
		require.NoError(t, step.write(), step.name)
		revision := storedRevision(t, second)
		require.NotEmpty(t, revision, step.name)
		if step.wantNew {
			assert.Empty(t, seen[revision], "%s: the revision was already used by %q", step.name, seen[revision])
		} else {
			assert.Equal(t, previous, revision, "%s keeps the revision", step.name)
		}
		seen[revision] = step.name
		previous = revision
	}
}

func TestSQLStoreBudgetRevisions(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		first, err := NewSQLStore(ctx, db)
		require.NoError(t, err)
		second, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		assertBudgetRevisions(t, first, second)
	})
}

func TestMongoDBStoreBudgetRevisions(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		first, err := NewMongoDBStore(ctx, db)
		require.NoError(t, err)
		second, err := NewMongoDBStore(ctx, db)
		require.NoError(t, err)

		assertBudgetRevisions(t, first, second)
	})
}

// assertLegacyBudgetRevision covers a budget stored before revisions existed:
// it keeps the revision it was identified by then, on every replica, until
// its amount changes.
func assertLegacyBudgetRevision(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	legacy := Budget{Scope: ScopeUserPath, Subject: "/existing", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual}

	assert.Equal(t, "1:10", storedRevision(t, store))

	legacy.AlertThresholds = []int{50}
	require.NoError(t, store.UpsertBudgets(ctx, []Budget{legacy}))
	assert.Equal(t, "1:10", storedRevision(t, store), "a threshold edit keeps it")

	legacy.Amount = 20
	require.NoError(t, store.UpsertBudgets(ctx, []Budget{legacy}))
	changed := storedRevision(t, store)
	assert.NotEqual(t, "1:20", changed, "a changed amount gets a stored revision")

	legacy.Amount = 10
	require.NoError(t, store.UpsertBudgets(ctx, []Budget{legacy}))
	assert.NotContains(t, []string{"1:10", changed}, storedRevision(t, store), "the old amount does not bring the old revision back")
}

func TestSQLStoreKeepsTheRevisionOfBudgetsStoredBeforeRevisions(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		createBudgetsTableBeforeAlerts(t, db)
		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		assertLegacyBudgetRevision(t, store)
	})
}

func TestMongoDBStoreKeepsTheRevisionOfBudgetsStoredBeforeRevisions(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		_, err := db.Collection("budgets").InsertOne(ctx, bson.D{
			{Key: "scope", Value: ScopeUserPath}, {Key: "subject", Value: "/existing"},
			{Key: "period_seconds", Value: PeriodDailySeconds}, {Key: "amount", Value: 10.0},
			{Key: "source", Value: SourceManual},
			{Key: "created_at", Value: time.Unix(1, 0).UTC()}, {Key: "updated_at", Value: time.Unix(1, 0).UTC()},
		})
		require.NoError(t, err)
		store, err := NewMongoDBStore(ctx, db)
		require.NoError(t, err)

		assertLegacyBudgetRevision(t, store)
	})
}
