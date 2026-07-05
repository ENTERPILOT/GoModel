package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"

	"gomodel/internal/core"
)

// BudgetsConfig holds per-user-path spend limits.
type BudgetsConfig struct {
	// Enabled controls whether budget checks are active.
	// Default: true. Requires usage tracking because spend limits are evaluated
	// from usage cost records.
	Enabled bool `yaml:"enabled" env:"BUDGETS_ENABLED"`

	// UserPaths declares budget limits by tracked user path.
	UserPaths []BudgetUserPathConfig `yaml:"user_paths"`
}

// BudgetUserPathConfig declares one or more budget limits for a user path.
type BudgetUserPathConfig struct {
	Path   string              `yaml:"path"`
	Limits []BudgetLimitConfig `yaml:"limits"`
}

// BudgetLimitConfig declares one spend limit for a reset period.
type BudgetLimitConfig struct {
	// Period accepts hourly, daily, weekly, or monthly. The resolved period is
	// persisted as PeriodSeconds in the database.
	Period string `yaml:"period"`

	// PeriodSeconds can be set directly instead of Period. Standard values are
	// 3600, 86400, 604800, and 2592000.
	PeriodSeconds int64 `yaml:"period_seconds"`

	// Amount is the maximum allowed tracked provider spend for the period.
	Amount float64 `yaml:"amount"`
}

func applyBudgetEnv(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if !cfg.Budgets.Enabled {
		return nil
	}
	entries, err := applyUserPathLimitEnv(
		cfg.Budgets.UserPaths,
		"SET_BUDGET_",
		func(entry BudgetUserPathConfig) string { return entry.Path },
		parseBudgetEnvLimits,
		func(path string, limits []BudgetLimitConfig) BudgetUserPathConfig {
			return BudgetUserPathConfig{Path: path, Limits: limits}
		},
	)
	if err != nil {
		return err
	}
	cfg.Budgets.UserPaths = entries
	return nil
}

func parseBudgetEnvLimits(raw string) ([]BudgetLimitConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "{") {
		values := map[string]float64{}
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, err
		}
		limits := make([]BudgetLimitConfig, 0, len(values))
		periods := make([]string, 0, len(values))
		for period := range values {
			periods = append(periods, period)
		}
		sort.Strings(periods)
		for _, period := range periods {
			limits = append(limits, BudgetLimitConfig{Period: period, Amount: values[period]})
		}
		return limits, nil
	}
	if strings.HasPrefix(raw, "[") {
		var limits []BudgetLimitConfig
		if err := json.Unmarshal([]byte(raw), &limits); err != nil {
			return nil, err
		}
		return limits, nil
	}

	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})
	limits := make([]BudgetLimitConfig, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		period, amountText, ok := strings.Cut(field, "=")
		if !ok {
			period, amountText, ok = strings.Cut(field, ":")
		}
		if !ok {
			return nil, fmt.Errorf("budget limit %q must use period=amount", field)
		}
		amount, err := strconv.ParseFloat(strings.TrimSpace(amountText), 64)
		if err != nil {
			return nil, fmt.Errorf("budget amount %q is not a valid number", amountText)
		}
		limits = append(limits, BudgetLimitConfig{
			Period: strings.TrimSpace(period),
			Amount: amount,
		})
	}
	return limits, nil
}

func validateBudgetConfig(cfg *BudgetsConfig) error {
	if cfg == nil {
		return nil
	}
	if !cfg.Enabled {
		return nil
	}
	seen := make(map[string]struct{})
	for pathIdx, entry := range cfg.UserPaths {
		if strings.TrimSpace(entry.Path) == "" {
			return fmt.Errorf("budgets.user_paths[%d].path is required", pathIdx)
		}
		normalizedPath, err := core.NormalizeUserPath(entry.Path)
		if err != nil {
			return fmt.Errorf("budgets.user_paths[%d].path is invalid: %w", pathIdx, err)
		}
		if normalizedPath == "" {
			return fmt.Errorf("budgets.user_paths[%d].path is required", pathIdx)
		}
		cfg.UserPaths[pathIdx].Path = normalizedPath
		for limitIdx, limit := range entry.Limits {
			if math.IsNaN(limit.Amount) || math.IsInf(limit.Amount, 0) || limit.Amount <= 0 {
				return fmt.Errorf("budgets.user_paths[%d].limits[%d].amount must be a finite number greater than 0", pathIdx, limitIdx)
			}
			seconds := limit.PeriodSeconds
			if limit.PeriodSeconds <= 0 {
				parsed, ok := budgetPeriodSeconds(limit.Period)
				if !ok {
					return fmt.Errorf("budgets.user_paths[%d].limits[%d].period must be one of hourly, daily, weekly, monthly or period_seconds must be set", pathIdx, limitIdx)
				}
				seconds = parsed
				cfg.UserPaths[pathIdx].Limits[limitIdx].PeriodSeconds = seconds
			}
			key := normalizedPath + ":" + strconv.FormatInt(seconds, 10)
			if _, ok := seen[key]; ok {
				return fmt.Errorf("duplicate budget for path %s period %d", normalizedPath, seconds)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func applyBudgetDependencies(cfg *Config) {
	if cfg == nil || !cfg.Budgets.Enabled || cfg.Usage.Enabled {
		return
	}
	cfg.Budgets.Enabled = false
	slog.Warn("budget management disabled because usage tracking is disabled",
		"usage_enabled", false,
		"budgets_enabled", false,
		"hint", "enable usage tracking to use budgets, or set BUDGETS_ENABLED=false to silence this warning",
	)
}

func budgetPeriodSeconds(period string) (int64, bool) {
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "hour", "hourly", "hours":
		return 3600, true
	case "day", "daily", "days":
		return 86400, true
	case "week", "weekly", "weeks":
		return 604800, true
	case "month", "monthly", "months":
		return 2592000, true
	default:
		return 0, false
	}
}
