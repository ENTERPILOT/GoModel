package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// RetryConfig holds resolved retry settings for an LLM client.
// This is the canonical type shared between config and llmclient.
type RetryConfig struct {
	// RetryOnStatuses uses defaults when nil; an empty list disables status triggers.
	RetryOnStatuses []string      `yaml:"retry_on_statuses" env:"RETRY_ON_STATUSES"`
	MaxRetries      int           `yaml:"max_retries"     env:"RETRY_MAX_RETRIES"`
	InitialBackoff  time.Duration `yaml:"initial_backoff" env:"RETRY_INITIAL_BACKOFF"`
	MaxBackoff      time.Duration `yaml:"max_backoff"     env:"RETRY_MAX_BACKOFF"`
	BackoffFactor   float64       `yaml:"backoff_factor"  env:"RETRY_BACKOFF_FACTOR"`
	JitterFactor    float64       `yaml:"jitter_factor"   env:"RETRY_JITTER_FACTOR"`
}

// DefaultRetryConfig returns the default retry settings.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		RetryOnStatuses: []string{"429", "502", "503", "504", "522", "524"},
		MaxRetries:      3,
		InitialBackoff:  1 * time.Second,
		MaxBackoff:      30 * time.Second,
		BackoffFactor:   2.0,
		JitterFactor:    0.1,
	}
}

// TripRuleConfig opens the circuit breaker instantly when an upstream error
// message matches Match. A zero TTL uses the breaker's open-state timeout.
// TTL encodes as JSON nanoseconds wherever the rule crosses the admin/store
// wire as {match, ttl}.
//
// Name identifies the group the rule belongs to. It comes from the YAML map
// key in `trip_on` blocks and from env group names; it only matters while
// merging config with env overrides and is empty elsewhere.
type TripRuleConfig struct {
	Name  string        `yaml:"-" json:"name,omitempty"`
	Match string        `yaml:"match" json:"match"`
	TTL   time.Duration `yaml:"ttl" json:"ttl"`
}

// TripRuleMap holds named trip rules the way operators configure them:
// docker-compose style groups keyed by name. The yaml shape is
//
//	trip_on:
//	  weekly_quota:
//	    match: "weekly usage limit"
//	    ttl: 4h
//
// Unmarshal injects the key into each rule's Name; resolution turns the map
// into TripRuleMap.List() so evaluation order is deterministic.
type TripRuleMap map[string]TripRuleConfig

// UnmarshalYAML accepts only the named map form. Each rule inherits the key
// as its Name; an empty key is a config error.
func (m *TripRuleMap) UnmarshalYAML(unmarshal func(any) error) error {
	var raw map[string]TripRuleConfig
	if err := unmarshal(&raw); err != nil {
		return fmt.Errorf("trip_on must be a map of name -> {match, ttl}: %w", err)
	}
	for name, rule := range raw {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("trip_on group names must not be empty")
		}
		rule.Name = name
		raw[name] = rule
	}
	*m = TripRuleMap(raw)
	return nil
}

// TripRuleMapFromList converts a name-carrying rule list back into a named
// map. A rule with an empty name gets a generated key (`rule_N`) so unnamed
// entries never collide with named ones.
func TripRuleMapFromList(rules []TripRuleConfig) TripRuleMap {
	m := make(TripRuleMap, len(rules))
	unnamed := 0
	for _, rule := range rules {
		name := rule.Name
		if name == "" {
			unnamed++
			name = fmt.Sprintf("rule_%d", unnamed)
		}
		m[name] = rule
	}
	return m
}

// List returns the rules with their names injected, ordered by group name so
// "first matching rule wins" stays deterministic no matter how the map was
// assembled (yaml, env, or merged).
func (m TripRuleMap) List() []TripRuleConfig {
	rules := make([]TripRuleConfig, 0, len(m))
	for name, rule := range m {
		rule.Name = name
		rules = append(rules, rule)
	}
	slices.SortFunc(rules, func(a, b TripRuleConfig) int { return strings.Compare(a.Name, b.Name) })
	return rules
}

// MergeTripRuleGroups returns the merged rule set: an incoming group replaces
// the existing rule with the same name (case-insensitive); other existing
// rules survive; new groups append. The result is ordered by name.
func MergeTripRuleGroups(existing []TripRuleConfig, groups []TripRuleConfig) []TripRuleConfig {
	merged := make(map[string]TripRuleConfig, len(existing)+len(groups))
	for _, rule := range existing {
		merged[strings.ToLower(rule.Name)] = rule
	}
	for _, group := range groups {
		merged[strings.ToLower(group.Name)] = group
	}
	rules := make([]TripRuleConfig, 0, len(merged))
	for _, rule := range merged {
		rules = append(rules, rule)
	}
	slices.SortFunc(rules, func(a, b TripRuleConfig) int { return strings.Compare(a.Name, b.Name) })
	return rules
}

// CollectTripRuleGroups reads `env` entries for `<PREFIX>_<GROUP>_MATCH` and
// `<PREFIX>_<GROUP>_TTL` pairs. A group must carry MATCH; TTL is an optional
// Go duration (zero uses the breaker timeout). Entries with a different
// prefix or no suffix are ignored. Groups are returned ordered by name.
func CollectTripRuleGroups(environ []string, prefix string) ([]TripRuleConfig, error) {
	matchKey := prefix + "_"
	groups := make(map[string]TripRuleConfig)
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" || !strings.HasPrefix(key, matchKey) {
			continue
		}
		// The attribute is the LAST underscore segment; group names may
		// contain underscores themselves.
		rest := key[len(matchKey):]
		i := strings.LastIndex(rest, "_")
		if i < 1 {
			continue
		}
		name, attr := rest[:i], rest[i+1:]
		if name == "" {
			continue
		}
		rule := groups[name]
		rule.Name = name
		switch attr {
		case "MATCH":
			rule.Match = value
		case "TTL":
			ttl, err := time.ParseDuration(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("%s: invalid ttl %q: %w", key, value, err)
			}
			rule.TTL = ttl
		}
		groups[name] = rule
	}
	rules := make([]TripRuleConfig, 0, len(groups))
	for _, rule := range groups {
		if rule.Match == "" {
			return nil, fmt.Errorf("%s_%s_MATCH: group declares a TTL but no match pattern", prefix, rule.Name)
		}
		rules = append(rules, rule)
	}
	slices.SortFunc(rules, func(a, b TripRuleConfig) int { return strings.Compare(a.Name, b.Name) })
	return rules, nil
}

// CircuitBreakerConfig holds resolved circuit breaker settings.
// This is the canonical type shared between config and llmclient.
type CircuitBreakerConfig struct {
	// FailureOnStatuses uses defaults when nil; an empty list disables status triggers.
	FailureOnStatuses []string `yaml:"failure_on_statuses" env:"CIRCUIT_BREAKER_FAILURE_ON_STATUSES"`
	Scope             string   `yaml:"scope" env:"CIRCUIT_BREAKER_SCOPE"`
	// Enabled switches the circuit breaker on or off. When false, requests are
	// never short-circuited regardless of the thresholds below.
	// Default: true
	Enabled          bool             `yaml:"enabled"           env:"CIRCUIT_BREAKER_ENABLED"`
	FailureThreshold int              `yaml:"failure_threshold" env:"CIRCUIT_BREAKER_FAILURE_THRESHOLD"`
	SuccessThreshold int              `yaml:"success_threshold" env:"CIRCUIT_BREAKER_SUCCESS_THRESHOLD"`
	Timeout          time.Duration    `yaml:"timeout"           env:"CIRCUIT_BREAKER_TIMEOUT"`
	TripOn           TripRuleMap `yaml:"trip_on"`
}

// DefaultCircuitBreakerConfig returns the default circuit breaker settings.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureOnStatuses: []string{"429", "5xx"},
		Scope:             "provider",
		Enabled:           true,
		FailureThreshold:  5,
		SuccessThreshold:  2,
		Timeout:           30 * time.Second,
	}
}

// ResilienceConfig holds resolved resilience settings (retry and circuit breaker).
type ResilienceConfig struct {
	Retry          RetryConfig          `yaml:"retry"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
}

// RawResilienceConfig holds optional per-provider resilience overrides from YAML.
// Nil fields inherit from the global ResilienceConfig.
type RawResilienceConfig struct {
	Retry          *RawRetryConfig          `yaml:"retry"`
	CircuitBreaker *RawCircuitBreakerConfig `yaml:"circuit_breaker"`
}

// RawCircuitBreakerConfig holds optional per-provider circuit breaker overrides from YAML.
// Nil fields inherit from the global CircuitBreakerConfig.
type RawCircuitBreakerConfig struct {
	FailureOnStatuses []string         `yaml:"failure_on_statuses"`
	Scope             *string          `yaml:"scope"`
	Enabled           *bool            `yaml:"enabled"`
	FailureThreshold  *int             `yaml:"failure_threshold"`
	SuccessThreshold  *int             `yaml:"success_threshold"`
	Timeout           *time.Duration   `yaml:"timeout"`
	TripOn            TripRuleMap `yaml:"trip_on"`
}

// RawRetryConfig holds optional per-provider retry overrides from YAML.
// Nil fields inherit from the global RetryConfig.
type RawRetryConfig struct {
	RetryOnStatuses []string       `yaml:"retry_on_statuses"`
	MaxRetries      *int           `yaml:"max_retries"`
	InitialBackoff  *time.Duration `yaml:"initial_backoff"`
	MaxBackoff      *time.Duration `yaml:"max_backoff"`
	BackoffFactor   *float64       `yaml:"backoff_factor"`
	JitterFactor    *float64       `yaml:"jitter_factor"`
}
