package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
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

func TestSQLStoreRoundTripsNullableLimits(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		if err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PerChild: true, PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(100)), MaxTokens: new(int64(5000)), Source: SourceManual},
			{Subject: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: new(int64(1000)), Source: SourceManual},
			{Subject: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: new(int64(10)), Source: SourceManual},
			{Subject: "/tokens-only", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(100)), Source: SourceManual},
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
		if !minute.PerChild {
			t.Fatal("minute rule per_child = false, want true")
		}
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
	})
}

func TestSQLStoreDeleteRule(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		if err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: new(int64(10)), Source: SourceManual},
		}); err != nil {
			t.Fatalf("UpsertRules() failed: %v", err)
		}
		if err := store.DeleteRule(ctx, ScopeUserPath, "/team", PeriodConcurrent); err != nil {
			t.Fatalf("DeleteRule() failed: %v", err)
		}
		if err := store.DeleteRule(ctx, ScopeUserPath, "/team", PeriodConcurrent); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DeleteRule() error = %v, want ErrNotFound", err)
		}
	})
}

func TestSQLStoreReplaceConfigRulesRemovesStaleConfigRowsOnly(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		if err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(10)), Source: SourceConfig},
			{Subject: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: new(int64(50)), Source: SourceConfig},
			{Subject: "/manual", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(5)), Source: SourceManual},
		}); err != nil {
			t.Fatalf("UpsertRules() failed: %v", err)
		}

		if err := store.ReplaceConfigRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: new(int64(75))},
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
	})
}

func TestSQLStoreReplaceConfigRulesPreservesManualCollision(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		if err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(10)), Source: SourceManual},
		}); err != nil {
			t.Fatalf("UpsertRules() failed: %v", err)
		}
		if err := store.ReplaceConfigRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(99))},
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
	})
}

func TestSQLStoreManualUpsertOverridesConfigRow(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		if err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(10)), Source: SourceConfig},
		}); err != nil {
			t.Fatalf("UpsertRules() failed: %v", err)
		}
		if err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(25)), Source: SourceManual},
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
	})
}

// TestSQLStoreMigratesPreScopeTable starts from the pre-scope table shape,
// keyed by user_path only. The PostgreSQL rebuild had no test before this.
func TestSQLStoreMigratesPreScopeTable(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		if err := db.Schema(ctx, `
			CREATE TABLE rate_limits (
				user_path TEXT NOT NULL,
				period_seconds `+sqlx.TypeInt64+` NOT NULL,
				max_requests `+sqlx.TypeInt64+`,
				max_tokens `+sqlx.TypeInt64+`,
				source TEXT NOT NULL DEFAULT '',
				created_at `+sqlx.TypeInt64+` NOT NULL,
				updated_at `+sqlx.TypeInt64+` NOT NULL,
				PRIMARY KEY (user_path, period_seconds)
			)`); err != nil {
			t.Fatalf("create pre-scope table: %v", err)
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO rate_limits (user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"/team", PeriodMinuteSeconds, 100, 5000, SourceManual, 1700000000, 1700000000,
		); err != nil {
			t.Fatalf("seed pre-scope row: %v", err)
		}

		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore() failed: %v", err)
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
		if migrated.MaxRequests == nil || *migrated.MaxRequests != 100 ||
			migrated.MaxTokens == nil || *migrated.MaxTokens != 5000 {
			t.Fatalf("migrated limits = %+v, want 100/5000 preserved", migrated)
		}
		if migrated.Source != SourceManual {
			t.Fatalf("migrated source = %q, want manual", migrated.Source)
		}

		// Re-opening the store must be a no-op, and scoped writes must work.
		if _, err := NewSQLStore(ctx, db); err != nil {
			t.Fatalf("NewSQLStore() second open failed: %v", err)
		}
		if err := store.UpsertRules(ctx, []Rule{
			{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodMinuteSeconds,
				MaxRequests: new(int64(500)), Source: SourceManual},
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
	})
}

func TestSQLStoreCounterRoundTrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		runCounterStoreSuite(t, store, func(t *testing.T, snap WindowSnapshot, updatedAt int64) {
			t.Helper()
			if _, err := store.db.Exec(context.Background(), upsertCounterSQL,
				snap.Scope, snap.Subject, snap.Partition, snap.PeriodSeconds,
				snap.RequestsWindowStart, snap.RequestsCurrent, snap.RequestsPrevious,
				snap.TokensWindowStart, snap.TokensCurrent, snap.TokensPrevious, updatedAt,
			); err != nil {
				t.Fatalf("seed stale counter: %v", err)
			}
		})
	})
}

// TestSQLStoreLoadCountersSkipsMalformedRow keeps one unreadable row from
// costing every other window its restore: Start treats a load error as "do not
// persist this generation". SQLite only — its dynamic typing is what lets a
// row hold a value the scan cannot read.
func TestSQLStoreLoadCountersSkipsMalformedRow(t *testing.T) {
	ctx := context.Background()
	db := sqlxtest.NewSQLite(t)
	store, err := NewSQLStore(ctx, db)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	good := WindowSnapshot{
		Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		RequestsWindowStart: 1700000000, RequestsCurrent: 2,
	}
	if err := store.SaveCounters(ctx, []WindowSnapshot{good}); err != nil {
		t.Fatalf("SaveCounters: %v", err)
	}
	if _, err := db.Exec(ctx, upsertCounterSQL,
		string(ScopeUserPath), "/broken", "", PeriodHourSeconds,
		0, "not-a-number", 0, 0, 0, 0, time.Now().Unix(),
	); err != nil {
		t.Fatalf("seed malformed counter: %v", err)
	}

	got, err := store.LoadCounters(ctx)
	if err != nil {
		t.Fatalf("LoadCounters: %v", err)
	}
	if len(got) != 1 || got[0] != good {
		t.Fatalf("loaded = %+v, want only the readable window %+v", got, good)
	}
}
