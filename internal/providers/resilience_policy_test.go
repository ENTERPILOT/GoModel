package providers

import (
	"encoding/json"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestProviderResilienceStatusOverrides(t *testing.T) {
	global := config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()}
	var raw config.RawProviderConfig
	err := yaml.Unmarshal([]byte("type: openai\nresilience:\n  retry:\n    retry_on_statuses: []\n  circuit_breaker:\n    failure_on_statuses: [524]\n    scope: model\n"), &raw)
	require.NoError(t, err)

	got := buildProviderConfig(raw, global).Resilience
	require.NotNil(t, got.Retry.RetryOnStatuses)
	require.Empty(t, got.Retry.RetryOnStatuses)
	require.Equal(t, []string{"524"}, got.CircuitBreaker.FailureOnStatuses)
	require.Equal(t, "model", got.CircuitBreaker.Scope, "breaker=%+v", got.CircuitBreaker)
	require.Equal(t, global.Retry.MaxRetries, got.Retry.MaxRetries)
	require.Equal(t, global.CircuitBreaker.FailureThreshold, got.CircuitBreaker.FailureThreshold)
	require.Equal(t, global, buildProviderConfig(config.RawProviderConfig{}, global).Resilience)
}

func TestSanitizedResiliencePolicies(t *testing.T) {
	for _, scope := range []string{"model", ""} {
		t.Run("scope="+scope, func(t *testing.T) {
			global := config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()}
			global.CircuitBreaker.Scope = "model"
			raw := config.RawProviderConfig{Resilience: &config.RawResilienceConfig{CircuitBreaker: &config.RawCircuitBreakerConfig{Scope: &scope}}}
			cfg := buildProviderConfig(raw, global)
			got := SanitizeProviderConfigs(map[string]ProviderConfig{"test": cfg})[0].Resilience
			wantScope := scope
			if wantScope == "" {
				wantScope = "provider"
			}
			require.Equal(t, wantScope, got.CircuitBreaker.Scope)
			require.Equal(t, global.Retry.RetryOnStatuses, got.Retry.RetryOnStatuses)
			require.Equal(t, global.CircuitBreaker.FailureOnStatuses, got.CircuitBreaker.FailureOnStatuses, "sanitized settings=%+v", got)
		})
	}
}

func TestFactoryRejectsInvalidResilience(t *testing.T) {
	factory := NewProviderFactory()
	for _, r := range []config.ResilienceConfig{
		{Retry: config.RetryConfig{RetryOnStatuses: []string{"bad"}}},
		{CircuitBreaker: config.CircuitBreakerConfig{FailureOnStatuses: []string{"600"}}},
		{CircuitBreaker: config.CircuitBreakerConfig{Scope: "bad"}},
	} {
		_, err := factory.Create(ProviderConfig{Resilience: r})
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid resilience configuration")
	}
}

func TestSanitizedStatusListsDistinguishInheritFromDisabled(t *testing.T) {
	global := config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()}
	var raw config.RawProviderConfig
	body := "type: openai\nresilience:\n  retry:\n    retry_on_statuses: []\n"
	err := yaml.Unmarshal([]byte(body), &raw)
	require.NoError(t, err)

	sanitized := SanitizeProviderConfigs(map[string]ProviderConfig{"test": buildProviderConfig(raw, global)})[0]
	encoded, err := json.Marshal(sanitized.Resilience)
	require.NoError(t, err)

	// An operator reading the admin API must be able to tell "no status
	// triggers" from "inherits the defaults"; both survive as distinct JSON.
	require.Contains(t, string(encoded), `"retry_on_statuses":[]`, "disabled retry statuses must serialize as an empty list: %s", encoded)
	require.Contains(t, string(encoded), `"failure_on_statuses":["429","5xx"]`, "inherited breaker statuses must serialize as the defaults: %s", encoded)
}

func TestFactoryAcceptsValidResilience(t *testing.T) {
	factory := NewProviderFactory()
	cfg := ProviderConfig{Type: "not-registered", Resilience: config.ResilienceConfig{
		Retry:          config.RetryConfig{RetryOnStatuses: []string{"429", "5xx"}},
		CircuitBreaker: config.CircuitBreakerConfig{FailureOnStatuses: []string{}, Scope: "model"},
	}}
	_, err := factory.Create(cfg)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "invalid resilience configuration")
	require.Contains(t, err.Error(), "unknown provider type")
}
