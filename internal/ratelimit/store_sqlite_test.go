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
		{UserPath: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(100), MaxTokens: int64Ptr(5000), Source: SourceManual},
		{UserPath: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: int64Ptr(1000), Source: SourceManual},
		{UserPath: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: int64Ptr(10), Source: SourceManual},
		{UserPath: "/tokens-only", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: int64Ptr(100), Source: SourceManual},
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
		byKey[ruleStoreKey(rule.UserPath, rule.PeriodSeconds)] = rule
	}
	minute := byKey[ruleStoreKey("/team", PeriodMinuteSeconds)]
	if minute.MaxRequests == nil || *minute.MaxRequests != 100 || minute.MaxTokens == nil || *minute.MaxTokens != 5000 {
		t.Fatalf("minute rule = %+v, want 100 requests / 5000 tokens", minute)
	}
	day := byKey[ruleStoreKey("/team", PeriodDaySeconds)]
	if day.MaxTokens != nil {
		t.Fatalf("day rule max_tokens = %v, want nil", *day.MaxTokens)
	}
	tokensOnly := byKey[ruleStoreKey("/tokens-only", PeriodMinuteSeconds)]
	if tokensOnly.MaxRequests != nil {
		t.Fatalf("tokens-only rule max_requests = %v, want nil", *tokensOnly.MaxRequests)
	}
	concurrent := byKey[ruleStoreKey("/team", PeriodConcurrent)]
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
		{UserPath: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: int64Ptr(10), Source: SourceManual},
	}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}
	if err := store.DeleteRule(ctx, "/team", PeriodConcurrent); err != nil {
		t.Fatalf("DeleteRule() failed: %v", err)
	}
	if err := store.DeleteRule(ctx, "/team", PeriodConcurrent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteRule() error = %v, want ErrNotFound", err)
	}
}

func TestSQLiteStoreReplaceConfigRulesRemovesStaleConfigRowsOnly(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)

	if err := store.UpsertRules(ctx, []Rule{
		{UserPath: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(10), Source: SourceConfig},
		{UserPath: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: int64Ptr(50), Source: SourceConfig},
		{UserPath: "/manual", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(5), Source: SourceManual},
	}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}

	if err := store.ReplaceConfigRules(ctx, []Rule{
		{UserPath: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: int64Ptr(75)},
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
		byKey[ruleStoreKey(rule.UserPath, rule.PeriodSeconds)] = rule
	}
	if _, ok := byKey[ruleStoreKey("/team", PeriodMinuteSeconds)]; ok {
		t.Fatal("stale config minute rule was not removed")
	}
	day := byKey[ruleStoreKey("/team", PeriodDaySeconds)]
	if day.MaxRequests == nil || *day.MaxRequests != 75 || day.Source != SourceConfig {
		t.Fatalf("day rule = %+v, want config 75", day)
	}
	if _, ok := byKey[ruleStoreKey("/manual", PeriodMinuteSeconds)]; !ok {
		t.Fatal("manual rule was removed by config replacement")
	}
}

func TestSQLiteStoreReplaceConfigRulesPreservesManualCollision(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)

	if err := store.UpsertRules(ctx, []Rule{
		{UserPath: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(10), Source: SourceManual},
	}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}
	if err := store.ReplaceConfigRules(ctx, []Rule{
		{UserPath: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(99)},
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
		{UserPath: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(10), Source: SourceConfig},
	}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}
	if err := store.UpsertRules(ctx, []Rule{
		{UserPath: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(25), Source: SourceManual},
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
