package ratelimit

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func newSQLiteTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() failed: %v", err)
	}
	// ":memory:" is connection-local; without pinning, a second pooled
	// connection would see a different empty database.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() failed: %v", err)
	}
	return store
}

func TestSQLiteStoreRoundTripsNullableLimits(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)

	if err := store.UpsertRules(ctx, []Rule{
		{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(100), MaxTokens: int64Ptr(5000), Source: SourceManual},
		{Subject: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: int64Ptr(1000), Source: SourceManual},
		{Subject: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: int64Ptr(10), Source: SourceManual},
		{Subject: "/tokens-only", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: int64Ptr(100), Source: SourceManual},
	}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}

	rules, err := store.ListRules(ctx)
	if err != nil {
		t.Fatalf("ListRules() failed: %v", err)
	}
	if len(rules) != 4 {
		t.Fatalf("rules = %d, want 4", len(rules))
	}
	byKey := make(map[string]Rule, len(rules))
	for _, rule := range rules {
		byKey[ruleStoreKey(rule.Scope, rule.Subject, rule.PeriodSeconds)] = rule
	}
	minute := byKey[ruleStoreKey(ScopeUserPath, "/team", PeriodMinuteSeconds)]
	if minute.MaxRequests == nil || *minute.MaxRequests != 100 || minute.MaxTokens == nil || *minute.MaxTokens != 5000 {
		t.Fatalf("minute rule = %+v, want 100 requests / 5000 tokens", minute)
	}
	day := byKey[ruleStoreKey(ScopeUserPath, "/team", PeriodDaySeconds)]
	if day.MaxTokens != nil {
		t.Fatalf("day rule max_tokens = %v, want nil", *day.MaxTokens)
	}
	tokensOnly := byKey[ruleStoreKey(ScopeUserPath, "/tokens-only", PeriodMinuteSeconds)]
	if tokensOnly.MaxRequests != nil {
		t.Fatalf("tokens-only rule max_requests = %v, want nil", *tokensOnly.MaxRequests)
	}
	concurrent := byKey[ruleStoreKey(ScopeUserPath, "/team", PeriodConcurrent)]
	if concurrent.MaxRequests == nil || *concurrent.MaxRequests != 10 {
		t.Fatalf("concurrent rule = %+v, want 10 in-flight", concurrent)
	}
	if concurrent.CreatedAt.IsZero() || concurrent.UpdatedAt.IsZero() {
		t.Fatal("timestamps not persisted")
	}
}

func TestSQLiteStoreDeleteRule(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)

	if err := store.UpsertRules(ctx, []Rule{
		{Subject: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: int64Ptr(10), Source: SourceManual},
	}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}
	if err := store.DeleteRule(ctx, ScopeUserPath, "/team", PeriodConcurrent); err != nil {
		t.Fatalf("DeleteRule() failed: %v", err)
	}
	if err := store.DeleteRule(ctx, ScopeUserPath, "/team", PeriodConcurrent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteRule() error = %v, want ErrNotFound", err)
	}
}

func TestSQLiteStoreReplaceConfigRulesRemovesStaleConfigRowsOnly(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)

	if err := store.UpsertRules(ctx, []Rule{
		{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(10), Source: SourceConfig},
		{Subject: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: int64Ptr(50), Source: SourceConfig},
		{Subject: "/manual", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(5), Source: SourceManual},
	}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}

	if err := store.ReplaceConfigRules(ctx, []Rule{
		{Subject: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: int64Ptr(75)},
	}); err != nil {
		t.Fatalf("ReplaceConfigRules() failed: %v", err)
	}

	rules, err := store.ListRules(ctx)
	if err != nil {
		t.Fatalf("ListRules() failed: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2: %+v", len(rules), rules)
	}
	byKey := make(map[string]Rule, len(rules))
	for _, rule := range rules {
		byKey[ruleStoreKey(rule.Scope, rule.Subject, rule.PeriodSeconds)] = rule
	}
	if _, ok := byKey[ruleStoreKey(ScopeUserPath, "/team", PeriodMinuteSeconds)]; ok {
		t.Fatal("stale config minute rule was not removed")
	}
	day := byKey[ruleStoreKey(ScopeUserPath, "/team", PeriodDaySeconds)]
	if day.MaxRequests == nil || *day.MaxRequests != 75 || day.Source != SourceConfig {
		t.Fatalf("day rule = %+v, want config 75", day)
	}
	if _, ok := byKey[ruleStoreKey(ScopeUserPath, "/manual", PeriodMinuteSeconds)]; !ok {
		t.Fatal("manual rule was removed by config replacement")
	}
}

func TestSQLiteStoreReplaceConfigRulesPreservesManualCollision(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)

	if err := store.UpsertRules(ctx, []Rule{
		{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(10), Source: SourceManual},
	}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}
	if err := store.ReplaceConfigRules(ctx, []Rule{
		{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(99)},
	}); err != nil {
		t.Fatalf("ReplaceConfigRules() failed: %v", err)
	}

	rules, err := store.ListRules(ctx)
	if err != nil {
		t.Fatalf("ListRules() failed: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %d, want 1: %+v", len(rules), rules)
	}
	if rules[0].Source != SourceManual || *rules[0].MaxRequests != 10 {
		t.Fatalf("rule = %+v, want manual limits preserved", rules[0])
	}
}

func TestSQLiteStoreManualUpsertOverridesConfigRow(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)

	if err := store.UpsertRules(ctx, []Rule{
		{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(10), Source: SourceConfig},
	}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}
	if err := store.UpsertRules(ctx, []Rule{
		{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(25), Source: SourceManual},
	}); err != nil {
		t.Fatalf("manual UpsertRules() failed: %v", err)
	}

	rules, err := store.ListRules(ctx)
	if err != nil {
		t.Fatalf("ListRules() failed: %v", err)
	}
	if len(rules) != 1 || rules[0].Source != SourceManual || *rules[0].MaxRequests != 25 {
		t.Fatalf("rule = %+v, want manual override", rules[0])
	}
}

func TestSQLiteStoreMigratesPreScopeTable(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() failed: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	// The pre-scope shape: keyed by user_path only.
	if _, err := db.Exec(`
		CREATE TABLE rate_limits (
			user_path TEXT NOT NULL,
			period_seconds INTEGER NOT NULL,
			max_requests INTEGER,
			max_tokens INTEGER,
			source TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (user_path, period_seconds)
		)
	`); err != nil {
		t.Fatalf("create pre-scope table: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO rate_limits (user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"/team", PeriodMinuteSeconds, 100, 5000, SourceManual, 1700000000, 1700000000,
	); err != nil {
		t.Fatalf("seed pre-scope row: %v", err)
	}

	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() failed: %v", err)
	}
	rules, err := store.ListRules(ctx)
	if err != nil {
		t.Fatalf("ListRules() failed: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(rules))
	}
	migrated := rules[0]
	if migrated.Scope != ScopeUserPath || migrated.Subject != "/team" {
		t.Fatalf("migrated rule = %+v, want user_path /team", migrated)
	}
	if migrated.MaxRequests == nil || *migrated.MaxRequests != 100 || migrated.MaxTokens == nil || *migrated.MaxTokens != 5000 {
		t.Fatalf("migrated limits = %+v, want 100/5000 preserved", migrated)
	}
	if migrated.Source != SourceManual {
		t.Fatalf("migrated source = %q, want manual", migrated.Source)
	}

	// Re-opening the store must be a no-op, and scoped writes must work.
	if _, err := NewSQLiteStore(db); err != nil {
		t.Fatalf("NewSQLiteStore() second open failed: %v", err)
	}
	if err := store.UpsertRules(ctx, []Rule{
		{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(500), Source: SourceManual},
	}); err != nil {
		t.Fatalf("UpsertRules() after migration failed: %v", err)
	}
	rules, err = store.ListRules(ctx)
	if err != nil {
		t.Fatalf("ListRules() failed: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(rules))
	}
}
