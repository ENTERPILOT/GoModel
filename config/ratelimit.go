package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gomodel/internal/core"
)

// RateLimitsConfig holds per-user-path request, token, and concurrency limits.
type RateLimitsConfig struct {
	// Enabled controls whether rate limit checks are active.
	// Default: true. With no rules configured the check is a no-op.
	Enabled bool `yaml:"enabled" env:"RATE_LIMITS_ENABLED"`

	// UserPaths declares rate limit rules by tracked user path.
	UserPaths []RateLimitUserPathConfig `yaml:"user_paths"`
}

// RateLimitUserPathConfig declares one or more rate limit rules for a user path.
type RateLimitUserPathConfig struct {
	Path   string                `yaml:"path"`
	Limits []RateLimitRuleConfig `yaml:"limits"`
}

// RateLimitRuleConfig declares the limits for one period. The json tags
// support the JSON-array form of SET_RATE_LIMIT_* env values.
type RateLimitRuleConfig struct {
	// Period accepts minute, hour, day, or concurrent. The resolved period is
	// persisted as PeriodSeconds in the database.
	Period string `yaml:"period" json:"period"`

	// PeriodSeconds can be set directly instead of Period for custom windows.
	// 0 means the concurrent (in-flight) limit.
	PeriodSeconds *int64 `yaml:"period_seconds" json:"period_seconds"`

	// MaxRequests caps requests per period, or in-flight requests for the
	// concurrent period.
	MaxRequests *int64 `yaml:"max_requests" json:"max_requests"`

	// MaxTokens caps total tokens per period. Not valid for the concurrent
	// period. Requires usage tracking to be enforced.
	MaxTokens *int64 `yaml:"max_tokens" json:"max_tokens"`
}

func applyRateLimitEnv(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if !cfg.RateLimits.Enabled {
		return nil
	}
	entries, err := applyUserPathLimitEnv(
		cfg.RateLimits.UserPaths,
		"SET_RATE_LIMIT_",
		func(entry RateLimitUserPathConfig) string { return entry.Path },
		parseRateLimitEnvLimits,
		func(path string, limits []RateLimitRuleConfig) RateLimitUserPathConfig {
			return RateLimitUserPathConfig{Path: path, Limits: limits}
		},
	)
	if err != nil {
		return err
	}
	cfg.RateLimits.UserPaths = entries
	return nil
}

// parseRateLimitEnvLimits parses either a JSON array of rule objects or the
// compact "rpm=100,tpm=50000,rpd=1000,concurrent=10" syntax.
func parseRateLimitEnvLimits(raw string) ([]RateLimitRuleConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var limits []RateLimitRuleConfig
		if err := json.Unmarshal([]byte(raw), &limits); err != nil {
			return nil, err
		}
		return limits, nil
	}

	// The compact syntax merges request and token caps for the same period
	// into one rule, matching the (path, period) storage key.
	byPeriod := make(map[int64]*RateLimitRuleConfig)
	order := make([]int64, 0, 4)
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		name, valueText, ok := strings.Cut(field, "=")
		if !ok {
			name, valueText, ok = strings.Cut(field, ":")
		}
		if !ok {
			return nil, fmt.Errorf("rate limit %q must use name=value", field)
		}
		value, err := strconv.ParseInt(strings.TrimSpace(valueText), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("rate limit value %q is not a valid integer", valueText)
		}
		periodSeconds, isTokens, err := rateLimitEnvName(name)
		if err != nil {
			return nil, err
		}
		rule, exists := byPeriod[periodSeconds]
		if !exists {
			seconds := periodSeconds
			rule = &RateLimitRuleConfig{PeriodSeconds: &seconds}
			byPeriod[periodSeconds] = rule
			order = append(order, periodSeconds)
		}
		if isTokens {
			rule.MaxTokens = &value
		} else {
			rule.MaxRequests = &value
		}
	}
	limits := make([]RateLimitRuleConfig, 0, len(order))
	for _, periodSeconds := range order {
		limits = append(limits, *byPeriod[periodSeconds])
	}
	return limits, nil
}

func rateLimitEnvName(name string) (periodSeconds int64, isTokens bool, err error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "rpm":
		return 60, false, nil
	case "tpm":
		return 60, true, nil
	case "rph":
		return 3600, false, nil
	case "tph":
		return 3600, true, nil
	case "rpd":
		return 86400, false, nil
	case "tpd":
		return 86400, true, nil
	case "concurrent", "concurrency":
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("rate limit name %q must be one of rpm, tpm, rph, tph, rpd, tpd, concurrent", name)
	}
}

func validateRateLimitConfig(cfg *RateLimitsConfig) error {
	if cfg == nil {
		return nil
	}
	if !cfg.Enabled {
		return nil
	}
	seen := make(map[string]struct{})
	for pathIdx, entry := range cfg.UserPaths {
		if strings.TrimSpace(entry.Path) == "" {
			return fmt.Errorf("rate_limits.user_paths[%d].path is required", pathIdx)
		}
		normalizedPath, err := core.NormalizeUserPath(entry.Path)
		if err != nil {
			return fmt.Errorf("rate_limits.user_paths[%d].path is invalid: %w", pathIdx, err)
		}
		if normalizedPath == "" {
			return fmt.Errorf("rate_limits.user_paths[%d].path is required", pathIdx)
		}
		cfg.UserPaths[pathIdx].Path = normalizedPath
		for limitIdx, limit := range entry.Limits {
			seconds, err := rateLimitConfigPeriodSeconds(limit)
			if err != nil {
				return fmt.Errorf("rate_limits.user_paths[%d].limits[%d]: %w", pathIdx, limitIdx, err)
			}
			cfg.UserPaths[pathIdx].Limits[limitIdx].PeriodSeconds = &seconds
			if limit.MaxRequests != nil && *limit.MaxRequests <= 0 {
				return fmt.Errorf("rate_limits.user_paths[%d].limits[%d].max_requests must be greater than 0", pathIdx, limitIdx)
			}
			if limit.MaxTokens != nil && *limit.MaxTokens <= 0 {
				return fmt.Errorf("rate_limits.user_paths[%d].limits[%d].max_tokens must be greater than 0", pathIdx, limitIdx)
			}
			if seconds == 0 {
				if limit.MaxTokens != nil {
					return fmt.Errorf("rate_limits.user_paths[%d].limits[%d].max_tokens is not valid for the concurrent period", pathIdx, limitIdx)
				}
				if limit.MaxRequests == nil {
					return fmt.Errorf("rate_limits.user_paths[%d].limits[%d].max_requests is required for the concurrent period", pathIdx, limitIdx)
				}
			} else if limit.MaxRequests == nil && limit.MaxTokens == nil {
				return fmt.Errorf("rate_limits.user_paths[%d].limits[%d] requires max_requests or max_tokens", pathIdx, limitIdx)
			}
			key := normalizedPath + ":" + strconv.FormatInt(seconds, 10)
			if _, ok := seen[key]; ok {
				return fmt.Errorf("duplicate rate limit for path %s period %d", normalizedPath, seconds)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func rateLimitConfigPeriodSeconds(limit RateLimitRuleConfig) (int64, error) {
	if limit.PeriodSeconds != nil {
		if *limit.PeriodSeconds < 0 {
			return 0, fmt.Errorf("period_seconds must be 0 (concurrent) or greater")
		}
		return *limit.PeriodSeconds, nil
	}
	switch strings.ToLower(strings.TrimSpace(limit.Period)) {
	case "minute", "minutes", "min", "minutely":
		return 60, nil
	case "hour", "hours", "hourly":
		return 3600, nil
	case "day", "days", "daily":
		return 86400, nil
	case "concurrent", "concurrency":
		return 0, nil
	default:
		return 0, fmt.Errorf("period must be one of minute, hour, day, concurrent or period_seconds must be set")
	}
}
