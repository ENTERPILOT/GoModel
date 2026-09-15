package budget

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	budgets  []Budget
	settings Settings
	listErr  error
	sum      func(SpendWindow) (float64, bool, error)

	sumCalls        int
	lastWindows     []SpendWindow
	lastResetAt     time.Time
	replaceCalls    int
	replacedBudgets []Budget
}

func (s *fakeStore) ListBudgets(context.Context) ([]Budget, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]Budget(nil), s.budgets...), nil
}

func (s *fakeStore) UpsertBudgets(context.Context, []Budget) error {
	return nil
}

func (s *fakeStore) DeleteBudget(context.Context, Scope, string, int64) error {
	return nil
}

func (s *fakeStore) ReplaceConfigBudgets(_ context.Context, budgets []Budget) error {
	s.replaceCalls++
	s.replacedBudgets = append([]Budget(nil), budgets...)
	return nil
}

func (s *fakeStore) GetSettings(context.Context) (Settings, error) {
	if s.settings == (Settings{}) {
		return DefaultSettings(), nil
	}
	return s.settings, nil
}

func (s *fakeStore) SaveSettings(_ context.Context, settings Settings) (Settings, error) {
	s.settings = settings
	return settings, nil
}

func (s *fakeStore) ResetBudget(_ context.Context, _ Scope, _ string, _ int64, at time.Time) error {
	s.lastResetAt = at
	return nil
}

func (s *fakeStore) ResetAllBudgets(_ context.Context, at time.Time) error {
	s.lastResetAt = at
	return nil
}

func (s *fakeStore) SumSpend(_ context.Context, windows []SpendWindow) ([]Spend, error) {
	s.sumCalls++
	s.lastWindows = append([]SpendWindow(nil), windows...)
	spends := make([]Spend, len(windows))
	for i, window := range windows {
		if s.sum == nil {
			continue
		}
		total, hasUsage, err := s.sum(window)
		if err != nil {
			return nil, err
		}
		spends[i] = Spend{Total: total, HasUsage: hasUsage}
	}
	return spends, nil
}

func (s *fakeStore) Close() error {
	return nil
}

// path builds the Subjects of a request with no labels.
func path(userPath string) Subjects {
	return Subjects{UserPath: userPath}
}

func TestServiceUnavailableOperationsReturnErrors(t *testing.T) {
	ctx := context.Background()
	service := &Service{}
	var nilService *Service
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)

	checks := []struct {
		name string
		run  func() error
	}{
		{name: "nil receiver", run: func() error { return nilService.Refresh(ctx) }},
		{name: "refresh", run: func() error { return service.Refresh(ctx) }},
		{name: "upsert", run: func() error {
			return service.UpsertBudgets(ctx, []Budget{{Subject: "/", PeriodSeconds: PeriodDailySeconds, Amount: 1}})
		}},
		{name: "delete", run: func() error {
			return service.DeleteBudget(ctx, ScopeUserPath, "/", PeriodDailySeconds)
		}},
		{name: "replace config", run: func() error { return service.ReplaceConfigBudgets(ctx, nil) }},
		{name: "save settings", run: func() error {
			_, err := service.SaveSettings(ctx, DefaultSettings())
			return err
		}},
		{name: "statuses", run: func() error {
			_, err := service.Statuses(ctx, now)
			return err
		}},
		{name: "reset one", run: func() error {
			return service.ResetBudget(ctx, ScopeUserPath, "/", PeriodDailySeconds, now)
		}},
		{name: "reset all", run: func() error { return service.ResetAll(ctx, now) }},
		{name: "check", run: func() error { return service.Check(ctx, path("/"), now) }},
		{name: "check with results", run: func() error {
			_, err := service.CheckWithResults(ctx, path("/"), now)
			return err
		}},
		{name: "statuses for subjects", run: func() error {
			_, err := service.StatusesFor(ctx, path("/"), now)
			return err
		}},
	}

	for _, tt := range checks {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			require.ErrorIs(t, err, ErrUnavailable)
		})
	}
}

func TestServiceRejectsQuotaTemplatesWhenDisabled(t *testing.T) {
	template := Budget{
		Scope: ScopeUserPath, Subject: "/customers", PerChild: true,
		PeriodSeconds: PeriodDailySeconds, Amount: 10,
	}
	_, err := NewService(context.Background(), &fakeStore{budgets: []Budget{template}}, WithQuotaTemplates(false))
	require.ErrorIs(t, err, ErrQuotaTemplatesUnavailable)

	store := &fakeStore{}
	service, err := NewService(context.Background(), store, WithQuotaTemplates(false))
	require.NoError(t, err)
	err = service.UpsertBudgets(context.Background(), []Budget{template})
	require.ErrorIs(t, err, ErrQuotaTemplatesUnavailable)
	err = service.ReplaceConfigBudgets(context.Background(), []Budget{template})
	require.ErrorIs(t, err, ErrQuotaTemplatesUnavailable)
	require.Equal(t, 0, store.replaceCalls)
}

func TestServiceSaveSettingsReturnsSavedSnapshotWhenRefreshFails(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	store.listErr = errors.New("refresh failed")

	want := DefaultSettings()
	want.DailyResetHour = 7
	saved, err := service.SaveSettings(ctx, want)

	require.ErrorContains(t, err, "refresh budget service after saving settings")
	require.Equal(t, want.DailyResetHour, saved.DailyResetHour, "saved settings = %+v, want persisted snapshot %+v", saved, want)
}

func TestServiceRefreshSortsBudgetsByScopeSubjectThenLongestPeriod(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{
			{Scope: ScopeLabel, Subject: "prod", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{Scope: ScopeUserPath, Subject: "/team/beta", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{Scope: ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{Scope: ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: PeriodMonthlySeconds, Amount: 100},
			{Scope: ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: PeriodWeeklySeconds, Amount: 50},
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	got := service.Budgets()
	want := []Budget{
		{Scope: ScopeLabel, Subject: "prod", PeriodSeconds: PeriodDailySeconds},
		{Scope: ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: PeriodMonthlySeconds},
		{Scope: ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: PeriodWeeklySeconds},
		{Scope: ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: PeriodDailySeconds},
		{Scope: ScopeUserPath, Subject: "/team/beta", PeriodSeconds: PeriodDailySeconds},
	}
	require.Len(t, got, len(want))

	for i := range want {
		require.Equal(t, want[i].Scope, got[i].Scope, "budget[%d]", i)
		require.Equal(t, want[i].Subject, got[i].Subject, "budget[%d]", i)
		require.Equal(t, want[i].PeriodSeconds, got[i].PeriodSeconds, "budget[%d]", i)
	}
}

func TestSeedConfiguredBudgetsReplacesEmptyConfigSet(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	store.replaceCalls = 0
	err = seedConfiguredBudgets(ctx, service, config.BudgetsConfig{})
	require.NoError(t, err)
	require.Equal(t, 1, store.replaceCalls)
	require.Empty(t, store.replacedBudgets)
}

func TestSeedConfiguredBudgetsSeedsBothScopes(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	err = seedConfiguredBudgets(ctx, service, config.BudgetsConfig{
		UserPaths: []config.BudgetUserPathConfig{
			{Path: "team", PerChild: true, Limits: []config.BudgetLimitConfig{{Period: "daily", Amount: 10}}},
		},
		Labels: []config.BudgetLabelConfig{
			{Label: "Mobile-App-iOS", Limits: []config.BudgetLimitConfig{{Period: "monthly", Amount: 500}}},
		},
	})
	require.NoError(t, err)

	want := []Budget{
		{Scope: ScopeUserPath, Subject: "/team", PerChild: true, PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceConfig},
		{Scope: ScopeLabel, Subject: "Mobile-App-iOS", PeriodSeconds: PeriodMonthlySeconds, Amount: 500, Source: SourceConfig},
	}
	require.Len(t, store.replacedBudgets, len(want))

	for i, budget := range want {
		got := store.replacedBudgets[i]
		require.Equal(t, budget.Scope, got.Scope, "replaced budget[%d]", i)
		require.Equal(t, budget.Subject, got.Subject, "replaced budget[%d]", i)
		require.Equal(t, budget.PerChild, got.PerChild, "replaced budget[%d]", i)
		require.Equal(t, budget.PeriodSeconds, got.PeriodSeconds, "replaced budget[%d]", i)
		require.Equal(t, budget.Amount, got.Amount, "replaced budget[%d]", i)
		require.Equal(t, budget.Source, got.Source, "replaced budget[%d]", i)
	}
}

func TestSeedConfiguredBudgetsRejectsInvalidPeriodBeforeReplacing(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	store.replaceCalls = 0

	err = seedConfiguredBudgets(ctx, service, config.BudgetsConfig{
		UserPaths: []config.BudgetUserPathConfig{
			{
				Path: "/team",
				Limits: []config.BudgetLimitConfig{
					{Period: "fortnightly", Amount: 10},
				},
			},
		},
	})

	require.ErrorContains(t, err, `invalid budget period for user_path "/team" limit 0: "fortnightly"`)
	require.Equal(t, 0, store.replaceCalls)
}

func TestServiceCheckRejectsExceededBudgetForMatchingUserPath(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10},
		},
		sum: func(window SpendWindow) (float64, bool, error) {
			require.Equal(t, "/team", window.Subject)

			return 10, true, nil
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	err = service.Check(ctx, path("/team/app"), time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	var exceeded *ExceededError
	require.ErrorAs(t, err, &exceeded)
	got := exceeded.Result.Budget.Subject
	require.Equal(t, "/team", got)
}

func TestServicePerChildBudgetUsesDirectChildSpendPartition(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{{
			Scope: ScopeUserPath, Subject: "/users", PerChild: true,
			PeriodSeconds: PeriodDailySeconds, Amount: 10,
		}},
		sum: func(window SpendWindow) (float64, bool, error) {
			switch window.Subject {
			case "/users/alice":
				return 10, true, nil
			case "/users/bob":
				return 2, true, nil
			default:
				t.Fatalf("unexpected spend partition %q", window.Subject)
				return 0, false, nil
			}
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)

	var exceeded *ExceededError
	err = service.Check(ctx, path("/users/alice/app"), now)
	require.ErrorAs(t, err, &exceeded)
	require.Equal(t, "/users", exceeded.Result.Budget.Subject)
	require.Equal(t, "/users/alice", exceeded.Result.Budget.EffectiveSubject, "resolved alice budget = %+v", exceeded.Result.Budget)
	err = service.Check(ctx, path("/users/bob/app"), now)
	require.NoError(t, err)

	results, err := service.StatusesFor(ctx, path("/users"), now)
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestServiceGlobalPerChildBudgetStatusDoesNotInventAggregate(t *testing.T) {
	store := &fakeStore{budgets: []Budget{{
		Scope: ScopeUserPath, Subject: "/users", PerChild: true,
		PeriodSeconds: PeriodDailySeconds, Amount: 10,
	}}}
	service, err := NewService(context.Background(), store)
	require.NoError(t, err)

	results, err := service.Statuses(context.Background(), time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].HasUsage)
	require.Equal(t, float64(10), results[0].Remaining)
	require.Equal(t, 0, store.sumCalls)
}

func TestServiceCheckRejectsExceededLabelBudget(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{
			{Scope: ScopeLabel, Subject: "iOS", PeriodSeconds: PeriodMonthlySeconds, Amount: 100},
		},
		sum: func(window SpendWindow) (float64, bool, error) {
			require.Equal(t, ScopeLabel, window.Scope)
			require.Equal(t, "iOS", window.Subject)

			return 120, true, nil
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)

	subjects := Subjects{UserPath: "/team", Labels: []string{"android", "iOS"}}
	var exceeded *ExceededError
	err = service.Check(ctx, subjects, now)
	require.ErrorAs(t, err, &exceeded)
	got := exceeded.Error()
	require.Contains(t, got, "label iOS")
	// The label is matched verbatim, so a different casing is a different budget.
	err = service.Check(ctx, Subjects{UserPath: "/team", Labels: []string{"ios"}}, now)
	require.NoError(t, err)
}

func TestServiceCheckEvaluatesEveryMatchingBudgetInOneStoreCall(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{
			{Scope: ScopeUserPath, Subject: "/", PeriodSeconds: PeriodMonthlySeconds, Amount: 1000},
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{Scope: ScopeUserPath, Subject: "/other", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{Scope: ScopeLabel, Subject: "prod", PeriodSeconds: PeriodDailySeconds, Amount: 50},
			{Scope: ScopeLabel, Subject: "iOS", PeriodSeconds: PeriodDailySeconds, Amount: 50},
			{Scope: ScopeLabel, Subject: "staging", PeriodSeconds: PeriodDailySeconds, Amount: 50},
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	subjects := Subjects{UserPath: "/team/app", Labels: []string{"prod", "iOS"}}
	results, err := service.CheckWithResults(ctx, subjects, time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, results, 4)
	require.Equal(t, 1, store.sumCalls)
	require.Len(t, store.lastWindows, 4)
}

func TestServiceStatusesForReportsAllMatchingBudgetsWithoutEnforcing(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodMonthlySeconds, Amount: 100},
			{Scope: ScopeUserPath, Subject: "/other", PeriodSeconds: PeriodDailySeconds, Amount: 5},
		},
		sum: func(window SpendWindow) (float64, bool, error) {
			// The daily budget is exceeded; the monthly one is not.
			if window.End.Sub(window.Start) <= 24*time.Hour {
				return 12, true, nil
			}
			return 42, true, nil
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	results, err := service.StatusesFor(ctx, path("/team/app"), time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, results, 2)

	byPeriod := map[int64]CheckResult{}
	for _, result := range results {
		require.Equal(t, "/team", result.Budget.Subject)

		byPeriod[result.Budget.PeriodSeconds] = result
	}
	got := byPeriod[PeriodDailySeconds].Spent
	require.Equal(t, float64(12), got)
	got = byPeriod[PeriodMonthlySeconds].Remaining
	require.Equal(t, float64(58), got)
}

func TestServiceStatusesForErrorPaths(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		budgets: []Budget{{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10}},
		sum: func(SpendWindow) (float64, bool, error) {
			return 0, false, errors.New("store down")
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)
	_, err = service.StatusesFor(ctx, path("/te:am"), now)
	require.Error(t, err)

	results, err := service.StatusesFor(ctx, path("/team"), now)
	require.ErrorContains(t, err, "store down")
	require.Empty(t, results)
}

func TestServiceCheckBudgetAmountBoundary(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		spent     float64
		wantError bool
	}{
		{name: "below amount passes", spent: 9.99},
		{name: "equal amount blocks", spent: 10, wantError: true},
		{name: "above amount blocks", spent: 10.01, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{
				budgets: []Budget{
					{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10},
				},
				sum: func(SpendWindow) (float64, bool, error) {
					return tt.spent, true, nil
				},
			}
			service, err := NewService(ctx, store)
			require.NoError(t, err)

			err = service.Check(ctx, path("/team/app"), now)
			var exceeded *ExceededError
			if tt.wantError {
				require.ErrorAs(t, err, &exceeded)
				require.Equal(t, tt.spent, exceeded.Result.Spent)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestServiceCheckDoesNotEnforceBudgetWithoutUsage(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		budgets: []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10},
		},
		sum: func(window SpendWindow) (float64, bool, error) {
			require.Equal(t, "/team", window.Subject)

			return 100, false, nil
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)
	err = service.Check(ctx, path("/team"), now)
	require.NoError(t, err)

	results, err := service.CheckWithResults(ctx, path("/team"), now)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].HasUsage)
}

func TestServiceCheckIgnoresNonMatchingSubjects(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		budgets: []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{Scope: ScopeLabel, Subject: "prod", PeriodSeconds: PeriodDailySeconds, Amount: 10},
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	// A sibling path, and a request whose labels do not include "prod".
	results, err := service.CheckWithResults(ctx, Subjects{UserPath: "/team-alpha", Labels: []string{"staging"}}, now)
	require.NoError(t, err)
	require.Empty(t, results)
	require.Equal(t, 0, store.sumCalls)
}

func TestServiceCheckStartsAtManualResetWhenNewerThanPeriodStart(t *testing.T) {
	ctx := context.Background()
	resetAt := time.Date(2026, time.April, 25, 9, 0, 0, 0, time.UTC)
	store := &fakeStore{
		budgets: []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, LastResetAt: &resetAt},
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	_, err = service.CheckWithResults(ctx, path("/team"), time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.True(t, store.lastWindows[0].Start.Equal(resetAt), "sum start = %s, want reset time %s", store.lastWindows[0].Start, resetAt)
}

func TestServiceCheckIgnoresManualResetOlderThanPeriodStart(t *testing.T) {
	ctx := context.Background()
	resetAt := time.Date(2026, time.April, 24, 9, 0, 0, 0, time.UTC)
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		budgets: []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, LastResetAt: &resetAt},
		},
	}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	_, err = service.CheckWithResults(ctx, path("/team"), now)
	require.NoError(t, err)

	want := time.Date(2026, time.April, 25, 0, 0, 0, 0, time.UTC)
	require.True(t, store.lastWindows[0].Start.Equal(want), "sum start = %s, want period start %s", store.lastWindows[0].Start, want)
}
