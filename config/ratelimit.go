package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// RateLimitsConfig holds request, token, and concurrency limits scoped to
// user paths (consumers), providers, and models.
type RateLimitsConfig struct {
	// Enabled controls whether rate limit checks are active.
	// Default: true. With no rules configured the check is a no-op.
	Enabled bool `yaml:"enabled" env:"RATE_LIMITS_ENABLED"`

	// UserPaths declares rate limit rules by tracked user path.
	UserPaths []RateLimitUserPathConfig `yaml:"user_paths"`

	// Providers declares rate limit rules by configured provider name.
	// Provider rules cap all traffic routed to that provider instance; load
	// balancing and failover skip a saturated provider while capacity exists
	// elsewhere.
	Providers []RateLimitProviderConfig `yaml:"providers"`

	// Models declares rate limit rules by model. A provider-qualified subject
	// ("openai/gpt-4o") caps one provider's model; a bare id ("gpt-4o") caps
	// the model across every provider.
	Models []RateLimitModelConfig `yaml:"models"`
}

// RateLimitUserPathConfig declares one or more rate limit rules for a user path.
type RateLimitUserPathConfig struct {
	Path   string                `yaml:"path"`
	Limits []RateLimitRuleConfig `yaml:"limits"`
}

// RateLimitProviderConfig declares one or more rate limit rules for a provider.
type RateLimitProviderConfig struct {
	Name   string                `yaml:"name"`
	Limits []RateLimitRuleConfig `yaml:"limits"`
}

// RateLimitModelConfig declares one or more rate limit rules for a model.
type RateLimitModelConfig struct {
	Model  string                `yaml:"model"`
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

func applyRateLimitEnv(cfg *Config, strict bool) error {
	if cfg == nil {
		return nil
	}
	if !cfg.RateLimits.Enabled {
		return nil
	}
	parseLimits := func(raw string) ([]RateLimitRuleConfig, error) {
		return parseRateLimitEnvLimits(raw, strict)
	}
	entries, err := applyUserPathLimitEnv(
		cfg.RateLimits.UserPaths,
		"SET_RATE_LIMIT_",
		func(entry RateLimitUserPathConfig) string { return entry.Path },
		parseLimits,
		func(path string, limits []RateLimitRuleConfig) RateLimitUserPathConfig {
			return RateLimitUserPathConfig{Path: path, Limits: limits}
		},
	)
	if err != nil {
		return err
	}
	cfg.RateLimits.UserPaths = entries

	// SET_PROVIDER_RATE_LIMIT_<NAME> uses its own prefix so provider names
	// never collide with the SET_RATE_LIMIT_* user-path suffix space. Model
	// rules have no env form (model ids are not env-name safe); declare them
	// in YAML or the admin API.
	providers, err := applyKeyedLimitEnv(
		cfg.RateLimits.Providers,
		"SET_PROVIDER_RATE_LIMIT_",
		rateLimitProviderNameFromEnvSuffix,
		normalizeRateLimitProviderName,
		func(entry RateLimitProviderConfig) string { return entry.Name },
		parseLimits,
		func(name string, limits []RateLimitRuleConfig) RateLimitProviderConfig {
			return RateLimitProviderConfig{Name: name, Limits: limits}
		},
	)
	if err != nil {
		return err
	}
	cfg.RateLimits.Providers = providers
	return nil
}

// rateLimitProviderNameFromEnvSuffix follows the provider-instance env
// convention: underscores in the suffix become hyphens in the provider name
// (OPENAI_EAST -> openai-east).
func rateLimitProviderNameFromEnvSuffix(suffix string) (string, error) {
	return normalizeRateLimitProviderName(strings.ReplaceAll(suffix, "_", "-"))
}

func normalizeRateLimitProviderName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", fmt.Errorf("provider name is required")
	}
	return name, nil
}

// parseRateLimitEnvLimits parses either a JSON array of rule objects or the
// compact "rpm=100,tpm=50000,rpd=1000,concurrent=10" syntax.
func parseRateLimitEnvLimits(raw string, strict bool) ([]RateLimitRuleConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var limits []RateLimitRuleConfig
		if err := decodeIaCJSON("SET_RATE_LIMIT_*", raw, &limits, strict); err != nil {
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

// Rate limit period lengths, mirrored from the internal/ratelimit package
// (config cannot import it without a cycle).
const (
	rateLimitMinuteSeconds     int64 = 60
	rateLimitHourSeconds       int64 = 3600
	rateLimitDaySeconds        int64 = 86400
	rateLimitConcurrentSeconds int64 = 0
)

func rateLimitEnvName(name string) (periodSeconds int64, isTokens bool, err error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "rpm":
		return rateLimitMinuteSeconds, false, nil
	case "tpm":
		return rateLimitMinuteSeconds, true, nil
	case "rph":
		return rateLimitHourSeconds, false, nil
	case "tph":
		return rateLimitHourSeconds, true, nil
	case "rpd":
		return rateLimitDaySeconds, false, nil
	case "tpd":
		return rateLimitDaySeconds, true, nil
	case "concurrent", "concurrency":
		return rateLimitConcurrentSeconds, false, nil
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
		context := fmt.Sprintf("rate_limits.user_paths[%d]", pathIdx)
		if err := validateRateLimitLimits(context, "user_path:"+normalizedPath, cfg.UserPaths[pathIdx].Limits, seen); err != nil {
			return err
		}
	}
	for providerIdx, entry := range cfg.Providers {
		name, err := normalizeRateLimitProviderName(entry.Name)
		if err != nil {
			return fmt.Errorf("rate_limits.providers[%d].name is required", providerIdx)
		}
		if strings.ContainsAny(name, "/ \t") {
			return fmt.Errorf("rate_limits.providers[%d].name must be a provider name without slashes or spaces", providerIdx)
		}
		cfg.Providers[providerIdx].Name = name
		context := fmt.Sprintf("rate_limits.providers[%d]", providerIdx)
		if err := validateRateLimitLimits(context, "provider:"+name, cfg.Providers[providerIdx].Limits, seen); err != nil {
			return err
		}
	}
	for modelIdx, entry := range cfg.Models {
		model := strings.TrimSpace(entry.Model)
		if model == "" {
			return fmt.Errorf("rate_limits.models[%d].model is required", modelIdx)
		}
		cfg.Models[modelIdx].Model = model
		context := fmt.Sprintf("rate_limits.models[%d]", modelIdx)
		if err := validateRateLimitLimits(context, "model:"+strings.ToLower(model), cfg.Models[modelIdx].Limits, seen); err != nil {
			return err
		}
	}
	return nil
}

// validateRateLimitLimits validates one entry's limit list in place (resolving
// named periods to seconds) and rejects duplicate (subject, period) keys.
func validateRateLimitLimits(context, subjectKey string, limits []RateLimitRuleConfig, seen map[string]struct{}) error {
	for limitIdx, limit := range limits {
		seconds, err := rateLimitConfigPeriodSeconds(limit)
		if err != nil {
			return fmt.Errorf("%s.limits[%d]: %w", context, limitIdx, err)
		}
		limits[limitIdx].PeriodSeconds = &seconds
		if limit.MaxRequests != nil && *limit.MaxRequests <= 0 {
			return fmt.Errorf("%s.limits[%d].max_requests must be greater than 0", context, limitIdx)
		}
		if limit.MaxTokens != nil && *limit.MaxTokens <= 0 {
			return fmt.Errorf("%s.limits[%d].max_tokens must be greater than 0", context, limitIdx)
		}
		if seconds == 0 {
			if limit.MaxTokens != nil {
				return fmt.Errorf("%s.limits[%d].max_tokens is not valid for the concurrent period", context, limitIdx)
			}
			if limit.MaxRequests == nil {
				return fmt.Errorf("%s.limits[%d].max_requests is required for the concurrent period", context, limitIdx)
			}
		} else if limit.MaxRequests == nil && limit.MaxTokens == nil {
			return fmt.Errorf("%s.limits[%d] requires max_requests or max_tokens", context, limitIdx)
		}
		key := subjectKey + ":" + strconv.FormatInt(seconds, 10)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate rate limit for %s period %d", subjectKey, seconds)
		}
		seen[key] = struct{}{}
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
		return rateLimitMinuteSeconds, nil
	case "hour", "hours", "hourly":
		return rateLimitHourSeconds, nil
	case "day", "days", "daily":
		return rateLimitDaySeconds, nil
	case "concurrent", "concurrency":
		return rateLimitConcurrentSeconds, nil
	default:
		return 0, fmt.Errorf("period must be one of minute, hour, day, concurrent or period_seconds must be set")
	}
}
