package budget

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gomodel/config"
)

type fakeStore struct {
	budgets  []Budget
	settings Settings
	listErr  error
	sum      func(userPath string, start, end time.Time) (float64, bool, error)

	lastSumUserPath string
	lastSumStart    time.Time
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

func (s *fakeStore) SaveSettings(_ context.Context, settings Settings) (Settings, error) {
	s.settings = settings
	return settings, nil
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
			return service.UpsertBudgets(ctx, []Budget{{UserPath: "/", PeriodSeconds: PeriodDailySeconds, Amount: 1}})
		}},
		{name: "delete", run: func() error { return service.DeleteBudget(ctx, "/", PeriodDailySeconds) }},
		{name: "replace config", run: func() error { return service.ReplaceConfigBudgets(ctx, nil) }},
		{name: "save settings", run: func() error {
			_, err := service.SaveSettings(ctx, DefaultSettings())
			return err
		}},
		{name: "statuses", run: func() error {
			_, err := service.Statuses(ctx, now)
			return err
		}},
		{name: "reset one", run: func() error { return service.ResetBudget(ctx, "/", PeriodDailySeconds, now) }},
		{name: "reset all", run: func() error { return service.ResetAll(ctx, now) }},
		{name: "check", run: func() error { return service.Check(ctx, "/", now) }},
		{name: "check with results", run: func() error {
			_, err := service.CheckWithResults(ctx, "/", now)
			return err
		}},
	}

	for _, tt := range checks {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestServiceSaveSettingsReturnsSavedSnapshotWhenRefreshFails(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}
	store.listErr = errors.New("refresh failed")

	want := DefaultSettings()
	want.DailyResetHour = 7
	saved, err := service.SaveSettings(ctx, want)

	if err == nil {
		t.Fatal("SaveSettings() error = nil, want refresh error")
	}
	if !strings.Contains(err.Error(), "refresh budget service after saving settings") {
		t.Fatalf("SaveSettings() error = %v, want refresh wrapper", err)
	}
	if saved.DailyResetHour != want.DailyResetHour {
		t.Fatalf("saved settings = %+v, want persisted snapshot %+v", saved, want)
	}
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

func TestSeedConfiguredBudgetsRejectsInvalidPeriodBeforeReplacing(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}
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

	if err == nil {
		t.Fatal("seedConfiguredBudgets() error = nil, want invalid period error")
	}
	if !strings.Contains(err.Error(), `invalid budget period for user path "/team" limit 0: "fortnightly"`) {
		t.Fatalf("seedConfiguredBudgets() error = %v, want contextual invalid period error", err)
	}
	if store.replaceCalls != 0 {
		t.Fatalf("ReplaceConfigBudgets calls = %d, want 0", store.replaceCalls)
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
