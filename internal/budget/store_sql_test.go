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
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		body(t, store)
	})
}

func TestSQLStoreRoundTripsPerChild(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		if err := store.UpsertBudgets(ctx, []Budget{{
			Scope: ScopeUserPath, Subject: "/users", PerChild: true,
			PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual,
		}}); err != nil {
			t.Fatalf("UpsertBudgets() failed: %v", err)
		}
		budgets, err := store.ListBudgets(ctx)
		if err != nil {
			t.Fatalf("ListBudgets() failed: %v", err)
		}
		if len(budgets) != 1 || !budgets[0].PerChild {
			t.Fatalf("budgets = %+v, want one per-child budget", budgets)
		}
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
		if err := db.Schema(ctx, legacySchema); err != nil {
			t.Fatalf("create legacy budgets table: %v", err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO budgets (scope, subject, period_seconds, amount, source, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, ScopeUserPath, "/legacy", PeriodDailySeconds, 10.0, SourceManual, int64(1), int64(1)); err != nil {
			t.Fatalf("insert legacy budget: %v", err)
		}

		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore() failed: %v", err)
		}
		budgets, err := store.ListBudgets(ctx)
		if err != nil {
			t.Fatalf("ListBudgets() failed: %v", err)
		}
		if len(budgets) != 1 || budgets[0].PerChild {
			t.Fatalf("migrated budgets = %+v, want one shared legacy budget", budgets)
		}
	})
}

func TestSQLStoreReplaceConfigBudgetsRemovesStaleConfigRowsOnly(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		resetAt := time.Date(2026, time.April, 25, 9, 0, 0, 0, time.UTC)
		if err := store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PerChild: true, PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceConfig},
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodWeeklySeconds, Amount: 50, Source: SourceConfig, LastResetAt: &resetAt},
			{Scope: ScopeUserPath, Subject: "/manual", PeriodSeconds: PeriodDailySeconds, Amount: 5, Source: SourceManual},
			{Scope: ScopeLabel, Subject: "prod", PeriodSeconds: PeriodDailySeconds, Amount: 20, Source: SourceConfig},
		}); err != nil {
			t.Fatalf("UpsertBudgets() failed: %v", err)
		}

		if err := store.ReplaceConfigBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodWeeklySeconds, Amount: 75},
		}); err != nil {
			t.Fatalf("ReplaceConfigBudgets() failed: %v", err)
		}

		got, err := store.ListBudgets(ctx)
		if err != nil {
			t.Fatalf("ListBudgets() failed: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 budgets after replacement, got %d: %+v", len(got), got)
		}
		byKey := make(map[string]Budget, len(got))
		for _, budget := range got {
			byKey[budgetKey(budget.Scope, budget.Subject, budget.PeriodSeconds)] = budget
		}
		if _, ok := byKey[budgetKey(ScopeUserPath, "/team", PeriodDailySeconds)]; ok {
			t.Fatal("stale config daily budget was not removed")
		}
		if _, ok := byKey[budgetKey(ScopeLabel, "prod", PeriodDailySeconds)]; ok {
			t.Fatal("stale config label budget was not removed")
		}
		weekly := byKey[budgetKey(ScopeUserPath, "/team", PeriodWeeklySeconds)]
		if weekly.Amount != 75 {
			t.Fatalf("weekly amount = %v, want 75", weekly.Amount)
		}
		if weekly.Source != SourceConfig {
			t.Fatalf("weekly source = %q, want config", weekly.Source)
		}
		if weekly.LastResetAt == nil || !weekly.LastResetAt.Equal(resetAt) {
			t.Fatalf("weekly last_reset_at = %v, want %s", weekly.LastResetAt, resetAt)
		}
		if _, ok := byKey[budgetKey(ScopeUserPath, "/manual", PeriodDailySeconds)]; !ok {
			t.Fatal("manual budget was removed by config replacement")
		}
	})
}

func TestSQLStoreReplaceConfigBudgetsPreservesManualCollision(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		if err := store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
		}); err != nil {
			t.Fatalf("UpsertBudgets() failed: %v", err)
		}

		if err := store.ReplaceConfigBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 99},
		}); err != nil {
			t.Fatalf("ReplaceConfigBudgets() failed: %v", err)
		}

		got, err := store.ListBudgets(ctx)
		if err != nil {
			t.Fatalf("ListBudgets() failed: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 budget, got %d: %+v", len(got), got)
		}
		if got[0].Source != SourceManual || got[0].Amount != 10 {
			t.Fatalf("manual budget = %+v, want manual amount preserved", got[0])
		}
	})
}

// A label and a user path can spell the same subject; the scope must keep them
// apart in storage.
func TestSQLStoreScopeSeparatesIdenticalSubjects(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		if err := store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/prod", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{Scope: ScopeLabel, Subject: "/prod", PeriodSeconds: PeriodDailySeconds, Amount: 20},
		}); err != nil {
			t.Fatalf("UpsertBudgets() failed: %v", err)
		}

		got, err := store.ListBudgets(ctx)
		if err != nil {
			t.Fatalf("ListBudgets() failed: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 budgets, got %d: %+v", len(got), got)
		}

		if err := store.DeleteBudget(ctx, ScopeLabel, "/prod", PeriodDailySeconds); err != nil {
			t.Fatalf("DeleteBudget() failed: %v", err)
		}
		got, err = store.ListBudgets(ctx)
		if err != nil {
			t.Fatalf("ListBudgets() failed: %v", err)
		}
		if len(got) != 1 || got[0].Scope != ScopeUserPath {
			t.Fatalf("after deleting the label budget, got %+v, want only the user-path budget", got)
		}
	})
}

// A budgets table written before scopes existed must survive the upgrade with
// its rows intact, including one predating the source and last_reset_at
// columns.
func TestSQLStoreMigratesPreScopeTable(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		if err := db.Schema(ctx, `CREATE TABLE budgets (
			user_path TEXT NOT NULL,
			period_seconds `+sqlx.TypeInt64+` NOT NULL,
			amount `+sqlx.TypeFloat+` NOT NULL,
			created_at `+sqlx.TypeInt64+` NOT NULL,
			updated_at `+sqlx.TypeInt64+` NOT NULL,
			PRIMARY KEY (user_path, period_seconds)
		)`); err != nil {
			t.Fatalf("create pre-scope table: %v", err)
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO budgets (user_path, period_seconds, amount, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			"/team", PeriodDailySeconds, 10.0, int64(1), int64(2)); err != nil {
			t.Fatalf("seed pre-scope row: %v", err)
		}

		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore() failed: %v", err)
		}
		got, err := store.ListBudgets(ctx)
		if err != nil {
			t.Fatalf("ListBudgets() failed: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected the pre-scope row to survive, got %+v", got)
		}
		if got[0].Scope != ScopeUserPath || got[0].Subject != "/team" || got[0].Amount != 10 {
			t.Fatalf("migrated budget = %+v, want user_path /team amount 10", got[0])
		}
		if got[0].LastResetAt != nil {
			t.Fatalf("migrated last_reset_at = %v, want nil", got[0].LastResetAt)
		}

		// A label budget must be storable afterwards: the old primary key would
		// have rejected a second row with the same subject and period.
		if err := store.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeLabel, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 1},
		}); err != nil {
			t.Fatalf("UpsertBudgets() after migration failed: %v", err)
		}
	})
}

// TestSQLStoreSumSpendHonorsSubjectBoundaryAndCacheType covers both dialects:
// the label predicate is the one part of SumSpend written twice
// (json_each on SQLite, jsonb_exists on PostgreSQL), so testing only one of
// them would leave the other free to silently match nothing.
func TestSQLStoreSumSpendHonorsSubjectBoundaryAndCacheType(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("sql.Open() failed: %v", err)
		}
		defer db.Close()

		usageStore, err := usage.NewSQLiteStore(db, 0)
		if err != nil {
			t.Fatalf("NewSQLiteStore() for usage failed: %v", err)
		}
		wrapped, err := sqlx.NewSQLite(db)
		if err != nil {
			t.Fatalf("sqlx.NewSQLite() failed: %v", err)
		}
		assertSumSpendMatchesSubjects(t, usageStore, wrapped)
	})

	t.Run("postgresql", func(t *testing.T) {
		pool := sqlxtest.NewPostgresPool(t)
		if pool == nil {
			return // already skipped
		}
		usageStore, err := usage.NewPostgreSQLStore(pool, 0)
		if err != nil {
			t.Fatalf("NewPostgreSQLStore() for usage failed: %v", err)
		}
		wrapped, err := sqlx.NewPostgreSQL(pool)
		if err != nil {
			t.Fatalf("sqlx.NewPostgreSQL() failed: %v", err)
		}
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
	if err != nil {
		t.Fatalf("NewSQLStore() failed: %v", err)
	}

	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	entries := []*usage.UsageEntry{
		usageEntryWithCost("team-root", "/team", "", now, 0.25, "prod"),
		usageEntryWithCost("team-child", "/team/app", "", now, 0.75, "prod", "iOS"),
		usageEntryWithCost("sibling", "/team-alpha", "", now, 5, "iOS"),
		usageEntryWithCost("cached", "/team/cache", usage.CacheTypeExact, now, 10, "prod"),
		usageEntryWithCost("outside-window", "/team/app", "", now.Add(-48*time.Hour), 7, "prod"),
		usageEntryWithCost("unlabelled", "/team/plain", "", now, 0.5),
	}
	if err := usageStore.WriteBatch(ctx, entries); err != nil {
		t.Fatalf("WriteBatch() failed: %v", err)
	}

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
	if err != nil {
		t.Fatalf("SumSpend() failed: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("SumSpend() returned %d spends, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].HasUsage != want[i].HasUsage || got[i].Total != want[i].Total {
			t.Fatalf("spend[%d] (%s %s) = %+v, want %+v",
				i, windows[i].Scope, windows[i].Subject, got[i], want[i])
		}
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
	if err != nil {
		t.Fatalf("sql.Open() failed: %v", err)
	}
	defer db.Close()

	usageStore, err := usage.NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("NewSQLiteStore() for usage failed: %v", err)
	}
	wrapped, err := sqlx.NewSQLite(db)
	if err != nil {
		t.Fatalf("sqlx.NewSQLite() failed: %v", err)
	}
	store, err := NewSQLStore(ctx, wrapped)
	if err != nil {
		t.Fatalf("NewSQLStore() failed: %v", err)
	}

	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	if err := usageStore.WriteBatch(ctx, []*usage.UsageEntry{
		usageEntryWithCost("only", "/team", "", now, 3, "prod"),
	}); err != nil {
		t.Fatalf("WriteBatch() failed: %v", err)
	}

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
	if err != nil {
		t.Fatalf("SumSpend() failed: %v", err)
	}
	if len(got) != total {
		t.Fatalf("SumSpend() returned %d spends, want %d", len(got), total)
	}
	for i, spend := range got[:total-1] {
		if spend.HasUsage {
			t.Fatalf("spend[%d] = %+v, want no usage", i, spend)
		}
	}
	if last := got[total-1]; !last.HasUsage || last.Total != 3 {
		t.Fatalf("last spend = %+v, want 3 with usage — chunk boundaries must preserve order", last)
	}
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
