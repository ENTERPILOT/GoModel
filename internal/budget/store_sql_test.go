package budget

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/stretchr/testify/require"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		body(t, store)
	})
}

func TestSQLStoreRoundTripsPerChild(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.UpsertBudgets(ctx, []Budget{{
			Scope: ScopeUserPath, Subject: "/users", PerChild: true,
			PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual,
		}})
		require.NoError(t, err)

		budgets, err := store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, budgets, 1)
		require.True(t, budgets[0].PerChild)
	})
}

func TestSQLStoreMigratesPrePerChildRows(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		legacySchema := `CREATE TABLE budgets (
			scope TEXT NOT NULL DEFAULT 'user_path',
			subject TEXT NOT NULL,
			period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
			amount ` + sqlx.TypeFloat + ` NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			last_reset_at ` + sqlx.TypeInt64 + `,
			created_at ` + sqlx.TypeInt64 + ` NOT NULL,
			updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
			PRIMARY KEY (scope, subject, period_seconds)
		)`
		err := db.Schema(ctx, legacySchema)
		require.NoError(t, err)
		_, err = db.Exec(ctx, `
			INSERT INTO budgets (scope, subject, period_seconds, amount, source, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, ScopeUserPath, "/legacy", PeriodDailySeconds, 10.0, SourceManual, int64(1), int64(1))
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		budgets, err := store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, budgets, 1)
		require.False(t, budgets[0].PerChild)
	})
}

func TestSQLStoreReplaceConfigBudgetsRemovesStaleConfigRowsOnly(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		resetAt := time.Date(2026, time.April, 25, 9, 0, 0, 0, time.UTC)
		err := store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PerChild: true, PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceConfig},
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodWeeklySeconds, Amount: 50, Source: SourceConfig, LastResetAt: &resetAt},
			{Scope: ScopeUserPath, Subject: "/manual", PeriodSeconds: PeriodDailySeconds, Amount: 5, Source: SourceManual},
			{Scope: ScopeLabel, Subject: "prod", PeriodSeconds: PeriodDailySeconds, Amount: 20, Source: SourceConfig},
		})
		require.NoError(t, err)
		err = store.ReplaceConfigBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodWeeklySeconds, Amount: 75},
		})
		require.NoError(t, err)

		got, err := store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, got, 2)

		byKey := make(map[string]Budget, len(got))
		for _, budget := range got {
			byKey[budgetKey(budget.Scope, budget.Subject, budget.PeriodSeconds)] = budget
		}
		_, ok := byKey[budgetKey(ScopeUserPath, "/team", PeriodDailySeconds)]
		require.False(t, ok)
		_, ok = byKey[budgetKey(ScopeLabel, "prod", PeriodDailySeconds)]
		require.False(t, ok)

		weekly := byKey[budgetKey(ScopeUserPath, "/team", PeriodWeeklySeconds)]
		require.Equal(t, float64(75), weekly.Amount)
		require.Equal(t, SourceConfig, weekly.Source)
		require.NotNil(t, weekly.LastResetAt)
		require.True(t, weekly.LastResetAt.Equal(resetAt), "weekly last_reset_at = %v, want %s", weekly.LastResetAt, resetAt)
		_, ok = byKey[budgetKey(ScopeUserPath, "/manual", PeriodDailySeconds)]
		require.True(t, ok)
	})
}

func TestSQLStoreReplaceConfigBudgetsPreservesManualCollision(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
		})
		require.NoError(t, err)
		err = store.ReplaceConfigBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 99},
		})
		require.NoError(t, err)

		got, err := store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, SourceManual, got[0].Source)
		require.Equal(t, float64(10), got[0].Amount, "manual budget = %+v, want manual amount preserved", got[0])
	})
}

// A label and a user path can spell the same subject; the scope must keep them
// apart in storage.
func TestSQLStoreScopeSeparatesIdenticalSubjects(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/prod", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{Scope: ScopeLabel, Subject: "/prod", PeriodSeconds: PeriodDailySeconds, Amount: 20},
		})
		require.NoError(t, err)

		got, err := store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, got, 2)
		err = store.DeleteBudget(ctx, ScopeLabel, "/prod", PeriodDailySeconds)
		require.NoError(t, err)

		got, err = store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, ScopeUserPath, got[0].Scope)
	})
}

// A budgets table written before scopes existed must survive the upgrade with
// its rows intact, including one predating the source and last_reset_at
// columns.
func TestSQLStoreMigratesPreScopeTable(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, `CREATE TABLE budgets (
			user_path TEXT NOT NULL,
			period_seconds `+sqlx.TypeInt64+` NOT NULL,
			amount `+sqlx.TypeFloat+` NOT NULL,
			created_at `+sqlx.TypeInt64+` NOT NULL,
			updated_at `+sqlx.TypeInt64+` NOT NULL,
			PRIMARY KEY (user_path, period_seconds)
		)`)
		require.NoError(t, err)
		_, err = db.Exec(ctx,
			`INSERT INTO budgets (user_path, period_seconds, amount, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			"/team", PeriodDailySeconds, 10.0, int64(1), int64(2))
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		got, err := store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, ScopeUserPath, got[0].Scope)
		require.Equal(t, "/team", got[0].Subject)
		require.Equal(t, float64(10), got[0].Amount, "migrated budget = %+v, want user_path /team amount 10", got[0])
		require.Nil(t, got[0].LastResetAt)
		// A label budget must be storable afterwards: the old primary key would
		// have rejected a second row with the same subject and period.
		err = store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeLabel, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 1},
		})
		require.NoError(t, err)
	})
}

// TestSQLStoreSumSpendHonorsSubjectBoundaryAndCacheType covers both dialects:
// the label predicate is the one part of SumSpend written twice
// (json_each on SQLite, jsonb_exists on PostgreSQL), so testing only one of
// them would leave the other free to silently match nothing.
func TestSQLStoreSumSpendHonorsSubjectBoundaryAndCacheType(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)

		defer db.Close()

		usageStore, err := usage.NewSQLiteStore(db, 0)
		require.NoError(t, err)

		wrapped, err := sqlx.NewSQLite(db)
		require.NoError(t, err)

		assertSumSpendMatchesSubjects(t, usageStore, wrapped)
	})

	t.Run("postgresql", func(t *testing.T) {
		pool := sqlxtest.NewPostgresPool(t)
		if pool == nil {
			return // already skipped
		}
		usageStore, err := usage.NewPostgreSQLStore(pool, 0)
		require.NoError(t, err)

		wrapped, err := sqlx.NewPostgreSQL(pool)
		require.NoError(t, err)

		assertSumSpendMatchesSubjects(t, usageStore, wrapped)
	})
}

// usageWriter is the slice of a usage store this test needs, satisfied by both
// backend implementations.
type usageWriter interface {
	WriteBatch(ctx context.Context, entries []*usage.UsageEntry) error
}

func assertSumSpendMatchesSubjects(t *testing.T, usageStore usageWriter, db sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	store, err := NewSQLStore(ctx, db)
	require.NoError(t, err)

	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	entries := []*usage.UsageEntry{
		usageEntryWithCost("team-root", "/team", "", now, 0.25, "prod"),
		usageEntryWithCost("team-child", "/team/app", "", now, 0.75, "prod", "iOS"),
		usageEntryWithCost("sibling", "/team-alpha", "", now, 5, "iOS"),
		usageEntryWithCost("cached", "/team/cache", usage.CacheTypeExact, now, 10, "prod"),
		usageEntryWithCost("outside-window", "/team/app", "", now.Add(-48*time.Hour), 7, "prod"),
		usageEntryWithCost("unlabelled", "/team/plain", "", now, 0.5),
	}
	err = usageStore.WriteBatch(ctx, entries)
	require.NoError(t, err)

	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	windows := []SpendWindow{
		{Scope: ScopeUserPath, Subject: "/team", Start: start, End: end},
		{Scope: ScopeUserPath, Subject: "/missing", Start: start, End: end},
		{Scope: ScopeLabel, Subject: "prod", Start: start, End: end},
		{Scope: ScopeLabel, Subject: "iOS", Start: start, End: end},
		{Scope: ScopeLabel, Subject: "absent", Start: start, End: end},
		// Same label, but a window that excludes every entry.
		{Scope: ScopeLabel, Subject: "prod", Start: now.Add(-72 * time.Hour), End: now.Add(-60 * time.Hour)},
	}
	want := []Spend{
		{Total: 1.5, HasUsage: true},  // /team subtree, uncached, minus the sibling
		{},                            // no such path
		{Total: 1.0, HasUsage: true},  // prod: team-root + team-child, cached excluded
		{Total: 5.75, HasUsage: true}, // iOS: team-child + sibling
		{},                            // no such label
		{},                            // window with no entries
	}

	got, err := store.SumSpend(ctx, windows)
	require.NoError(t, err)
	require.Len(t, got, len(want))

	for i := range want {
		require.Equal(t, want[i].HasUsage, got[i].HasUsage)
		require.Equal(t, want[i].Total, got[i].Total, "spend[%d] (%s %s) = %+v, want %+v", i, windows[i].Scope, windows[i].Subject, got[i], want[i])
	}
}

// usageEntryUUID maps a readable entry name to a stable UUID.
func usageEntryUUID(name string) string {
	sum := sha256.Sum256([]byte(name))
	hex := hex.EncodeToString(sum[:16])
	return hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]
}

// The chunking that keeps a batch inside the SQLite parameter limit must not
// change the results or their order.
func TestSQLStoreSumSpendChunksLargeBatches(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	usageStore, err := usage.NewSQLiteStore(db, 0)
	require.NoError(t, err)

	wrapped, err := sqlx.NewSQLite(db)
	require.NoError(t, err)

	store, err := NewSQLStore(ctx, wrapped)
	require.NoError(t, err)

	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	err = usageStore.WriteBatch(ctx, []*usage.UsageEntry{
		usageEntryWithCost("only", "/team", "", now, 3, "prod"),
	})
	require.NoError(t, err)

	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	total := spendChunkSize*2 + 3
	windows := make([]SpendWindow, 0, total)
	for i := range total {
		if i == total-1 {
			windows = append(windows, SpendWindow{Scope: ScopeLabel, Subject: "prod", Start: start, End: end})
			continue
		}
		windows = append(windows, SpendWindow{Scope: ScopeLabel, Subject: "absent", Start: start, End: end})
	}

	got, err := store.SumSpend(ctx, windows)
	require.NoError(t, err)
	require.Equal(t, total, len(got))

	for i, spend := range got[:total-1] {
		require.False(t, spend.HasUsage, "spend[%d] = %+v, want no usage", i, spend)
	}
	last := got[total-1]
	require.True(t, last.HasUsage)
	require.Equal(t, float64(3), last.Total, "last spend = %+v, want 3 with usage — chunk boundaries must preserve order", last)
}

// usageEntryWithCost builds one priced usage row. PostgreSQL types usage.id as
// a UUID, so the readable name only labels the request and a fixed UUID keyed
// off it fills the primary key — no randomness, so runs stay reproducible.
func usageEntryWithCost(name, userPath, cacheType string, ts time.Time, cost float64, labels ...string) *usage.UsageEntry {
	inputCost := cost / 2
	outputCost := cost / 2
	totalCost := cost
	return &usage.UsageEntry{
		ID:           usageEntryUUID(name),
		RequestID:    name,
		ProviderID:   name,
		Timestamp:    ts,
		Model:        "gpt-4",
		Provider:     "test",
		ProviderName: "test",
		Endpoint:     "/v1/chat/completions",
		UserPath:     userPath,
		CacheType:    cacheType,
		Labels:       labels,
		InputTokens:  1,
		OutputTokens: 1,
		TotalTokens:  2,
		InputCost:    &inputCost,
		OutputCost:   &outputCost,
		TotalCost:    &totalCost,
	}
}
