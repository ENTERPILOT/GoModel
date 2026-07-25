package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// BudgetsConfig holds per-user-path spend limits.
type BudgetsConfig struct {
	// Enabled controls whether budget checks are active.
	// Default: true. Requires usage tracking because spend limits are evaluated
	// from usage cost records.
	Enabled bool `yaml:"enabled" env:"BUDGETS_ENABLED"`

	// UserPaths declares budget limits by tracked user path.
	UserPaths []BudgetUserPathConfig `yaml:"user_paths"`

	// Labels declares budget limits by request label. A request carrying
	// several labels is charged against every matching label budget.
	//
	// Labels have no env-var form: they are matched verbatim and may contain
	// characters and casing that env var names cannot express. Declare them
	// here or through the admin API.
	Labels []BudgetLabelConfig `yaml:"labels"`
}

// BudgetUserPathConfig declares one or more budget limits for a user path.
type BudgetUserPathConfig struct {
	Path   string              `yaml:"path"`
	Limits []BudgetLimitConfig `yaml:"limits"`
}

// BudgetLabelConfig declares one or more budget limits for a request label.
type BudgetLabelConfig struct {
	Label  string              `yaml:"label"`
	Limits []BudgetLimitConfig `yaml:"limits"`
}

// BudgetLimitConfig declares one spend limit for a reset period.
// The json tags support the JSON-array form of SET_BUDGET_* env values.
type BudgetLimitConfig struct {
	// Period accepts hourly, daily, weekly, or monthly. The resolved period is
	// persisted as PeriodSeconds in the database.
	Period string `yaml:"period" json:"period"`

	// PeriodSeconds can be set directly instead of Period. Standard values are
	// 3600, 86400, 604800, and 2592000.
	PeriodSeconds int64 `yaml:"period_seconds" json:"period_seconds"`

	// Amount is the maximum allowed tracked provider spend for the period.
	Amount float64 `yaml:"amount" json:"amount"`
}

func applyBudgetEnv(cfg *Config, strict bool) error {
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
		func(raw string) ([]BudgetLimitConfig, error) { return parseBudgetEnvLimits(raw, strict) },
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

func parseBudgetEnvLimits(raw string, strict bool) ([]BudgetLimitConfig, error) {
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
		if err := decodeIaCJSON("SET_BUDGET_*", raw, &limits, strict); err != nil {
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
		field := fmt.Sprintf("budgets.user_paths[%d]", pathIdx)
		if strings.TrimSpace(entry.Path) == "" {
			return fmt.Errorf("%s.path is required", field)
		}
		normalizedPath, err := core.NormalizeUserPath(entry.Path)
		if err != nil {
			return fmt.Errorf("%s.path is invalid: %w", field, err)
		}
		if normalizedPath == "" {
			return fmt.Errorf("%s.path is required", field)
		}
		cfg.UserPaths[pathIdx].Path = normalizedPath
		if err := validateBudgetLimits(cfg.UserPaths[pathIdx].Limits, field, "user_path", normalizedPath, seen); err != nil {
			return err
		}
	}
	for labelIdx, entry := range cfg.Labels {
		field := fmt.Sprintf("budgets.labels[%d]", labelIdx)
		label := strings.TrimSpace(entry.Label)
		if label == "" {
			return fmt.Errorf("%s.label is required", field)
		}
		cfg.Labels[labelIdx].Label = label
		if err := validateBudgetLimits(cfg.Labels[labelIdx].Limits, field, "label", label, seen); err != nil {
			return err
		}
	}
	return nil
}

// validateBudgetLimits validates and canonicalizes the limits of one budget
// subject in place, rejecting a period declared twice for the same subject.
func validateBudgetLimits(limits []BudgetLimitConfig, field, scope, subject string, seen map[string]struct{}) error {
	for limitIdx, limit := range limits {
		if math.IsNaN(limit.Amount) || math.IsInf(limit.Amount, 0) || limit.Amount <= 0 {
			return fmt.Errorf("%s.limits[%d].amount must be a finite number greater than 0", field, limitIdx)
		}
		seconds := limit.PeriodSeconds
		if seconds <= 0 {
			parsed, ok := budgetPeriodSeconds(limit.Period)
			if !ok {
				return fmt.Errorf("%s.limits[%d].period must be one of hourly, daily, weekly, monthly or period_seconds must be set", field, limitIdx)
			}
			seconds = parsed
			limits[limitIdx].PeriodSeconds = seconds
		}
		key := scope + "\x00" + subject + "\x00" + strconv.FormatInt(seconds, 10)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate budget for %s %s period %d", scope, subject, seconds)
		}
		seen[key] = struct{}{}
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
