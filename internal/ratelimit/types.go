// Package ratelimit enforces per-user-path request, token, and concurrency
// limits for the AI gateway. Rule definitions are persisted; live counters are
// in-memory and per instance.
package ratelimit

import (
	"fmt"
	"strings"
	"time"

	"gomodel/internal/core"
)

const (
	PeriodMinuteSeconds int64 = 60
	PeriodHourSeconds   int64 = 3600
	PeriodDaySeconds    int64 = 86400
	// PeriodConcurrent marks a window-less rule: MaxRequests caps in-flight
	// requests instead of requests per period.
	PeriodConcurrent int64 = 0
)

const (
	// SourceConfig marks rules seeded from static configuration.
	SourceConfig = "config"
	// SourceManual marks rules created or changed through admin APIs.
	SourceManual = "manual"
)

// Rule stores the limits for one user path and period.
// A period of PeriodConcurrent caps in-flight requests via MaxRequests.
type Rule struct {
	UserPath      string    `json:"user_path" bson:"user_path"`
	PeriodSeconds int64     `json:"period_seconds" bson:"period_seconds"`
	MaxRequests   *int64    `json:"max_requests,omitempty" bson:"max_requests,omitempty"`
	MaxTokens     *int64    `json:"max_tokens,omitempty" bson:"max_tokens,omitempty"`
	Source        string    `json:"source,omitempty" bson:"source,omitempty"`
	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" bson:"updated_at"`
}

// LimitScope names which limit dimension a check or breach refers to.
type LimitScope string

const (
	ScopeRequests    LimitScope = "requests"
	ScopeTokens      LimitScope = "tokens"
	ScopeConcurrency LimitScope = "concurrency"
)

// ExceededError indicates a rate limit rejected the request.
type ExceededError struct {
	Rule       Rule
	Scope      LimitScope
	Observed   int64
	Limit      int64
	RetryAfter time.Duration
}

func (e *ExceededError) Error() string {
	if e == nil {
		return ""
	}
	label := PeriodLabel(e.Rule.PeriodSeconds)
	switch e.Scope {
	case ScopeTokens:
		return fmt.Sprintf("rate limit exceeded for %s: %s token limit of %d reached", e.Rule.UserPath, label, e.Limit)
	case ScopeConcurrency:
		return fmt.Sprintf("rate limit exceeded for %s: concurrent request limit of %d reached", e.Rule.UserPath, e.Limit)
	default:
		return fmt.Sprintf("rate limit exceeded for %s: %s request limit of %d reached", e.Rule.UserPath, label, e.Limit)
	}
}

// Status reports the live counter state for one rule.
type Status struct {
	Rule              Rule
	WindowStart       time.Time
	WindowEnd         time.Time
	RequestsUsed      int64
	RequestsRemaining *int64
	TokensUsed        int64
	TokensRemaining   *int64
	InFlight          int64
}

// HeaderSnapshot carries the most-constrained matching limits for
// OpenAI-style x-ratelimit-* response headers.
type HeaderSnapshot struct {
	HasRequests       bool
	RequestLimit      int64
	RequestRemaining  int64
	RequestResetAfter time.Duration
	HasTokens         bool
	TokenLimit        int64
	TokenRemaining    int64
	TokenResetAfter   time.Duration
}

func NormalizeUserPath(raw string) (string, error) {
	path, err := core.NormalizeUserPath(raw)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "/", nil
	}
	return path, nil
}

func NormalizeRule(r Rule) (Rule, error) {
	path, err := NormalizeUserPath(r.UserPath)
	if err != nil {
		return Rule{}, err
	}
	r.UserPath = path
	if err := validatePeriodSeconds(r.PeriodSeconds); err != nil {
		return Rule{}, err
	}
	if r.MaxRequests != nil && *r.MaxRequests <= 0 {
		return Rule{}, fmt.Errorf("max_requests must be greater than 0")
	}
	if r.MaxTokens != nil && *r.MaxTokens <= 0 {
		return Rule{}, fmt.Errorf("max_tokens must be greater than 0")
	}
	if r.PeriodSeconds == PeriodConcurrent {
		if r.MaxTokens != nil {
			return Rule{}, fmt.Errorf("max_tokens is not valid for the concurrent period")
		}
		if r.MaxRequests == nil {
			return Rule{}, fmt.Errorf("max_requests is required for the concurrent period")
		}
	} else if r.MaxRequests == nil && r.MaxTokens == nil {
		return Rule{}, fmt.Errorf("at least one of max_requests or max_tokens is required")
	}
	r.Source = strings.TrimSpace(r.Source)
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	return r, nil
}

// PeriodSecondsFromName resolves a named period. The bool reports whether the
// name is recognized.
func PeriodSecondsFromName(period string) (int64, bool) {
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "minute", "minutes", "min", "minutely":
		return PeriodMinuteSeconds, true
	case "hour", "hours", "hourly":
		return PeriodHourSeconds, true
	case "day", "days", "daily":
		return PeriodDaySeconds, true
	case "concurrent", "concurrency":
		return PeriodConcurrent, true
	default:
		return 0, false
	}
}

func PeriodLabel(seconds int64) string {
	switch seconds {
	case PeriodConcurrent:
		return "concurrent"
	case PeriodMinuteSeconds:
		return "minute"
	case PeriodHourSeconds:
		return "hour"
	case PeriodDaySeconds:
		return "day"
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// ruleAppliesToPath reports whether a rule path covers the request path,
// using the same subtree semantics as budgets.
func ruleAppliesToPath(rulePath, requestPath string) bool {
	rulePath = strings.TrimSpace(rulePath)
	requestPath = strings.TrimSpace(requestPath)
	if rulePath == "/" {
		return true
	}
	return requestPath == rulePath || strings.HasPrefix(requestPath, rulePath+"/")
}
