package gateway

import (
	"errors"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// FailoverPolicy decides which failed attempts trigger a failover sweep and
// how many targets the sweep may try. Build one with NewFailoverPolicy; a nil
// policy, or one with no matchers, retries on the documented defaults.
type FailoverPolicy struct {
	// MaxAttempts caps the failover targets tried per request; zero sweeps
	// every remaining target.
	MaxAttempts int
	statuses    map[int]bool
	phrases     []config.FailoverErrorPhrase
}

// NewFailoverPolicy compiles the failover section of the configuration. A
// config that was not loaded through config.Load (so its parsed fields are
// empty) falls back to the documented defaults.
func NewFailoverPolicy(cfg config.FailoverConfig) *FailoverPolicy {
	policy := &FailoverPolicy{
		MaxAttempts: max(cfg.MaxAttempts, 0),
		statuses:    cfg.RetryStatuses,
		phrases:     cfg.RetryErrors,
	}
	if len(policy.statuses) == 0 || len(policy.phrases) == 0 {
		defaults := config.FailoverConfig{}
		if err := config.LoadFailoverPolicy(&defaults); err != nil {
			panic("gateway: default failover policy must parse: " + err.Error())
		}
		if len(policy.statuses) == 0 {
			policy.statuses = defaults.RetryStatuses
		}
		if len(policy.phrases) == 0 {
			policy.phrases = defaults.RetryErrors
		}
	}
	return policy
}

var defaultFailoverPolicy = NewFailoverPolicy(config.FailoverConfig{})

// ShouldRetry reports whether err should trigger a failover sweep: its HTTP
// status is in retry_on_statuses, or its code and message contain every word
// of a retry_on_errors phrase (with that phrase's status constraint, if any).
func (p *FailoverPolicy) ShouldRetry(err error) bool {
	if p == nil {
		p = defaultFailoverPolicy
	}
	statuses, phrases := p.statuses, p.phrases
	if len(statuses) == 0 && len(phrases) == 0 {
		statuses, phrases = defaultFailoverPolicy.statuses, defaultFailoverPolicy.phrases
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr == nil {
		return false
	}

	status := gatewayErr.HTTPStatusCode()
	if statuses[status] {
		return true
	}

	text := strings.ToLower(gatewayErr.Message)
	if gatewayErr.Code != nil {
		text = strings.ToLower(*gatewayErr.Code) + " " + text
	}
	for _, phrase := range phrases {
		if len(phrase.Statuses) > 0 && !phrase.Statuses[status] {
			continue
		}
		if containsAll(text, phrase.Words) {
			return true
		}
	}
	return false
}

// attemptsExhausted reports whether attempts has reached the policy cap.
func (p *FailoverPolicy) attemptsExhausted(attempts int) bool {
	return p != nil && p.MaxAttempts > 0 && attempts >= p.MaxAttempts
}

func containsAll(text string, words []string) bool {
	for _, word := range words {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

// ShouldAttemptFailover reports whether err should trigger translated failover
// under the default policy.
func ShouldAttemptFailover(err error) bool {
	return defaultFailoverPolicy.ShouldRetry(err)
}
