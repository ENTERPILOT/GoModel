package config

import "fmt"

// ParseResilienceStatuses expands exact HTTP codes and classes. Nil uses the
// supplied defaults; an explicit empty list disables status-based matches.
func ParseResilienceStatuses(entries, defaults []string) (map[int]bool, error) {
	if entries == nil {
		entries = defaults
	}
	statuses := make(map[int]bool)
	for _, entry := range entries {
		codes, ok := expandStatusToken(entry)
		if !ok {
			return nil, fmt.Errorf("%q is not an HTTP status code or class such as 5xx", entry)
		}
		for _, code := range codes {
			statuses[code] = true
		}
	}
	return statuses, nil
}

func validateResilienceConfig(global ResilienceConfig, providers map[string]RawProviderConfig) error {
	if err := ValidateResilience(global); err != nil {
		return fmt.Errorf("resilience: %w", err)
	}
	for name, provider := range providers {
		if provider.Resilience == nil {
			continue
		}
		r := global
		if retry := provider.Resilience.Retry; retry != nil && retry.RetryOnStatuses != nil {
			r.Retry.RetryOnStatuses = retry.RetryOnStatuses
		}
		if cb := provider.Resilience.CircuitBreaker; cb != nil {
			if cb.FailureOnStatuses != nil {
				r.CircuitBreaker.FailureOnStatuses = cb.FailureOnStatuses
			}
			if cb.Scope != nil {
				r.CircuitBreaker.Scope = *cb.Scope
			}
		}
		if err := ValidateResilience(r); err != nil {
			return fmt.Errorf("providers.%s.resilience: %w", name, err)
		}
	}
	return nil
}

// ValidateResilience checks policies for both file loading and programmatic providers.
func ValidateResilience(r ResilienceConfig) error {
	if _, err := ParseResilienceStatuses(r.Retry.RetryOnStatuses, nil); err != nil {
		return fmt.Errorf("retry.retry_on_statuses: %w", err)
	}
	if _, err := ParseResilienceStatuses(r.CircuitBreaker.FailureOnStatuses, nil); err != nil {
		return fmt.Errorf("circuit_breaker.failure_on_statuses: %w", err)
	}
	switch r.CircuitBreaker.Scope {
	case "", "provider", "model":
	default:
		return fmt.Errorf("circuit_breaker.scope must be provider or model")
	}
	return nil
}

// NormalizeBreakerScope resolves the empty scope to the provider default.
func NormalizeBreakerScope(scope string) string {
	if scope == "" {
		return "provider"
	}
	return scope
}
