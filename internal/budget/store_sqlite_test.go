package budget

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSQLiteStoreReplaceConfigBudgetsRemovesStaleConfigRowsOnly(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() failed: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() failed: %v", err)
	}
	resetAt := time.Date(2026, time.April, 25, 9, 0, 0, 0, time.UTC)
	if err := store.UpsertBudgets(ctx, []Budget{
		{UserPath: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: "config"},
		{UserPath: "/team", PeriodSeconds: PeriodWeeklySeconds, Amount: 50, Source: "config", LastResetAt: &resetAt},
		{UserPath: "/manual", PeriodSeconds: PeriodDailySeconds, Amount: 5, Source: "manual"},
	}); err != nil {
		t.Fatalf("UpsertBudgets() failed: %v", err)
	}

	if err := store.ReplaceConfigBudgets(ctx, []Budget{
		{UserPath: "/team", PeriodSeconds: PeriodWeeklySeconds, Amount: 75},
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
		byKey[budgetKey(budget.UserPath, budget.PeriodSeconds)] = budget
	}
	if _, ok := byKey[budgetKey("/team", PeriodDailySeconds)]; ok {
		t.Fatal("stale config daily budget was not removed")
	}
	weekly := byKey[budgetKey("/team", PeriodWeeklySeconds)]
	if weekly.Amount != 75 {
		t.Fatalf("weekly amount = %v, want 75", weekly.Amount)
	}
	if weekly.Source != "config" {
		t.Fatalf("weekly source = %q, want config", weekly.Source)
	}
	if weekly.LastResetAt == nil || !weekly.LastResetAt.Equal(resetAt) {
		t.Fatalf("weekly last_reset_at = %v, want %s", weekly.LastResetAt, resetAt)
	}
	if _, ok := byKey[budgetKey("/manual", PeriodDailySeconds)]; !ok {
		t.Fatal("manual budget was removed by config replacement")
	}
}
