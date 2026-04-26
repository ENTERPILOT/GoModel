package budget

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotFound = errors.New("budget not found")

// Store persists budget definitions, reset settings, and spend lookups.
type Store interface {
	ListBudgets(ctx context.Context) ([]Budget, error)
	UpsertBudgets(ctx context.Context, budgets []Budget) error
	DeleteBudget(ctx context.Context, userPath string, periodSeconds int64) error
	ReplaceConfigBudgets(ctx context.Context, budgets []Budget) error
	GetSettings(ctx context.Context) (Settings, error)
	SaveSettings(ctx context.Context, settings Settings) (Settings, error)
	ResetBudget(ctx context.Context, userPath string, periodSeconds int64, at time.Time) error
	ResetAllBudgets(ctx context.Context, at time.Time) error
	SumUsageCost(ctx context.Context, userPath string, start, end time.Time) (float64, bool, error)
	Close() error
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
		key := budgetKey(item.UserPath, item.PeriodSeconds)
		if existing, ok := seen[key]; ok {
			normalized[existing] = item
			continue
		}
		seen[key] = len(normalized)
		normalized = append(normalized, item)
	}
	return normalized, nil
}

func budgetKey(userPath string, periodSeconds int64) string {
	return strings.TrimSpace(userPath) + ":" + fmt.Sprint(periodSeconds)
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

func usagePathMatchesBudgetExpr(column string) string {
	return "COALESCE(NULLIF(TRIM(" + column + "), ''), '/')"
}

func usagePathLikePattern(userPath string) string {
	if userPath == "/" {
		return "/%"
	}
	return escapeLikeWildcards(userPath) + "/%"
}

func escapeLikeWildcards(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
