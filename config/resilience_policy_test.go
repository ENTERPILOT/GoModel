package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResiliencePolicyLoading(t *testing.T) {
	for _, tc := range []struct{ name, body, wantError string }{
		{"defaults", "", ""},
		{"classes", "resilience:\n  retry:\n    retry_on_statuses: [429, 5xx]\n  circuit_breaker:\n    scope: model\n    failure_on_statuses: []\n", ""},
		{"invalid retry", "resilience:\n  retry:\n    retry_on_statuses: [600]\n", "retry.retry_on_statuses"},
		{"invalid breaker", "resilience:\n  circuit_breaker:\n    failure_on_statuses: [oops]\n", "circuit_breaker.failure_on_statuses"},
		{"invalid scope", "resilience:\n  circuit_breaker:\n    scope: global\n", "circuit_breaker.scope"},
		{"invalid provider", "providers:\n  cloudflare:\n    resilience:\n      retry:\n        retry_on_statuses: [oops]\n", "providers.cloudflare.resilience"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderEnvVars(t)
			dir := t.TempDir()
			writeConfigYAML(t, dir, tc.body)
			t.Chdir(dir)
			_, err := Load()
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error=%v, want %s", err, tc.wantError)
			}
		})
	}
}

func TestResilienceEmptyListsAndEnvironment(t *testing.T) {
	cfg := &Config{Resilience: ResilienceConfig{Retry: DefaultRetryConfig(), CircuitBreaker: DefaultCircuitBreakerConfig()}}
	if err := yaml.Unmarshal([]byte("resilience:\n  retry:\n    retry_on_statuses: []\n  circuit_breaker:\n    failure_on_statuses: []\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Resilience.Retry.RetryOnStatuses == nil || cfg.Resilience.CircuitBreaker.FailureOnStatuses == nil {
		t.Fatal("explicit empty lists must remain non-nil")
	}
	t.Setenv("RETRY_ON_STATUSES", "429,524")
	t.Setenv("CIRCUIT_BREAKER_FAILURE_ON_STATUSES", "429,5xx")
	t.Setenv("CIRCUIT_BREAKER_SCOPE", "model")
	if err := applyEnvOverrides(cfg); err != nil {
		t.Fatal(err)
	}
	statuses, err := ParseResilienceStatuses(cfg.Resilience.CircuitBreaker.FailureOnStatuses, nil)
	if err != nil || !statuses[429] || !statuses[524] || cfg.Resilience.CircuitBreaker.Scope != "model" {
		t.Fatalf("config=%+v err=%v", cfg.Resilience, err)
	}
	if strings.Join(cfg.Resilience.Retry.RetryOnStatuses, ",") != "429,524" {
		t.Fatal("retry environment override missing")
	}
}
