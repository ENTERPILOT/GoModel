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

// The folded subject stays the key; the written spelling survives the round
// trip so breaches and listings can name a provider that exists.
func TestSQLStoreRoundTripsSubjectDisplay(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		err := store.UpsertRules(ctx, []Rule{
			{Scope: ScopeProvider, Subject: "mockA", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1)), Source: SourceManual},
		})
		require.NoError(t, err)

		rules, err := store.ListRules(ctx)
		require.NoError(t, err)
		require.Len(t, rules, 1)
		require.Equal(t, "mocka", rules[0].Subject, "subject must be the folded match key")
		require.Equal(t, "mockA", rules[0].DisplaySubject())
	})
}

// subject_display follows the same source precedence as the limits: a config
// re-seed may not re-spell a subject an operator edited by hand.
func TestSQLStoreSubjectDisplayPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		storedSpell string
		storedSrc   string
		nextSpell   string
		nextSrc     string
		want        string
	}{
		{name: "config over config", storedSpell: "mockA", storedSrc: SourceConfig, nextSpell: "MOCKA", nextSrc: SourceConfig, want: "MOCKA"},
		{name: "config over manual", storedSpell: "mockA", storedSrc: SourceManual, nextSpell: "MOCKA", nextSrc: SourceConfig, want: "mockA"},
		{name: "manual over config", storedSpell: "mockA", storedSrc: SourceConfig, nextSpell: "MOCKA", nextSrc: SourceManual, want: "MOCKA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
				ctx := context.Background()

				err := store.UpsertRules(ctx, []Rule{
					{Scope: ScopeProvider, Subject: tt.storedSpell, PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1)), Source: tt.storedSrc},
				})
				require.NoError(t, err)

				err = store.UpsertRules(ctx, []Rule{
					{Scope: ScopeProvider, Subject: tt.nextSpell, PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(2)), Source: tt.nextSrc},
				})
				require.NoError(t, err, "second UpsertRules")

				rules, err := store.ListRules(ctx)
				require.NoError(t, err)
				require.Len(t, rules, 1, "rules = %+v", rules)
				require.Equal(t, "mocka", rules[0].Subject, "subject must be the folded match key")
				require.Equal(t, tt.want, rules[0].DisplaySubject())
			})
		})
	}
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
				snap.Scope, snap.Subject, snap.Partition, snap.PeriodSeconds, snap.Writer, snap.Epoch,
				snap.RequestsWindowStart, snap.RequestsCurrent, snap.RequestsPrevious,
				snap.TokensWindowStart, snap.TokensCurrent, snap.TokensPrevious, snap.InFlight, updatedAt,
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
		string(ScopeUserPath), "/broken", "", PeriodHourSeconds, "w1", "",
		0, "not-a-number", 0, 0, 0, 0, 0, time.Now().Unix(),
	)
	require.NoError(t, err)

	got, err := store.LoadCounters(ctx, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, good, unstamped(got)[0])
}

// legacyCountersSchema is rate_limit_counters as releases before writer IDs
// created it: one row per window, whichever replica flushed last.
const legacyCountersSchema = `
	CREATE TABLE rate_limit_counters (
		scope TEXT NOT NULL,
		subject TEXT NOT NULL,
		partition TEXT NOT NULL DEFAULT '',
		period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
		requests_window_start ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		requests_current ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		requests_previous ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		tokens_window_start ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		tokens_current ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		tokens_previous ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		PRIMARY KEY (scope, subject, partition, period_seconds)
	)`

// legacyUpsertSQL is the write an older replica keeps issuing during a
// rolling upgrade.
const legacyUpsertSQL = `
	INSERT INTO rate_limit_counters (
		scope, subject, partition, period_seconds,
		requests_window_start, requests_current, requests_previous,
		tokens_window_start, tokens_current, tokens_previous, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(scope, subject, partition, period_seconds) DO UPDATE SET
		requests_current = excluded.requests_current,
		updated_at = excluded.updated_at
`

func TestSQLStoreImportsLegacyCounters(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, legacyCountersSchema)
		require.NoError(t, err)
		legacy := WindowSnapshot{
			Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: PeriodHourSeconds,
			RequestsWindowStart: 1700000000, RequestsCurrent: 3, RequestsPrevious: 1,
			TokensWindowStart: 1700000000, TokensCurrent: 40, TokensPrevious: 10,
		}
		_, err = db.Exec(ctx, legacyUpsertSQL,
			legacy.Scope, legacy.Subject, legacy.Partition, legacy.PeriodSeconds,
			legacy.RequestsWindowStart, legacy.RequestsCurrent, legacy.RequestsPrevious,
			legacy.TokensWindowStart, legacy.TokensCurrent, legacy.TokensPrevious, time.Now().Unix(),
		)
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)
		got, err := store.LoadCounters(ctx, 0)
		require.NoError(t, err)
		require.Equal(t, []WindowSnapshot{legacy}, unstamped(got), "the legacy row loads under the empty writer")
		var remaining int64
		err = db.QueryRow(ctx, `SELECT COUNT(*) FROM rate_limit_counters`).Scan(&remaining)
		require.NoError(t, err)
		require.Zero(t, remaining, "imported rows leave the legacy table")

		// Re-opening imports nothing new and keeps the imported row.
		_, err = NewSQLStore(ctx, db)
		require.NoError(t, err)
		got, err = store.LoadCounters(ctx, 0)
		require.NoError(t, err)
		require.Equal(t, []WindowSnapshot{legacy}, unstamped(got))

		// Rolling upgrade: an older replica still writes the legacy shape. The
		// next start imports its newer count over the earlier import.
		_, err = db.Exec(ctx, legacyUpsertSQL,
			legacy.Scope, legacy.Subject, legacy.Partition, legacy.PeriodSeconds,
			legacy.RequestsWindowStart, 7, legacy.RequestsPrevious,
			legacy.TokensWindowStart, legacy.TokensCurrent, legacy.TokensPrevious, time.Now().Unix(),
		)
		require.NoError(t, err)
		store, err = NewSQLStore(ctx, db)
		require.NoError(t, err)
		got, err = store.LoadCounters(ctx, 0)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, int64(7), got[0].RequestsCurrent)

		// A reset also clears what an older replica wrote since, so the next
		// start cannot import it back.
		_, err = db.Exec(ctx, legacyUpsertSQL,
			legacy.Scope, legacy.Subject, legacy.Partition, legacy.PeriodSeconds,
			legacy.RequestsWindowStart, 9, 0, 0, 0, 0, time.Now().Unix(),
		)
		require.NoError(t, err)
		err = store.ResetCounter(ctx, ScopeUserPath, "/team", PeriodHourSeconds)
		require.NoError(t, err)
		store, err = NewSQLStore(ctx, db)
		require.NoError(t, err)
		got, err = store.LoadCounters(ctx, 0)
		require.NoError(t, err)
		require.Empty(t, got)
	})
}

// A fresh install has no legacy table, and resets must not need one.
func TestSQLStoreWithoutLegacyCountersTable(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		require.False(t, store.legacyCounters)
		require.NoError(t, store.ResetCounter(context.Background(), ScopeUserPath, "/team", PeriodHourSeconds))
		require.NoError(t, store.ResetAllCounters(context.Background()))
	})
}

// A reset that runs while a starting replica imports the legacy table must
// not have the import write back what it read before the reset. SQLite
// serializes every write transaction, so only PostgreSQL can interleave them.
//
// A third transaction holds a row of the window table locked so the reset
// stops partway, then the import starts; once the import has finished or is
// waiting on the reset, the lock is released and both run to completion.
func TestSQLStoreLegacyImportCannotUndoConcurrentReset(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("SQLite serializes write transactions")
		}
		ctx := context.Background()
		require.NoError(t, db.Schema(ctx, legacyCountersSchema))
		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)
		blocker := WindowSnapshot{
			Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: PeriodHourSeconds,
			Writer: "blocker", RequestsWindowStart: 1700000000, RequestsCurrent: 1,
		}
		require.NoError(t, store.SaveCounters(ctx, []WindowSnapshot{blocker}))
		_, err = db.Exec(ctx, legacyUpsertSQL,
			string(ScopeUserPath), "/team", "", PeriodHourSeconds,
			1700000000, 90, 0, 0, 0, 0, time.Now().Unix(),
		)
		require.NoError(t, err)

		blockedBy := func(pid int64) bool {
			var waiting int64
			err := db.QueryRow(ctx, `SELECT COUNT(*) FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid))`, pid).Scan(&waiting)
			require.NoError(t, err)
			return waiting > 0
		}
		lockerPID := make(chan int64, 1)
		release := make(chan struct{})
		lockerDone := make(chan error, 1)
		go func() {
			lockerDone <- db.InTx(ctx, func(q sqlx.Querier) error {
				var pid int64
				if err := q.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					return err
				}
				if _, err := q.Exec(ctx, `SELECT 1 FROM rate_limit_window_snapshots WHERE writer = 'blocker' FOR UPDATE`); err != nil {
					return err
				}
				lockerPID <- pid
				<-release
				return nil
			})
		}()
		locker := <-lockerPID

		resetDone := make(chan error, 1)
		go func() { resetDone <- store.ResetCounter(ctx, ScopeUserPath, "/team", PeriodHourSeconds) }()
		var resetPID int64
		require.Eventually(t, func() bool {
			err := db.QueryRow(ctx, `SELECT COALESCE(MAX(pid), 0) FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid))`, locker).Scan(&resetPID)
			require.NoError(t, err)
			return resetPID != 0
		}, 5*time.Second, 10*time.Millisecond, "reset never reached the locked row")

		importDone := make(chan error, 1)
		go func() {
			_, err := NewSQLStore(ctx, db)
			importDone <- err
		}()
		require.Eventually(t, func() bool {
			return len(importDone) > 0 || blockedBy(resetPID)
		}, 5*time.Second, 10*time.Millisecond, "import neither finished nor waited on the reset")

		close(release)
		require.NoError(t, <-lockerDone)
		require.NoError(t, <-resetDone)
		require.NoError(t, <-importDone)

		got, err := store.LoadCounters(ctx, 0)
		require.NoError(t, err)
		require.Empty(t, got, "the import must not bring back the count the reset deleted")
	})
}
