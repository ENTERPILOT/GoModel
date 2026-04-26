package budget

import (
	"context"
	"errors"
	"testing"
	"time"

	"gomodel/config"
)

type fakeStore struct {
	budgets  []Budget
	settings Settings
	sum      func(userPath string, start, end time.Time) (float64, bool, error)

	lastSumUserPath string
	lastSumStart    time.Time
	lastResetAt     time.Time
	replaceCalls    int
	replacedBudgets []Budget
}

func (s *fakeStore) ListBudgets(context.Context) ([]Budget, error) {
	return append([]Budget(nil), s.budgets...), nil
}

func (s *fakeStore) UpsertBudgets(context.Context, []Budget) error {
	return nil
}

func (s *fakeStore) DeleteBudget(context.Context, string, int64) error {
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

func (s *fakeStore) SaveSettings(context.Context, Settings) (Settings, error) {
	return Settings{}, nil
}

func (s *fakeStore) ResetBudget(_ context.Context, _ string, _ int64, at time.Time) error {
	s.lastResetAt = at
	return nil
}

func (s *fakeStore) ResetAllBudgets(_ context.Context, at time.Time) error {
	s.lastResetAt = at
	return nil
}

func (s *fakeStore) SumUsageCost(_ context.Context, userPath string, start, end time.Time) (float64, bool, error) {
	s.lastSumUserPath = userPath
	s.lastSumStart = start
	if s.sum == nil {
		return 0, false, nil
	}
	return s.sum(userPath, start, end)
}

func (s *fakeStore) Close() error {
	return nil
}

func TestServiceRefreshSortsBudgetsByUserPathThenLongestPeriod(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{
			{UserPath: "/team/beta", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{UserPath: "/team/alpha", PeriodSeconds: PeriodDailySeconds, Amount: 10},
			{UserPath: "/team/alpha", PeriodSeconds: PeriodMonthlySeconds, Amount: 100},
			{UserPath: "/team/alpha", PeriodSeconds: PeriodWeeklySeconds, Amount: 50},
		},
	}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	got := service.Budgets()
	want := []Budget{
		{UserPath: "/team/alpha", PeriodSeconds: PeriodMonthlySeconds, Amount: 100},
		{UserPath: "/team/alpha", PeriodSeconds: PeriodWeeklySeconds, Amount: 50},
		{UserPath: "/team/alpha", PeriodSeconds: PeriodDailySeconds, Amount: 10},
		{UserPath: "/team/beta", PeriodSeconds: PeriodDailySeconds, Amount: 10},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d budgets, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].UserPath != want[i].UserPath || got[i].PeriodSeconds != want[i].PeriodSeconds {
			t.Fatalf("budget[%d] = %s/%d, want %s/%d", i, got[i].UserPath, got[i].PeriodSeconds, want[i].UserPath, want[i].PeriodSeconds)
		}
	}
}

func TestSeedConfiguredBudgetsReplacesEmptyConfigSet(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}
	store.replaceCalls = 0

	if err := seedConfiguredBudgets(ctx, service, config.BudgetsConfig{}); err != nil {
		t.Fatalf("seedConfiguredBudgets() failed: %v", err)
	}
	if store.replaceCalls != 1 {
		t.Fatalf("ReplaceConfigBudgets calls = %d, want 1", store.replaceCalls)
	}
	if len(store.replacedBudgets) != 0 {
		t.Fatalf("replaced budgets = %+v, want empty", store.replacedBudgets)
	}
}

func TestServiceCheckRejectsExceededBudgetForMatchingUserPath(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{
			{UserPath: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10},
		},
		sum: func(userPath string, start, end time.Time) (float64, bool, error) {
			if userPath != "/team" {
				t.Fatalf("sum user path = %q, want /team", userPath)
			}
			return 10, true, nil
		},
	}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	err = service.Check(ctx, "/team/app", time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("Check() error = %v, want ExceededError", err)
	}
	if got := exceeded.Result.Budget.UserPath; got != "/team" {
		t.Fatalf("exceeded budget path = %q, want /team", got)
	}
}

func TestServiceCheckDoesNotEnforceBudgetWithoutUsage(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{
			{UserPath: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10},
		},
		sum: func(userPath string, start, end time.Time) (float64, bool, error) {
			if userPath != "/team" {
				t.Fatalf("sum user path = %q, want /team", userPath)
			}
			return 100, false, nil
		},
	}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	if err := service.Check(ctx, "/team", time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Check() error = %v, want nil when SumUsageCost reports no usage", err)
	}
	results, err := service.CheckWithResults(ctx, "/team", time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CheckWithResults() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("CheckWithResults() returned %d results, want 1", len(results))
	}
	if results[0].HasUsage {
		t.Fatal("CheckWithResults().HasUsage = true, want false")
	}
}

func TestServiceCheckIgnoresSiblingUserPath(t *testing.T) {
	ctx := context.Background()
	called := false
	store := &fakeStore{
		budgets: []Budget{
			{UserPath: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10},
		},
		sum: func(userPath string, start, end time.Time) (float64, bool, error) {
			called = true
			return 0, false, nil
		},
	}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	results, err := service.CheckWithResults(ctx, "/team-alpha", time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CheckWithResults() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no matching budgets, got %d", len(results))
	}
	if called {
		t.Fatal("sum should not be called for a sibling path")
	}
}

func TestServiceCheckStartsAtManualResetWhenNewerThanPeriodStart(t *testing.T) {
	ctx := context.Background()
	resetAt := time.Date(2026, time.April, 25, 9, 0, 0, 0, time.UTC)
	store := &fakeStore{
		budgets: []Budget{
			{UserPath: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, LastResetAt: &resetAt},
		},
	}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	_, err = service.CheckWithResults(ctx, "/team", time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CheckWithResults() error = %v", err)
	}
	if !store.lastSumStart.Equal(resetAt) {
		t.Fatalf("sum start = %s, want reset time %s", store.lastSumStart, resetAt)
	}
}
