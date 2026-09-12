package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantError)
		})
	}
}

func TestResilienceEmptyListsAndEnvironment(t *testing.T) {
	cfg := &Config{Resilience: ResilienceConfig{Retry: DefaultRetryConfig(), CircuitBreaker: DefaultCircuitBreakerConfig()}}
	err := yaml.Unmarshal([]byte("resilience:\n  retry:\n    retry_on_statuses: []\n  circuit_breaker:\n    failure_on_statuses: []\n"), cfg)
	require.NoError(t, err)
	require.NotNil(t, cfg.Resilience.Retry.RetryOnStatuses)
	require.NotNil(t, cfg.Resilience.CircuitBreaker.FailureOnStatuses)

	t.Setenv("RETRY_ON_STATUSES", "429,524")
	t.Setenv("CIRCUIT_BREAKER_FAILURE_ON_STATUSES", "429,5xx")
	t.Setenv("CIRCUIT_BREAKER_SCOPE", "model")
	err = applyEnvOverrides(cfg)
	require.NoError(t, err)

	statuses, err := ParseResilienceStatuses(cfg.Resilience.CircuitBreaker.FailureOnStatuses, nil)
	require.NoError(t, err)
	require.True(t, statuses[429])
	require.True(t, statuses[524])
	require.Equal(t, "model", cfg.Resilience.CircuitBreaker.Scope)
	require.Equal(t, "429,524", strings.Join(cfg.Resilience.Retry.RetryOnStatuses, ","))
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
				require.Error(t, err)
				require.Contains(t, err.Error(), "HTTP status code or class")
				return
			}
			require.NoError(t, err)

			for _, code := range tc.want {
				require.True(t, statuses[code], "%q did not expand to %d: %v", tc.token, code, statuses)
			}
		})
	}
}

func TestParseResilienceStatusClassBoundaries(t *testing.T) {
	statuses, err := ParseResilienceStatuses([]string{"5xx"}, nil)
	require.NoError(t, err)
	require.Len(t, statuses, 100, "5xx must cover exactly 500-599")
	require.False(t, statuses[499])
	require.False(t, statuses[600])
}

func TestParseResilienceStatusesDefaultsAndOverrides(t *testing.T) {
	defaults := DefaultCircuitBreakerConfig().FailureOnStatuses

	inherited, err := ParseResilienceStatuses(nil, defaults)
	require.NoError(t, err)
	require.True(t, inherited[429])
	require.True(t, inherited[500], "nil must inherit the defaults, got %v", inherited)

	disabled, err := ParseResilienceStatuses([]string{}, defaults)
	require.NoError(t, err)
	require.Empty(t, disabled)

	deduped, err := ParseResilienceStatuses([]string{"503", "5xx", "503"}, defaults)
	require.NoError(t, err)
	require.Len(t, deduped, 100)
	_, err = ParseResilienceStatuses(nil, []string{"oops"})
	require.Error(t, err)
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
			require.Equal(t, tc.wantErr, err != nil, "scope %q: error=%v", tc.scope, err)
		})
	}
}

func TestNormalizeBreakerScope(t *testing.T) {
	for scope, want := range map[string]string{"": "provider", "provider": "provider", "model": "model"} {
		got := NormalizeBreakerScope(scope)
		require.Equal(t, want, got)
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
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantError)
		})
	}
}
