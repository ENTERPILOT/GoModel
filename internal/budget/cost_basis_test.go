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

func TestServiceRejectsBilledBudgetsWhenDisabled(t *testing.T) {
	ctx := context.Background()
	billed := Budget{
		Scope: ScopeLabel, Subject: "acme", CostBasis: CostBasisBilled,
		PeriodSeconds: PeriodMonthlySeconds, Amount: 100,
	}
	_, err := NewService(ctx, &fakeStore{budgets: []Budget{billed}}, WithBilledBudgets(false))
	require.ErrorIs(t, err, ErrBilledBudgetsUnavailable)

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

func TestSeedConfiguredBudgetsCarriesCostBasis(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	err = seedConfiguredBudgets(ctx, service, config.BudgetsConfig{
		Labels: []config.BudgetLabelConfig{{Label: "acme", Limits: []config.BudgetLimitConfig{
			{Period: "monthly", Amount: 500, CostBasis: "billed"},
			{Period: "daily", Amount: 20},
		}}},
	})
	require.NoError(t, err)
	require.Len(t, store.replacedBudgets, 2)
	assert.Equal(t, CostBasisBilled, store.replacedBudgets[0].CostBasis)
	assert.Equal(t, CostBasisProvider, store.replacedBudgets[1].CostBasis)
}

// A config-seeded billed budget aborts startup unless the capability is
// present and markups are enabled, mirroring per-child quota templates.
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
			ctx := context.Background()
			shared, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "budgets.db")})
			require.NoError(t, err)
			t.Cleanup(func() { _ = shared.Close() })

			cfg := &config.Config{}
			cfg.Budgets.Enabled = true
			cfg.Pricing.MarkupsEnabled = tt.markups
			cfg.Budgets.Labels = []config.BudgetLabelConfig{{Label: "acme", Limits: []config.BudgetLimitConfig{
				{Period: "monthly", Amount: 500, CostBasis: "billed"},
			}}}

			result, err := New(ctx, cfg, shared, false, tt.capability)
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
	}
	want := []Spend{
		{Total: 3, HasUsage: true},    // provider: uncached marked-up + pre-markup
		{Total: 7.75, HasUsage: true}, // billed: 1.25 + 2 (fallback) + 4.5 (billed hit) + 0
		{Total: 7.75, HasUsage: true}, // same rows by user path
		{},                            // provider never counts cache hits
		{Total: 0, HasUsage: true},    // an unbilled hit is billed usage worth 0
		{},
	}

	got, err := store.SumSpend(context.Background(), windows)
	require.NoError(t, err)
	require.Len(t, got, len(want))
	for i := range want {
		assert.Equal(t, want[i].HasUsage, got[i].HasUsage, "spend[%d] has usage", i)
		assert.InDelta(t, want[i].Total, got[i].Total, 1e-9, "spend[%d] total", i)
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
