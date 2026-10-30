package budget

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/internal/usage"
)

func TestNormalizeCostBasis(t *testing.T) {
	tests := []struct {
		raw     string
		want    CostBasis
		wantErr bool
	}{
		{raw: "", want: CostBasisProvider},
		{raw: "provider", want: CostBasisProvider},
		{raw: " Billed ", want: CostBasisBilled},
		{raw: "billed", want: CostBasisBilled},
		{raw: "invoice", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := NormalizeCostBasis(tt.raw)
			if tt.wantErr {
				require.ErrorContains(t, err, "cost_basis must be one of provider, billed")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeBudgetDefaultsCostBasisToProvider(t *testing.T) {
	got, err := NormalizeBudget(Budget{Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 1})
	require.NoError(t, err)
	assert.Equal(t, CostBasisProvider, got.CostBasis)

	_, err = NormalizeBudget(Budget{Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 1, CostBasis: "gross"})
	require.Error(t, err)
}

func TestServiceRejectsBilledBudgetWritesWhenDisabled(t *testing.T) {
	ctx := context.Background()
	billed := Budget{
		Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled,
		PeriodSeconds: PeriodMonthlySeconds, Amount: 100,
	}
	store := &fakeStore{}
	service, err := NewService(ctx, store, WithBilledBudgets(false))
	require.NoError(t, err)
	err = service.UpsertBudgets(ctx, []Budget{billed})
	require.ErrorIs(t, err, ErrBilledBudgetsUnavailable)
	err = service.ReplaceConfigBudgets(ctx, []Budget{billed})
	require.ErrorIs(t, err, ErrBilledBudgetsUnavailable)
	require.Equal(t, 0, store.replaceCalls)

	// Provider-cost budgets are unaffected by the gate.
	provider := billed
	provider.CostBasis = CostBasisProvider
	require.NoError(t, service.UpsertBudgets(ctx, []Budget{provider}))
}

// A stored billed budget must not stop the gateway when billed budgets become
// unavailable: it keeps limiting spend, on provider cost.
func TestServiceEnforcesStoredBilledBudgetOnProviderCostWhenDisabled(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{{
			Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled,
			PeriodSeconds: PeriodMonthlySeconds, Amount: 100, Source: SourceManual,
		}},
		sum: func(SpendWindow) (float64, bool, error) { return 100, true, nil },
	}
	service, err := NewService(ctx, store, WithBilledBudgets(false), WithBilledCacheHits(true))
	require.NoError(t, err)

	budgets := service.Budgets()
	require.Len(t, budgets, 1)
	assert.Equal(t, CostBasisBilled, budgets[0].CostBasis, "the stored basis is kept")
	assert.Equal(t, CostBasisProvider, budgets[0].EnforcedBasis())

	err = service.Check(ctx, Subjects{UserPath: "/", Labels: []string{"acme"}}, time.Now())
	var exceeded *ExceededError
	require.ErrorAs(t, err, &exceeded)
	require.Len(t, store.lastWindows, 1)
	assert.Equal(t, CostBasisProvider, store.lastWindows[0].CostBasis)

	// Enforced on provider cost, the budget no longer gates cache hits.
	store.sumCalls = 0
	require.NoError(t, service.CheckCacheHit(ctx, Subjects{UserPath: "/", Labels: []string{"acme"}}, time.Now()))
	assert.Zero(t, store.sumCalls)
}

func TestServiceKeepsStoredBilledBasisOnEditWhenDisabled(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{budgets: []Budget{{
		Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled,
		PeriodSeconds: PeriodMonthlySeconds, Amount: 100, Source: SourceManual,
	}}}
	service, err := NewService(ctx, store, WithBilledBudgets(false))
	require.NoError(t, err)

	assert.True(t, service.StoredAsBilled(ScopeLabel, "acme", PeriodMonthlySeconds))
	assert.False(t, service.StoredAsBilled(ScopeLabel, "acme", PeriodDailySeconds))
	assert.False(t, service.StoredAsBilled(ScopeLabel, "other", PeriodMonthlySeconds))

	// Editing the amount keeps the stored billed basis.
	require.NoError(t, service.UpsertBudgets(ctx, []Budget{{
		Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled,
		PeriodSeconds: PeriodMonthlySeconds, Amount: 200, Source: SourceManual,
	}}))
	// A new billed budget still needs the capability.
	err = service.UpsertBudgets(ctx, []Budget{{
		Scope: ScopeLabel, Subject: "other", CostBasis: CostBasisBilled,
		PeriodSeconds: PeriodMonthlySeconds, Amount: 200, Source: SourceManual,
	}})
	require.ErrorIs(t, err, ErrBilledBudgetsUnavailable)
	// Config budgets get the full check, even for a stored billed key.
	err = service.ReplaceConfigBudgets(ctx, []Budget{{
		Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled,
		PeriodSeconds: PeriodMonthlySeconds, Amount: 200, Source: SourceConfig,
	}})
	require.ErrorIs(t, err, ErrBilledBudgetsUnavailable)
	assert.Zero(t, store.replaceCalls)
}

func TestServicePassesCostBasisToSpendWindows(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{budgets: []Budget{
		{Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled, PeriodSeconds: PeriodDailySeconds, Amount: 10},
		{Scope: ScopeUserPath, Subject: "/", CostBasis: CostBasisProvider, PeriodSeconds: PeriodDailySeconds, Amount: 10},
	}}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	_, err = service.CheckWithResults(ctx, Subjects{UserPath: "/team", Labels: []string{"acme"}}, time.Now())
	require.NoError(t, err)
	require.Len(t, store.lastWindows, 2)
	bases := map[Scope]CostBasis{}
	for _, window := range store.lastWindows {
		bases[window.Scope] = window.CostBasis
	}
	assert.Equal(t, CostBasisBilled, bases[ScopeLabel])
	assert.Equal(t, CostBasisProvider, bases[ScopeUserPath])
}

func TestServiceCheckCacheHit(t *testing.T) {
	billedLabel := Budget{Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled, PeriodSeconds: PeriodDailySeconds, Amount: 10}
	providerLabel := Budget{Scope: ScopeLabel, Subject: "acme", PeriodSeconds: PeriodDailySeconds, Amount: 10}
	billedProvider := Budget{Scope: ScopeProvider, Subject: "openai", CostBasis: CostBasisBilled, PeriodSeconds: PeriodDailySeconds, Amount: 10}
	billedOther := Budget{Scope: ScopeLabel, Subject: "other", CostBasis: CostBasisBilled, PeriodSeconds: PeriodDailySeconds, Amount: 10}
	tests := []struct {
		name            string
		budgets         []Budget
		billedCacheHits bool
		wantExceeded    bool
		wantStoreCall   bool
	}{
		{name: "billed budget exhausted", budgets: []Budget{billedLabel}, billedCacheHits: true, wantExceeded: true, wantStoreCall: true},
		{name: "cache hits not billed", budgets: []Budget{billedLabel}},
		{name: "provider cost budget", budgets: []Budget{providerLabel}, billedCacheHits: true},
		{name: "billed provider-scope budget", budgets: []Budget{billedProvider}, billedCacheHits: true},
		{name: "billed budget for another label", budgets: []Budget{billedOther}, billedCacheHits: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := &fakeStore{
				budgets: tt.budgets,
				sum:     func(SpendWindow) (float64, bool, error) { return 10, true, nil },
			}
			service, err := NewService(ctx, store, WithBilledCacheHits(tt.billedCacheHits))
			require.NoError(t, err)

			err = service.CheckCacheHit(ctx, Subjects{UserPath: "/team", Labels: []string{"acme"}, Provider: "openai"}, time.Now())
			if tt.wantExceeded {
				var exceeded *ExceededError
				require.ErrorAs(t, err, &exceeded)
				assert.Equal(t, "acme", exceeded.Result.Budget.Subject)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantStoreCall, store.sumCalls > 0)
		})
	}
}

func TestSeedConfiguredBudgetsCarriesCostBasis(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	err := seedConfiguredBudgets(ctx, store, config.BudgetsConfig{
		Labels: []config.BudgetLabelConfig{{Label: "acme", Limits: []config.BudgetLimitConfig{
			{Period: "monthly", Amount: 500, CostBasis: "billed"},
			{Period: "daily", Amount: 20},
		}}},
	}, allCapabilities)
	require.NoError(t, err)
	require.Len(t, store.replacedBudgets, 2)
	assert.Equal(t, CostBasisBilled, store.replacedBudgets[0].CostBasis)
	assert.Equal(t, CostBasisProvider, store.replacedBudgets[1].CostBasis)

	err = seedConfiguredBudgets(ctx, store, config.BudgetsConfig{
		Labels: []config.BudgetLabelConfig{{Label: "acme", Limits: []config.BudgetLimitConfig{
			{Period: "monthly", Amount: 500, CostBasis: "billed"},
		}}},
	}, capabilities{quotaTemplates: true})
	require.ErrorIs(t, err, ErrBilledBudgetsUnavailable)
	assert.Equal(t, 1, store.replaceCalls)
}

func newSQLiteStorage(t *testing.T) storage.Storage {
	t.Helper()
	shared, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "budgets.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shared.Close() })
	return shared
}

func billedLabelConfig(basis string) []config.BudgetLabelConfig {
	return []config.BudgetLabelConfig{{Label: "acme", Limits: []config.BudgetLimitConfig{
		{Period: "monthly", Amount: 500, CostBasis: basis},
	}}}
}

// A billed budget in the current config aborts startup unless the capability
// is present and markups are enabled, mirroring per-child quota templates.
func TestNewGatesConfiguredBilledBudgets(t *testing.T) {
	tests := []struct {
		name       string
		capability bool
		markups    bool
		wantErr    bool
	}{
		{name: "capability and markups", capability: true, markups: true},
		{name: "no capability", capability: false, markups: true, wantErr: true},
		{name: "markups disabled", capability: true, markups: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Budgets.Enabled = true
			cfg.Pricing.MarkupsEnabled = tt.markups
			cfg.Budgets.Labels = billedLabelConfig("billed")

			result, err := New(context.Background(), cfg, newSQLiteStorage(t), false, tt.capability)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrBilledBudgetsUnavailable)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { _ = result.Close() })
			budgets := result.Service.Budgets()
			require.Len(t, budgets, 1)
			assert.Equal(t, CostBasisBilled, budgets[0].CostBasis)
		})
	}
}

// Restarting after billed budgets become unavailable must start: a config
// change that drops or switches the billed budget applies, and a manual billed
// budget keeps limiting spend on provider cost.
func TestNewStartsWithStoredBilledBudgetsAfterEntitlementLoss(t *testing.T) {
	tests := []struct {
		name      string
		nextLabel []config.BudgetLabelConfig
		manual    bool
		stored    []CostBasis
		enforced  []CostBasis
	}{
		{name: "config budget switched to provider", nextLabel: billedLabelConfig("provider"), stored: []CostBasis{CostBasisProvider}, enforced: []CostBasis{CostBasisProvider}},
		{name: "config budget removed"},
		{name: "manual billed budget", manual: true, stored: []CostBasis{CostBasisBilled}, enforced: []CostBasis{CostBasisProvider}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			shared := newSQLiteStorage(t)

			cfg := &config.Config{}
			cfg.Budgets.Enabled = true
			cfg.Pricing.MarkupsEnabled = true
			if !tt.manual {
				cfg.Budgets.Labels = billedLabelConfig("billed")
			}
			first, err := New(ctx, cfg, shared, false, true)
			require.NoError(t, err)
			if tt.manual {
				require.NoError(t, first.Service.UpsertBudgets(ctx, []Budget{{
					Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled,
					PeriodSeconds: PeriodMonthlySeconds, Amount: 500, Source: SourceManual,
				}}))
			}
			require.NoError(t, first.Close())

			cfg.Pricing.MarkupsEnabled = false
			cfg.Budgets.Labels = tt.nextLabel
			next, err := New(ctx, cfg, shared, false, false)
			require.NoError(t, err)
			t.Cleanup(func() { _ = next.Close() })

			var stored, enforced []CostBasis
			for _, item := range next.Service.Budgets() {
				stored = append(stored, item.CostBasis)
				enforced = append(enforced, item.EnforcedBasis())
			}
			assert.Equal(t, tt.stored, stored)
			assert.Equal(t, tt.enforced, enforced)
		})
	}
}

// billedSpendEntries covers every row shape a billed window must handle: a
// marked-up row, a row written before markups were enabled (no billed cost), a
// billed and an unbilled response-cache hit, a pre-markup cache hit, and a row
// of unknown cost.
func billedSpendEntries(now time.Time) []*usage.UsageEntry {
	withBilled := func(entry *usage.UsageEntry, billed float64) *usage.UsageEntry {
		entry.BilledCost = &billed
		return entry
	}
	unknown := usageEntryWithCost("unknown", "/acme", "", now, 0, "acme")
	unknown.InputCost, unknown.OutputCost, unknown.TotalCost = nil, nil, nil
	return []*usage.UsageEntry{
		withBilled(usageEntryWithCost("marked-up", "/acme", "", now, 1, "acme"), 1.25),
		usageEntryWithCost("pre-markup", "/acme", "", now, 2, "acme"),
		withBilled(usageEntryWithCost("billed-hit", "/acme", usage.CacheTypeExact, now, 4, "acme"), 4.5),
		withBilled(usageEntryWithCost("free-hit", "/acme", usage.CacheTypeSemantic, now, 8, "acme"), 0),
		usageEntryWithCost("pre-markup-hit", "/acme", usage.CacheTypeExact, now, 16, "acme"),
		unknown,
		withBilled(usageEntryWithCost("only-free-hit", "/cache-only", usage.CacheTypeExact, now, 3, "cache-only"), 0),
	}
}

// assertBilledSpend checks provider and billed windows over billedSpendEntries
// in one batch, so a mixed batch is covered too.
func assertBilledSpend(t *testing.T, store Store, now time.Time) {
	t.Helper()
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	windows := []SpendWindow{
		{Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisProvider, Start: start, End: end},
		{Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled, Start: start, End: end},
		{Scope: ScopeUserPath, Subject: "/acme", CostBasis: CostBasisBilled, Start: start, End: end},
		{Scope: ScopeLabel, Subject: "cache-only", CostBasis: CostBasisProvider, Start: start, End: end},
		{Scope: ScopeLabel, Subject: "cache-only", CostBasis: CostBasisBilled, Start: start, End: end},
		{Scope: ScopeLabel, Subject: "absent", CostBasis: CostBasisBilled, Start: start, End: end},
		{Scope: ScopeProvider, Subject: "test", CostBasis: CostBasisBilled, Start: start, End: end},
		{Scope: ScopeProvider, Subject: "test", CostBasis: CostBasisBilled, Start: start, End: end, ExcludeCacheHits: true},
		{Scope: ScopeLabel, Subject: "cache-only", CostBasis: CostBasisBilled, Start: start, End: end, ExcludeCacheHits: true},
	}
	want := []Spend{
		{Total: 3, HasUsage: true},    // provider: uncached marked-up + pre-markup
		{Total: 7.75, HasUsage: true}, // billed: 1.25 + 2 (fallback) + 4.5 (billed hit) + 0
		{Total: 7.75, HasUsage: true}, // same rows by user path
		{},                            // provider never counts cache hits
		{Total: 0, HasUsage: true},    // an unbilled hit is billed usage worth 0
		{},
		{Total: 7.75, HasUsage: true}, // billed provider budget counts billed hits by default
		{Total: 3.25, HasUsage: true}, // without them: 1.25 + 2 (fallback)
		{},                            // nothing left once the only rows are hits
	}

	got, err := store.SumSpend(context.Background(), windows)
	require.NoError(t, err)
	require.Len(t, got, len(want))
	for i := range want {
		assert.Equal(t, want[i].HasUsage, got[i].HasUsage, "spend[%d] has usage", i)
		assert.InDelta(t, want[i].Total, got[i].Total, 1e-9, "spend[%d] total", i)
	}
}

func TestServiceExcludesBilledCacheHitsFromProviderBudgetsWhenConfigured(t *testing.T) {
	budgets := []Budget{
		{Scope: ScopeProvider, Subject: "openai", CostBasis: CostBasisBilled, PeriodSeconds: PeriodDailySeconds, Amount: 10},
		{Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled, PeriodSeconds: PeriodDailySeconds, Amount: 10},
	}
	for _, tt := range []struct {
		name    string
		options []ServiceOption
		exclude map[Scope]bool
	}{
		{name: "default counts hits", exclude: map[Scope]bool{ScopeProvider: false, ScopeLabel: false}},
		{name: "disabled excludes provider hits", options: []ServiceOption{WithProviderBilledCacheHits(false)}, exclude: map[Scope]bool{ScopeProvider: true, ScopeLabel: false}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{budgets: budgets}
			options := append([]ServiceOption{WithBilledBudgets(true)}, tt.options...)
			service, err := NewService(context.Background(), store, options...)
			require.NoError(t, err)
			_, err = service.Statuses(context.Background(), time.Now())
			require.NoError(t, err)
			got := map[Scope]bool{}
			for _, window := range store.lastWindows {
				got[window.Scope] = window.ExcludeCacheHits
			}
			assert.Equal(t, tt.exclude, got)
		})
	}
}

func TestSQLStoreSumSpendOnBilledCost(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		usageStore, err := usage.NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
		require.NoError(t, usageStore.WriteBatch(ctx, billedSpendEntries(now)))

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)
		assertBilledSpend(t, store, now)
	})
}

func TestMongoDBStoreSumSpendOnBilledCost(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		usageStore, err := usage.NewMongoDBStore(db, 0)
		require.NoError(t, err)
		now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
		require.NoError(t, usageStore.WriteBatch(ctx, billedSpendEntries(now)))

		store, err := NewMongoDBStore(ctx, db)
		require.NoError(t, err)
		assertBilledSpend(t, store, now)
	})
}

func TestSQLStoreRoundTripsCostBasis(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		assertStoreRoundTripsCostBasis(t, store)
	})
}

func TestMongoDBStoreRoundTripsCostBasis(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(context.Background(), db)
		require.NoError(t, err)
		assertStoreRoundTripsCostBasis(t, store)
	})
}

// assertStoreRoundTripsCostBasis persists both bases, then checks that a
// config re-seed leaves a manual budget's basis alone while updating a
// config-sourced one.
func assertStoreRoundTripsCostBasis(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	err := store.UpsertBudgets(ctx, []Budget{
		{Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled, PeriodSeconds: PeriodMonthlySeconds, Amount: 100, Source: SourceManual},
		{Scope: ScopeLabel, Subject: "beta", PeriodSeconds: PeriodMonthlySeconds, Amount: 50, Source: SourceConfig},
	})
	require.NoError(t, err)
	err = store.ReplaceConfigBudgets(ctx, []Budget{
		{Scope: ScopeLabel, Subject: "acme", PeriodSeconds: PeriodMonthlySeconds, Amount: 1},
		{Scope: ScopeLabel, Subject: "beta", CostBasis: CostBasisBilled, PeriodSeconds: PeriodMonthlySeconds, Amount: 50},
	})
	require.NoError(t, err)

	budgets, err := store.ListBudgets(ctx)
	require.NoError(t, err)
	bases := map[string]CostBasis{}
	for _, item := range budgets {
		bases[item.Subject] = item.CostBasis
	}
	assert.Equal(t, map[string]CostBasis{"acme": CostBasisBilled, "beta": CostBasisBilled}, bases)

	err = store.UpsertBudgets(ctx, []Budget{
		{Scope: ScopeLabel, Subject: "acme", PeriodSeconds: PeriodMonthlySeconds, Amount: 100, Source: SourceManual},
	})
	require.NoError(t, err)
	budgets, err = store.ListBudgets(ctx)
	require.NoError(t, err)
	for _, item := range budgets {
		if item.Subject == "acme" {
			assert.Equal(t, CostBasisProvider, item.CostBasis)
		}
	}
}

// Budgets stored before cost bases existed keep limiting provider cost.
func TestSQLStoreMigratesPreCostBasisRows(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, `CREATE TABLE budgets (
			scope TEXT NOT NULL DEFAULT 'user_path',
			subject TEXT NOT NULL,
			per_child `+sqlx.TypeBool+` NOT NULL DEFAULT FALSE,
			period_seconds `+sqlx.TypeInt64+` NOT NULL,
			amount `+sqlx.TypeFloat+` NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			last_reset_at `+sqlx.TypeInt64+`,
			created_at `+sqlx.TypeInt64+` NOT NULL,
			updated_at `+sqlx.TypeInt64+` NOT NULL,
			PRIMARY KEY (scope, subject, period_seconds)
		)`)
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
		assert.Equal(t, CostBasisProvider, budgets[0].CostBasis)
	})
}

func TestMongoDBStoreReadsPreCostBasisDocumentsAsProvider(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
		_, err := db.Collection("budgets").InsertOne(ctx, bson.D{
			{Key: "scope", Value: string(ScopeUserPath)}, {Key: "subject", Value: "/legacy"},
			{Key: "period_seconds", Value: PeriodDailySeconds}, {Key: "amount", Value: 10.0},
			{Key: "source", Value: SourceManual}, {Key: "created_at", Value: now}, {Key: "updated_at", Value: now},
		})
		require.NoError(t, err)

		store, err := NewMongoDBStore(ctx, db)
		require.NoError(t, err)
		budgets, err := store.ListBudgets(ctx)
		require.NoError(t, err)
		require.Len(t, budgets, 1)
		assert.Equal(t, CostBasisProvider, budgets[0].CostBasis)
	})
}
