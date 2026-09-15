package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
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

func TestSQLStoreRoundTripsNullableLimits(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PerChild: true, PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(100)), MaxTokens: new(int64(5000)), Source: SourceManual},
			{Subject: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: new(int64(1000)), Source: SourceManual},
			{Subject: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: new(int64(10)), Source: SourceManual},
			{Subject: "/tokens-only", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(100)), Source: SourceManual},
		})
		require.NoError(t, err)

		rules, err := store.ListRules(ctx)
		require.NoError(t, err)
		require.Len(t, rules, 4)

		byKey := make(map[string]Rule, len(rules))
		for _, rule := range rules {
			byKey[ruleStoreKey(rule.Scope, rule.Subject, rule.PeriodSeconds)] = rule
		}
		minute := byKey[ruleStoreKey(ScopeUserPath, "/team", PeriodMinuteSeconds)]
		require.True(t, minute.PerChild)
		require.NotNil(t, minute.MaxRequests)
		require.Equal(t, int64(100), *minute.MaxRequests)
		require.NotNil(t, minute.MaxTokens)
		require.Equal(t, int64(5000), *minute.MaxTokens, "minute rule = %+v, want 100 requests / 5000 tokens", minute)

		day := byKey[ruleStoreKey(ScopeUserPath, "/team", PeriodDaySeconds)]
		require.Nil(t, day.MaxTokens)

		tokensOnly := byKey[ruleStoreKey(ScopeUserPath, "/tokens-only", PeriodMinuteSeconds)]
		require.Nil(t, tokensOnly.MaxRequests)

		concurrent := byKey[ruleStoreKey(ScopeUserPath, "/team", PeriodConcurrent)]
		require.NotNil(t, concurrent.MaxRequests)
		require.Equal(t, int64(10), *concurrent.MaxRequests, "concurrent rule = %+v, want 10 in-flight", concurrent)
		require.False(t, concurrent.CreatedAt.IsZero())
		require.False(t, concurrent.UpdatedAt.IsZero())
	})
}

func TestSQLStoreDeleteRule(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: new(int64(10)), Source: SourceManual},
		})
		require.NoError(t, err)
		err = store.DeleteRule(ctx, ScopeUserPath, "/team", PeriodConcurrent)
		require.NoError(t, err)
		err = store.DeleteRule(ctx, ScopeUserPath, "/team", PeriodConcurrent)
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestSQLStoreReplaceConfigRulesRemovesStaleConfigRowsOnly(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(10)), Source: SourceConfig},
			{Subject: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: new(int64(50)), Source: SourceConfig},
			{Subject: "/manual", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(5)), Source: SourceManual},
		})
		require.NoError(t, err)
		err = store.ReplaceConfigRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodDaySeconds, MaxRequests: new(int64(75))},
		})
		require.NoError(t, err)

		rules, err := store.ListRules(ctx)
		require.NoError(t, err)
		require.Len(t, rules, 2)

		byKey := make(map[string]Rule, len(rules))
		for _, rule := range rules {
			byKey[ruleStoreKey(rule.Scope, rule.Subject, rule.PeriodSeconds)] = rule
		}
		_, ok := byKey[ruleStoreKey(ScopeUserPath, "/team", PeriodMinuteSeconds)]
		require.False(t, ok)

		day := byKey[ruleStoreKey(ScopeUserPath, "/team", PeriodDaySeconds)]
		require.NotNil(t, day.MaxRequests)
		require.Equal(t, int64(75), *day.MaxRequests)
		require.Equal(t, SourceConfig, day.Source, "day rule = %+v, want config 75", day)
		_, ok = byKey[ruleStoreKey(ScopeUserPath, "/manual", PeriodMinuteSeconds)]
		require.True(t, ok)
	})
}

func TestSQLStoreReplaceConfigRulesPreservesManualCollision(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(10)), Source: SourceManual},
		})
		require.NoError(t, err)
		err = store.ReplaceConfigRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(99))},
		})
		require.NoError(t, err)

		rules, err := store.ListRules(ctx)
		require.NoError(t, err)
		require.Len(t, rules, 1)
		require.Equal(t, SourceManual, rules[0].Source)
		require.Equal(t, int64(10), *rules[0].MaxRequests, "rule = %+v, want manual limits preserved", rules[0])
	})
}

func TestSQLStoreManualUpsertOverridesConfigRow(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(10)), Source: SourceConfig},
		})
		require.NoError(t, err)
		err = store.UpsertRules(ctx, []Rule{
			{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(25)), Source: SourceManual},
		})
		require.NoError(t, err)

		rules, err := store.ListRules(ctx)
		require.NoError(t, err)
		require.Len(t, rules, 1)
		require.Equal(t, SourceManual, rules[0].Source)
		require.Equal(t, int64(25), *rules[0].MaxRequests)
	})
}

// TestSQLStoreMigratesPreScopeTable starts from the pre-scope table shape,
// keyed by user_path only. The PostgreSQL rebuild had no test before this.
func TestSQLStoreMigratesPreScopeTable(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, `
			CREATE TABLE rate_limits (
				user_path TEXT NOT NULL,
				period_seconds `+sqlx.TypeInt64+` NOT NULL,
				max_requests `+sqlx.TypeInt64+`,
				max_tokens `+sqlx.TypeInt64+`,
				source TEXT NOT NULL DEFAULT '',
				created_at `+sqlx.TypeInt64+` NOT NULL,
				updated_at `+sqlx.TypeInt64+` NOT NULL,
				PRIMARY KEY (user_path, period_seconds)
			)`)
		require.NoError(t, err)
		_, err = db.Exec(ctx,
			`INSERT INTO rate_limits (user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"/team", PeriodMinuteSeconds, 100, 5000, SourceManual, 1700000000, 1700000000,
		)
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		rules, err := store.ListRules(ctx)
		require.NoError(t, err)
		require.Len(t, rules, 1)

		migrated := rules[0]
		require.Equal(t, ScopeUserPath, migrated.Scope)
		require.Equal(t, "/team", migrated.Subject, "migrated rule = %+v, want user_path /team", migrated)
		require.NotNil(t, migrated.MaxRequests)
		require.Equal(t, int64(100), *migrated.MaxRequests)
		require.NotNil(t, migrated.MaxTokens)
		require.Equal(t, int64(5000), *migrated.MaxTokens, "migrated limits = %+v, want 100/5000 preserved", migrated)
		require.Equal(t, SourceManual, migrated.Source)
		// Re-opening the store must be a no-op, and scoped writes must work.
		_, err = NewSQLStore(ctx, db)
		require.NoError(t, err)
		err = store.UpsertRules(ctx, []Rule{
			{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodMinuteSeconds,
				MaxRequests: new(int64(500)), Source: SourceManual},
		})
		require.NoError(t, err)

		rules, err = store.ListRules(ctx)
		require.NoError(t, err)
		require.Len(t, rules, 2)
	})
}

func TestSQLStoreCounterRoundTrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		runCounterStoreSuite(t, store, func(t *testing.T, snap WindowSnapshot, updatedAt int64) {
			t.Helper()
			_, err := store.db.Exec(context.Background(), upsertCounterSQL,
				snap.Scope, snap.Subject, snap.Partition, snap.PeriodSeconds,
				snap.RequestsWindowStart, snap.RequestsCurrent, snap.RequestsPrevious,
				snap.TokensWindowStart, snap.TokensCurrent, snap.TokensPrevious, updatedAt,
			)
			require.NoError(t, err)
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
	require.NoError(t, err)

	good := WindowSnapshot{
		Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		RequestsWindowStart: 1700000000, RequestsCurrent: 2,
	}
	err = store.SaveCounters(ctx, []WindowSnapshot{good})
	require.NoError(t, err)
	_, err = db.Exec(ctx, upsertCounterSQL,
		string(ScopeUserPath), "/broken", "", PeriodHourSeconds,
		0, "not-a-number", 0, 0, 0, 0, time.Now().Unix(),
	)
	require.NoError(t, err)

	got, err := store.LoadCounters(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, good, got[0])
}
