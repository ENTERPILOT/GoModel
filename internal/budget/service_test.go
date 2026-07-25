package budget

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
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
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	got := service.Budgets()
	want := []Budget{
		{Scope: ScopeLabel, Subject: "prod", PeriodSeconds: PeriodDailySeconds},
		{Scope: ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: PeriodMonthlySeconds},
		{Scope: ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: PeriodWeeklySeconds},
		{Scope: ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: PeriodDailySeconds},
		{Scope: ScopeUserPath, Subject: "/team/beta", PeriodSeconds: PeriodDailySeconds},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d budgets, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Scope != want[i].Scope || got[i].Subject != want[i].Subject || got[i].PeriodSeconds != want[i].PeriodSeconds {
			t.Fatalf("budget[%d] = %s %s/%d, want %s %s/%d",
				i, got[i].Scope, got[i].Subject, got[i].PeriodSeconds,
				want[i].Scope, want[i].Subject, want[i].PeriodSeconds)
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

func TestSeedConfiguredBudgetsSeedsBothScopes(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	err = seedConfiguredBudgets(ctx, service, config.BudgetsConfig{
		UserPaths: []config.BudgetUserPathConfig{
			{Path: "team", Limits: []config.BudgetLimitConfig{{Period: "daily", Amount: 10}}},
		},
		Labels: []config.BudgetLabelConfig{
			{Label: "Mobile-App-iOS", Limits: []config.BudgetLimitConfig{{Period: "monthly", Amount: 500}}},
		},
	})
	if err != nil {
		t.Fatalf("seedConfiguredBudgets() failed: %v", err)
	}

	want := []Budget{
		{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceConfig},
		{Scope: ScopeLabel, Subject: "Mobile-App-iOS", PeriodSeconds: PeriodMonthlySeconds, Amount: 500, Source: SourceConfig},
	}
	if len(store.replacedBudgets) != len(want) {
		t.Fatalf("replaced budgets = %+v, want %d entries", store.replacedBudgets, len(want))
	}
	for i, budget := range want {
		got := store.replacedBudgets[i]
		if got.Scope != budget.Scope || got.Subject != budget.Subject ||
			got.PeriodSeconds != budget.PeriodSeconds || got.Amount != budget.Amount || got.Source != budget.Source {
			t.Fatalf("replaced budget[%d] = %+v, want %+v", i, got, budget)
		}
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
	if !strings.Contains(err.Error(), `invalid budget period for user_path "/team" limit 0: "fortnightly"`) {
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
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10},
		},
		sum: func(window SpendWindow) (float64, bool, error) {
			if window.Subject != "/team" {
				t.Fatalf("sum subject = %q, want /team", window.Subject)
			}
			return 10, true, nil
		},
	}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	err = service.Check(ctx, path("/team/app"), time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("Check() error = %v, want ExceededError", err)
	}
	if got := exceeded.Result.Budget.Subject; got != "/team" {
		t.Fatalf("exceeded budget subject = %q, want /team", got)
	}
}

func TestServiceCheckRejectsExceededLabelBudget(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		budgets: []Budget{
			{Scope: ScopeLabel, Subject: "iOS", PeriodSeconds: PeriodMonthlySeconds, Amount: 100},
		},
		sum: func(window SpendWindow) (float64, bool, error) {
			if window.Scope != ScopeLabel || window.Subject != "iOS" {
				t.Fatalf("sum window = %s %q, want label iOS", window.Scope, window.Subject)
			}
			return 120, true, nil
		},
	}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)

	subjects := Subjects{UserPath: "/team", Labels: []string{"android", "iOS"}}
	var exceeded *ExceededError
	if err := service.Check(ctx, subjects, now); !errors.As(err, &exceeded) {
		t.Fatalf("Check() error = %v, want ExceededError", err)
	}
	if got := exceeded.Error(); !strings.Contains(got, "label iOS") {
		t.Fatalf("ExceededError = %q, want it to name the label subject", got)
	}

	// The label is matched verbatim, so a different casing is a different budget.
	if err := service.Check(ctx, Subjects{UserPath: "/team", Labels: []string{"ios"}}, now); err != nil {
		t.Fatalf("Check() with unmatched label casing error = %v, want nil", err)
	}
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
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	subjects := Subjects{UserPath: "/team/app", Labels: []string{"prod", "iOS"}}
	results, err := service.CheckWithResults(ctx, subjects, time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CheckWithResults() error = %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("CheckWithResults() returned %d results, want 4 (/, /team, label prod, label iOS)", len(results))
	}
	if store.sumCalls != 1 {
		t.Fatalf("store spend lookups = %d, want 1 batched call for all matching budgets", store.sumCalls)
	}
	if len(store.lastWindows) != 4 {
		t.Fatalf("batched windows = %d, want 4", len(store.lastWindows))
	}
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
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	results, err := service.StatusesFor(ctx, path("/team/app"), time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("StatusesFor() error = %v, want nil", err)
	}
	if len(results) != 2 {
		t.Fatalf("StatusesFor() returned %d results, want 2 (exceeded budgets must not stop evaluation)", len(results))
	}
	byPeriod := map[int64]CheckResult{}
	for _, result := range results {
		if result.Budget.Subject != "/team" {
			t.Fatalf("result budget subject = %q, want /team", result.Budget.Subject)
		}
		byPeriod[result.Budget.PeriodSeconds] = result
	}
	if got := byPeriod[PeriodDailySeconds].Spent; got != 12 {
		t.Fatalf("daily spent = %v, want 12", got)
	}
	if got := byPeriod[PeriodMonthlySeconds].Remaining; got != 58 {
		t.Fatalf("monthly remaining = %v, want 58", got)
	}
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
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	if _, err := service.StatusesFor(ctx, path("/te:am"), now); err == nil {
		t.Fatal("StatusesFor() with invalid path: error = nil, want normalization error")
	}
	results, err := service.StatusesFor(ctx, path("/team"), now)
	if err == nil || !strings.Contains(err.Error(), "store down") {
		t.Fatalf("StatusesFor() error = %v, want store failure", err)
	}
	if len(results) != 0 {
		t.Fatalf("StatusesFor() partial results = %d, want 0 when the batch fails", len(results))
	}
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
			if err != nil {
				t.Fatalf("NewService() failed: %v", err)
			}

			err = service.Check(ctx, path("/team/app"), now)
			var exceeded *ExceededError
			if tt.wantError {
				if !errors.As(err, &exceeded) {
					t.Fatalf("Check() error = %v, want ExceededError", err)
				}
				if exceeded.Result.Spent != tt.spent {
					t.Fatalf("exceeded spent = %v, want %v", exceeded.Result.Spent, tt.spent)
				}
				return
			}
			if err != nil {
				t.Fatalf("Check() error = %v, want nil", err)
			}
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
			if window.Subject != "/team" {
				t.Fatalf("sum subject = %q, want /team", window.Subject)
			}
			return 100, false, nil
		},
	}
	service, err := NewService(ctx, store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	if err := service.Check(ctx, path("/team"), now); err != nil {
		t.Fatalf("Check() error = %v, want nil when the store reports no usage", err)
	}
	results, err := service.CheckWithResults(ctx, path("/team"), now)
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
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	// A sibling path, and a request whose labels do not include "prod".
	results, err := service.CheckWithResults(ctx, Subjects{UserPath: "/team-alpha", Labels: []string{"staging"}}, now)
	if err != nil {
		t.Fatalf("CheckWithResults() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no matching budgets, got %d", len(results))
	}
	if store.sumCalls != 0 {
		t.Fatal("the store should not be queried when nothing matches")
	}
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
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	_, err = service.CheckWithResults(ctx, path("/team"), time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CheckWithResults() error = %v", err)
	}
	if !store.lastWindows[0].Start.Equal(resetAt) {
		t.Fatalf("sum start = %s, want reset time %s", store.lastWindows[0].Start, resetAt)
	}
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
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}

	_, err = service.CheckWithResults(ctx, path("/team"), now)
	if err != nil {
		t.Fatalf("CheckWithResults() error = %v", err)
	}
	want := time.Date(2026, time.April, 25, 0, 0, 0, 0, time.UTC)
	if !store.lastWindows[0].Start.Equal(want) {
		t.Fatalf("sum start = %s, want period start %s", store.lastWindows[0].Start, want)
	}
}
