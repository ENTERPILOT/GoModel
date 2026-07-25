package budget

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotFound = errors.New("budget not found")

// SpendWindow asks for the tracked spend of one budget subject over one time
// window.
type SpendWindow struct {
	Scope   Scope
	Subject string
	Start   time.Time
	End     time.Time
}

// Spend is the total tracked cost for one SpendWindow. HasUsage is false when
// the window matched no priced usage at all, which keeps a budget with no
// traffic from ever blocking.
type Spend struct {
	Total    float64
	HasUsage bool
}

// spendBounds returns the union of every window's time range, which is the
// range a batched spend query has to scan.
func spendBounds(windows []SpendWindow) (time.Time, time.Time) {
	start, end := windows[0].Start, windows[0].End
	for _, window := range windows[1:] {
		if window.Start.Before(start) {
			start = window.Start
		}
		if window.End.After(end) {
			end = window.End
		}
	}
	return start, end
}

// Store persists budget definitions, reset settings, and spend lookups.
type Store interface {
	ListBudgets(ctx context.Context) ([]Budget, error)
	UpsertBudgets(ctx context.Context, budgets []Budget) error
	DeleteBudget(ctx context.Context, scope Scope, subject string, periodSeconds int64) error
	ReplaceConfigBudgets(ctx context.Context, budgets []Budget) error
	GetSettings(ctx context.Context) (Settings, error)
	SaveSettings(ctx context.Context, settings Settings) (Settings, error)
	ResetBudget(ctx context.Context, scope Scope, subject string, periodSeconds int64, at time.Time) error
	ResetAllBudgets(ctx context.Context, at time.Time) error

	// SumSpend totals uncached spend for every window in one round trip, in
	// the order given. Budget enforcement runs on every request, so the whole
	// matching set is asked for at once rather than one query per budget.
	SumSpend(ctx context.Context, windows []SpendWindow) ([]Spend, error)

	Close() error
}

// normalizeBudgetKey validates and canonicalizes the identity of one budget.
func normalizeBudgetKey(scope Scope, subject string, periodSeconds int64) (Scope, string, error) {
	scope, err := NormalizeScope(string(scope))
	if err != nil {
		return "", "", err
	}
	subject, err = NormalizeSubject(scope, subject)
	if err != nil {
		return "", "", err
	}
	if periodSeconds <= 0 {
		return "", "", fmt.Errorf("period_seconds must be greater than 0")
	}
	return scope, subject, nil
}

func normalizeBudgetsForUpsert(budgets []Budget) ([]Budget, error) {
	if len(budgets) == 0 {
		return nil, nil
	}
	normalized := make([]Budget, 0, len(budgets))
	seen := make(map[string]int, len(budgets))
	for _, budget := range budgets {
		item, err := NormalizeBudget(budget)
		if err != nil {
			return nil, err
		}
		key := budgetKey(item.Scope, item.Subject, item.PeriodSeconds)
		if existing, ok := seen[key]; ok {
			normalized[existing] = item
			continue
		}
		seen[key] = len(normalized)
		normalized = append(normalized, item)
	}
	return normalized, nil
}

func budgetKey(scope Scope, subject string, periodSeconds int64) string {
	return string(scope) + "\x00" + strings.TrimSpace(subject) + "\x00" + fmt.Sprint(periodSeconds)
}

func normalizeLoadedSettings(settings Settings) (Settings, error) {
	defaults := DefaultSettings()
	if settings.MonthlyResetDay == 0 {
		settings.MonthlyResetDay = defaults.MonthlyResetDay
	}
	if settings.UpdatedAt.IsZero() {
		settings.UpdatedAt = time.Now().UTC()
	}
	if err := ValidateSettings(settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}
