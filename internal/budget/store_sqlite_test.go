package budget

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"gomodel/internal/usage"
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
		{UserPath: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceConfig},
		{UserPath: "/team", PeriodSeconds: PeriodWeeklySeconds, Amount: 50, Source: SourceConfig, LastResetAt: &resetAt},
		{UserPath: "/manual", PeriodSeconds: PeriodDailySeconds, Amount: 5, Source: SourceManual},
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
	if weekly.Source != SourceConfig {
		t.Fatalf("weekly source = %q, want config", weekly.Source)
	}
	if weekly.LastResetAt == nil || !weekly.LastResetAt.Equal(resetAt) {
		t.Fatalf("weekly last_reset_at = %v, want %s", weekly.LastResetAt, resetAt)
	}
	if _, ok := byKey[budgetKey("/manual", PeriodDailySeconds)]; !ok {
		t.Fatal("manual budget was removed by config replacement")
	}
}

func TestSQLiteStoreReplaceConfigBudgetsPreservesManualCollision(t *testing.T) {
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
	if err := store.UpsertBudgets(ctx, []Budget{
		{UserPath: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
	}); err != nil {
		t.Fatalf("UpsertBudgets() failed: %v", err)
	}

	if err := store.ReplaceConfigBudgets(ctx, []Budget{
		{UserPath: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 99},
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
}

func TestSQLiteStoreSumUsageCostHonorsUserPathBoundaryAndCacheType(t *testing.T) {
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
	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() failed: %v", err)
	}

	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	entries := []*usage.UsageEntry{
		usageEntryWithCost("team-root", "/team", "", now, 0.25),
		usageEntryWithCost("team-child", "/team/app", "", now, 0.75),
		usageEntryWithCost("sibling", "/team-alpha", "", now, 5),
		usageEntryWithCost("cached", "/team/cache", usage.CacheTypeExact, now, 10),
		usageEntryWithCost("outside-window", "/team/app", "", now.Add(-48*time.Hour), 7),
	}
	if err := usageStore.WriteBatch(ctx, entries); err != nil {
		t.Fatalf("WriteBatch() failed: %v", err)
	}

	got, hasUsage, err := store.SumUsageCost(ctx, "/team", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("SumUsageCost() failed: %v", err)
	}
	if !hasUsage {
		t.Fatal("SumUsageCost() hasUsage = false, want true")
	}
	if got != 1.0 {
		t.Fatalf("SumUsageCost() = %v, want 1.0", got)
	}

	got, hasUsage, err = store.SumUsageCost(ctx, "/missing", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("SumUsageCost() for missing path failed: %v", err)
	}
	if hasUsage || got != 0 {
		t.Fatalf("missing path sum = %v/%v, want 0/false", got, hasUsage)
	}
}

func usageEntryWithCost(id, userPath, cacheType string, ts time.Time, cost float64) *usage.UsageEntry {
	inputCost := cost / 2
	outputCost := cost / 2
	totalCost := cost
	return &usage.UsageEntry{
		ID:           id,
		RequestID:    id,
		ProviderID:   id,
		Timestamp:    ts,
		Model:        "gpt-4",
		Provider:     "test",
		ProviderName: "test",
		Endpoint:     "/v1/chat/completions",
		UserPath:     userPath,
		CacheType:    cacheType,
		InputTokens:  1,
		OutputTokens: 1,
		TotalTokens:  2,
		InputCost:    &inputCost,
		OutputCost:   &outputCost,
		TotalCost:    &totalCost,
	}
}
