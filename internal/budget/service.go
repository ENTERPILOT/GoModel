package budget

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

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
		return nil
	}
	budgets, err := s.store.ListBudgets(ctx)
	if err != nil {
		return err
	}
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	sort.SliceStable(budgets, func(i, j int) bool {
		if budgets[i].UserPath == budgets[j].UserPath {
			return budgets[i].PeriodSeconds < budgets[j].PeriodSeconds
		}
		return budgets[i].UserPath < budgets[j].UserPath
	})
	s.mu.Lock()
	s.budgets = budgets
	s.settings = settings
	s.mu.Unlock()
	return nil
}

func (s *Service) UpsertBudgets(ctx context.Context, budgets []Budget) error {
	if s == nil || s.store == nil {
		return nil
	}
	if err := s.store.UpsertBudgets(ctx, budgets); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) ReplaceConfigBudgets(ctx context.Context, budgets []Budget) error {
	if s == nil || s.store == nil {
		return nil
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
		return Settings{}, fmt.Errorf("budget service is unavailable")
	}
	saved, err := s.store.SaveSettings(ctx, settings)
	if err != nil {
		return Settings{}, err
	}
	if err := s.Refresh(ctx); err != nil {
		return Settings{}, err
	}
	return saved, nil
}

func (s *Service) ResetAll(ctx context.Context, at time.Time) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("budget service is unavailable")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := s.store.ResetAllBudgets(ctx, at.UTC()); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) Check(ctx context.Context, userPath string, now time.Time) error {
	_, err := s.CheckWithResults(ctx, userPath, now)
	return err
}

func (s *Service) CheckWithResults(ctx context.Context, userPath string, now time.Time) ([]CheckResult, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	s.mu.RLock()
	budgets := append([]Budget(nil), s.budgets...)
	settings := s.settings
	s.mu.RUnlock()
	if len(budgets) == 0 {
		return nil, nil
	}

	results := make([]CheckResult, 0)
	for _, budget := range budgets {
		if !budgetAppliesToPath(budget.UserPath, userPath) {
			continue
		}
		start, end := PeriodBounds(now, budget.PeriodSeconds, settings)
		if budget.LastResetAt != nil && budget.LastResetAt.After(start) {
			start = budget.LastResetAt.UTC()
		}
		spent, _, err := s.store.SumUsageCost(ctx, budget.UserPath, start, now)
		if err != nil {
			return results, err
		}
		result := CheckResult{
			Budget:      budget,
			PeriodStart: start,
			PeriodEnd:   end,
			Spent:       spent,
			Remaining:   budget.Amount - spent,
		}
		results = append(results, result)
		if spent >= budget.Amount {
			return results, &ExceededError{Result: result}
		}
	}
	return results, nil
}

func budgetAppliesToPath(budgetPath, requestPath string) bool {
	budgetPath = strings.TrimSpace(budgetPath)
	requestPath = strings.TrimSpace(requestPath)
	if budgetPath == "/" {
		return true
	}
	return requestPath == budgetPath || strings.HasPrefix(requestPath, budgetPath+"/")
}
