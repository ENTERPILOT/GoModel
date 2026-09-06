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

func TestParseResilienceStatusTokens(t *testing.T) {
	for _, tc := range []struct {
		name    string
		token   string
		want    []int
		wantErr bool
	}{
		{"exact code", "503", []int{503}, false},
		{"padded", "  429\t", []int{429}, false},
		{"lowest code", "100", []int{100}, false},
		{"highest code", "599", []int{599}, false},
		{"uppercase class", "5XX", []int{500, 550, 599}, false},
		{"informational class", "1xx", []int{100, 199}, false},
		{"below range", "099", nil, true},
		{"above range", "600", nil, true},
		{"class zero", "0xx", nil, true},
		{"class six", "6xx", nil, true},
		{"two digits", "99", nil, true},
		{"four digits", "1000", nil, true},
		{"wildcard inside", "4x4", nil, true},
		{"negative", "-99", nil, true},
		{"word", "5xxx", nil, true},
		{"empty", "", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statuses, err := ParseResilienceStatuses([]string{tc.token}, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("statuses=%v, want an error for %q", statuses, tc.token)
				}
				if !strings.Contains(err.Error(), "HTTP status code or class") {
					t.Fatalf("error=%v must explain the accepted forms", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, code := range tc.want {
				if !statuses[code] {
					t.Fatalf("%q did not expand to %d: %v", tc.token, code, statuses)
				}
			}
		})
	}
}

func TestParseResilienceStatusClassBoundaries(t *testing.T) {
	statuses, err := ParseResilienceStatuses([]string{"5xx"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 100 || statuses[499] || statuses[600] {
		t.Fatalf("5xx must cover exactly 500-599, got %d entries", len(statuses))
	}
}

func TestParseResilienceStatusesDefaultsAndOverrides(t *testing.T) {
	defaults := DefaultCircuitBreakerConfig().FailureOnStatuses

	inherited, err := ParseResilienceStatuses(nil, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if !inherited[429] || !inherited[500] {
		t.Fatalf("nil must inherit the defaults, got %v", inherited)
	}

	disabled, err := ParseResilienceStatuses([]string{}, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 0 {
		t.Fatalf("an explicit empty list must disable status matches, got %v", disabled)
	}

	deduped, err := ParseResilienceStatuses([]string{"503", "5xx", "503"}, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if len(deduped) != 100 {
		t.Fatalf("overlapping entries must merge, got %d entries", len(deduped))
	}

	if _, err := ParseResilienceStatuses(nil, []string{"oops"}); err == nil {
		t.Fatal("invalid defaults must be rejected too")
	}
}

func TestValidateResilienceScope(t *testing.T) {
	for _, tc := range []struct {
		scope   string
		wantErr bool
	}{
		{"", false},
		{"provider", false},
		{"model", false},
		{"Model", true},
		{"global", true},
		{" model", true},
	} {
		t.Run("scope="+tc.scope, func(t *testing.T) {
			r := ResilienceConfig{Retry: DefaultRetryConfig(), CircuitBreaker: DefaultCircuitBreakerConfig()}
			r.CircuitBreaker.Scope = tc.scope
			err := ValidateResilience(r)
			if tc.wantErr != (err != nil) {
				t.Fatalf("scope %q: error=%v", tc.scope, err)
			}
		})
	}
}

func TestNormalizeBreakerScope(t *testing.T) {
	for scope, want := range map[string]string{"": "provider", "provider": "provider", "model": "model"} {
		if got := NormalizeBreakerScope(scope); got != want {
			t.Fatalf("NormalizeBreakerScope(%q)=%q, want %q", scope, got, want)
		}
	}
}

func TestProviderPolicyOverrideValidation(t *testing.T) {
	for _, tc := range []struct{ name, body, wantError string }{
		{
			"invalid provider scope",
			"providers:\n  cloudflare:\n    resilience:\n      circuit_breaker:\n        scope: cluster\n",
			"providers.cloudflare.resilience: circuit_breaker.scope",
		},
		{
			"invalid provider breaker statuses",
			"providers:\n  cloudflare:\n    resilience:\n      circuit_breaker:\n        failure_on_statuses: [6xx]\n",
			"providers.cloudflare.resilience: circuit_breaker.failure_on_statuses",
		},
		{
			"valid provider override",
			"providers:\n  cloudflare:\n    resilience:\n      circuit_breaker:\n        scope: model\n        failure_on_statuses: [429, 5xx]\n      retry:\n        retry_on_statuses: []\n",
			"",
		},
		{
			"provider override survives an unrelated global policy",
			"resilience:\n  circuit_breaker:\n    scope: model\nproviders:\n  cloudflare:\n    resilience:\n      circuit_breaker:\n        scope: provider\n",
			"",
		},
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
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error=%v, want %s", err, tc.wantError)
			}
		})
	}
}
