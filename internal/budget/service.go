package budget

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// ErrUnavailable indicates a budget service was used without an initialized store.
var ErrUnavailable = errors.New("budget service is unavailable")

type Service struct {
	store Store
	mu    sync.RWMutex

	budgets  []Budget
	settings Settings
}

func NewService(ctx context.Context, store Store) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("budget store is required")
	}
	service := &Service{
		store:    store,
		settings: DefaultSettings(),
	}
	if err := service.Refresh(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) Refresh(ctx context.Context) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	budgets, err := s.store.ListBudgets(ctx)
	if err != nil {
		return err
	}
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	slices.SortStableFunc(budgets, func(a, b Budget) int {
		if order := cmp.Or(
			cmp.Compare(a.Scope, b.Scope),
			cmp.Compare(a.Subject, b.Subject),
		); order != 0 {
			return order
		}
		return cmp.Compare(b.PeriodSeconds, a.PeriodSeconds)
	})
	s.mu.Lock()
	s.budgets = budgets
	s.settings = settings
	s.mu.Unlock()
	return nil
}

func (s *Service) UpsertBudgets(ctx context.Context, budgets []Budget) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	if err := s.store.UpsertBudgets(ctx, budgets); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) DeleteBudget(ctx context.Context, scope Scope, subject string, periodSeconds int64) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	scope, subject, err := normalizeBudgetKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	if err := s.store.DeleteBudget(ctx, scope, subject, periodSeconds); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) ReplaceConfigBudgets(ctx context.Context, budgets []Budget) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	if err := s.store.ReplaceConfigBudgets(ctx, budgets); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) Budgets() []Budget {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Budget(nil), s.budgets...)
}

func (s *Service) Settings() Settings {
	if s == nil {
		return DefaultSettings()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *Service) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	if s == nil || s.store == nil {
		return Settings{}, ErrUnavailable
	}
	saved, err := s.store.SaveSettings(ctx, settings)
	if err != nil {
		return Settings{}, err
	}
	if err := s.Refresh(ctx); err != nil {
		return saved, fmt.Errorf("refresh budget service after saving settings: %w", err)
	}
	return saved, nil
}

// Statuses evaluates every configured budget without enforcing limits.
func (s *Service) Statuses(ctx context.Context, now time.Time) ([]CheckResult, error) {
	return s.statusesMatching(ctx, nil, now)
}

// StatusesFor evaluates every budget covering the request subjects without
// enforcing limits. Unlike CheckWithResults it never stops at an exhausted
// budget, so callers get the full status picture even when several are
// exceeded.
func (s *Service) StatusesFor(ctx context.Context, subjects Subjects, now time.Time) ([]CheckResult, error) {
	return s.statusesMatching(ctx, &subjects, now)
}

// statusesMatching evaluates the budgets covering subjects, or all of them
// when subjects is nil.
func (s *Service) statusesMatching(ctx context.Context, subjects *Subjects, now time.Time) ([]CheckResult, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	matching, settings, now, err := s.match(subjects, now)
	if err != nil {
		return nil, err
	}
	return s.evaluate(ctx, matching, now, settings)
}

func (s *Service) ResetBudget(ctx context.Context, scope Scope, subject string, periodSeconds int64, at time.Time) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	scope, subject, err := normalizeBudgetKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := s.store.ResetBudget(ctx, scope, subject, periodSeconds, at.UTC()); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) ResetAll(ctx context.Context, at time.Time) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := s.store.ResetAllBudgets(ctx, at.UTC()); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) Check(ctx context.Context, subjects Subjects, now time.Time) error {
	_, err := s.CheckWithResults(ctx, subjects, now)
	return err
}

// CheckWithResults evaluates every budget covering the request subjects and
// stops at the first exhausted one, returning it as an ExceededError alongside
// the results evaluated so far.
func (s *Service) CheckWithResults(ctx context.Context, subjects Subjects, now time.Time) ([]CheckResult, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	matching, settings, now, err := s.match(&subjects, now)
	if err != nil {
		return nil, err
	}
	if len(matching) == 0 {
		return nil, nil
	}
	results, err := s.evaluate(ctx, matching, now, settings)
	if err != nil {
		return results, err
	}
	for i, result := range results {
		if result.HasUsage && result.Spent >= result.Budget.Amount {
			return results[:i+1], &ExceededError{Result: result}
		}
	}
	return results, nil
}

// match snapshots the budgets covering subjects — or all of them when subjects
// is nil — along with the reset settings and the normalized evaluation time.
func (s *Service) match(subjects *Subjects, now time.Time) ([]Budget, Settings, time.Time, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	// Refresh publishes a freshly built slice rather than mutating the current
	// one, so the header read under the lock stays valid afterwards and the
	// matching pass needs no copy.
	s.mu.RLock()
	budgets := s.budgets
	settings := s.settings
	s.mu.RUnlock()

	if subjects == nil {
		return slices.Clone(budgets), settings, now, nil
	}
	userPath, err := NormalizeUserPath(subjects.UserPath)
	if err != nil {
		return nil, settings, now, err
	}
	resolved := Subjects{UserPath: userPath, Labels: subjects.Labels}

	matching := make([]Budget, 0, len(budgets))
	for _, budget := range budgets {
		if budget.appliesTo(resolved) {
			matching = append(matching, budget)
		}
	}
	return matching, settings, now, nil
}

// evaluate resolves the active period of every budget and asks the store for
// all their spends in one round trip. Enforcement runs on every request, so the
// batched lookup is what keeps a wide match set from costing a query each.
func (s *Service) evaluate(ctx context.Context, budgets []Budget, now time.Time, settings Settings) ([]CheckResult, error) {
	if len(budgets) == 0 {
		return []CheckResult{}, nil
	}
	results := make([]CheckResult, len(budgets))
	windows := make([]SpendWindow, len(budgets))
	for i, budget := range budgets {
		start, end := PeriodBounds(now, budget.PeriodSeconds, settings)
		if budget.LastResetAt != nil && budget.LastResetAt.After(start) {
			start = budget.LastResetAt.UTC()
		}
		results[i] = CheckResult{Budget: budget, PeriodStart: start, PeriodEnd: end}
		windows[i] = SpendWindow{
			Scope:   budget.Scope,
			Subject: budget.Subject,
			Start:   start,
			End:     now,
		}
	}

	spends, err := s.store.SumSpend(ctx, windows)
	if err != nil {
		return nil, err
	}
	if len(spends) != len(windows) {
		return nil, fmt.Errorf("budget store returned %d spends for %d windows", len(spends), len(windows))
	}
	for i, spend := range spends {
		results[i].Spent = spend.Total
		results[i].HasUsage = spend.HasUsage
		results[i].Remaining = results[i].Budget.Amount - spend.Total
	}
	return results, nil
}
