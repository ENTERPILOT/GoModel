// Package ratelimit enforces request, token, and concurrency limits for the
// AI gateway. Rules are scoped to a consumer user-path subtree, a provider, or
// a model. Rule definitions are persisted; live counters are in-memory and per
// instance.
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

// RuleScope names what a rule limits: a consumer user-path subtree, a
// provider instance, or a model.
type RuleScope string

const (
	// ScopeUserPath limits a consumer subtree; the subject is a user path and
	// covers all its descendants.
	ScopeUserPath RuleScope = "user_path"
	// ScopeProvider limits everything routed to one configured provider
	// instance; the subject is the provider name (e.g. "openai").
	ScopeProvider RuleScope = "provider"
	// ScopeModel limits one model. The subject is a provider-qualified model
	// ("openai/gpt-4o") or a bare model id ("gpt-4o", matching any provider).
	ScopeModel RuleScope = "model"
)

// Rule stores the limits for one scope, subject, and period.
// A period of PeriodConcurrent caps in-flight requests via MaxRequests.
type Rule struct {
	Scope         RuleScope `json:"scope" bson:"scope"`
	Subject       string    `json:"subject" bson:"subject"`
	PeriodSeconds int64     `json:"period_seconds" bson:"period_seconds"`
	MaxRequests   *int64    `json:"max_requests,omitempty" bson:"max_requests,omitempty"`
	MaxTokens     *int64    `json:"max_tokens,omitempty" bson:"max_tokens,omitempty"`
	Source        string    `json:"source,omitempty" bson:"source,omitempty"`
	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" bson:"updated_at"`
}

// Subjects identifies the dimensions one request can be limited by. UserPath
// is always known at ingress; Provider and Model are set once the route is
// resolved (provider name and provider-qualified model).
type Subjects struct {
	UserPath string
	Provider string
	Model    string
}

// appliesTo reports whether the rule covers the request subjects.
func (r Rule) appliesTo(s Subjects) bool {
	switch r.Scope {
	case ScopeProvider:
		return s.Provider != "" && strings.EqualFold(r.Subject, s.Provider)
	case ScopeModel:
		return modelSubjectMatches(r.Subject, s.Provider, s.Model)
	default:
		return s.UserPath != "" && ruleAppliesToPath(r.Subject, s.UserPath)
	}
}

// modelSubjectMatches compares a model rule subject against the routed model.
// The subject may be provider-qualified or bare; the routed model may arrive
// either way too (qualified at admission, bare on usage entries), so both
// spellings are candidates.
func modelSubjectMatches(subject, provider, model string) bool {
	if model == "" {
		return false
	}
	if strings.EqualFold(subject, model) {
		return true
	}
	if provider == "" {
		return false
	}
	prefix := provider + "/"
	if strings.EqualFold(subject, prefix+model) {
		return true
	}
	return len(model) > len(prefix) &&
		strings.EqualFold(model[:len(prefix)], prefix) &&
		strings.EqualFold(subject, model[len(prefix):])
}

// SubjectLabel names the rule subject for error messages and logs.
func (r Rule) SubjectLabel() string {
	switch r.Scope {
	case ScopeProvider:
		return "provider " + r.Subject
	case ScopeModel:
		return "model " + r.Subject
	default:
		return r.Subject
	}
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
	subject := e.Rule.SubjectLabel()
	switch e.Scope {
	case ScopeTokens:
		return fmt.Sprintf("rate limit exceeded for %s: %s token limit of %d reached", subject, label, e.Limit)
	case ScopeConcurrency:
		return fmt.Sprintf("rate limit exceeded for %s: concurrent request limit of %d reached", subject, e.Limit)
	default:
		return fmt.Sprintf("rate limit exceeded for %s: %s request limit of %d reached", subject, label, e.Limit)
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

// NormalizeScope canonicalizes a rule scope name. An empty scope means
// user_path, keeping pre-scope rule definitions and requests valid.
func NormalizeScope(raw string) (RuleScope, error) {
	switch RuleScope(strings.ToLower(strings.TrimSpace(raw))) {
	case "", ScopeUserPath:
		return ScopeUserPath, nil
	case ScopeProvider:
		return ScopeProvider, nil
	case ScopeModel:
		return ScopeModel, nil
	default:
		return "", fmt.Errorf("scope must be one of user_path, provider, model")
	}
}

// NormalizeSubject canonicalizes a rule subject for its scope.
func NormalizeSubject(scope RuleScope, subject string) (string, error) {
	switch scope {
	case ScopeProvider:
		subject = strings.ToLower(strings.TrimSpace(subject))
		if subject == "" {
			return "", fmt.Errorf("provider rule subject is required")
		}
		if strings.ContainsAny(subject, "/ \t") {
			return "", fmt.Errorf("provider rule subject must be a provider name without slashes or spaces")
		}
		return subject, nil
	case ScopeModel:
		// Lowercased so storage matches the case-insensitive matching: without
		// this, "OpenAI/GPT-4o" and "openai/gpt-4o" would persist as two rules
		// that both match the same requests.
		subject = strings.ToLower(strings.TrimSpace(subject))
		if subject == "" {
			return "", fmt.Errorf("model rule subject is required")
		}
		if strings.HasPrefix(subject, "/") || strings.HasSuffix(subject, "/") {
			return "", fmt.Errorf("model rule subject must not start or end with a slash")
		}
		return subject, nil
	default:
		return NormalizeUserPath(subject)
	}
}

func NormalizeRule(r Rule) (Rule, error) {
	scope, err := NormalizeScope(string(r.Scope))
	if err != nil {
		return Rule{}, err
	}
	r.Scope = scope
	subject, err := NormalizeSubject(scope, r.Subject)
	if err != nil {
		return Rule{}, err
	}
	r.Subject = subject
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
