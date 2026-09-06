package providers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"gopkg.in/yaml.v3"
)

func TestProviderResilienceStatusOverrides(t *testing.T) {
	global := config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()}
	var raw config.RawProviderConfig
	err := yaml.Unmarshal([]byte("type: openai\nresilience:\n  retry:\n    retry_on_statuses: []\n  circuit_breaker:\n    failure_on_statuses: [524]\n    scope: model\n"), &raw)
	if err != nil {
		t.Fatal(err)
	}
	got := buildProviderConfig(raw, global).Resilience
	if got.Retry.RetryOnStatuses == nil || len(got.Retry.RetryOnStatuses) != 0 {
		t.Fatal("empty retry override was lost")
	}
	if !reflect.DeepEqual(got.CircuitBreaker.FailureOnStatuses, []string{"524"}) || got.CircuitBreaker.Scope != "model" {
		t.Fatalf("breaker=%+v", got.CircuitBreaker)
	}
	if got.Retry.MaxRetries != global.Retry.MaxRetries || got.CircuitBreaker.FailureThreshold != global.CircuitBreaker.FailureThreshold {
		t.Fatal("unrelated settings must inherit")
	}
	if !reflect.DeepEqual(buildProviderConfig(config.RawProviderConfig{}, global).Resilience, global) {
		t.Fatal("omitted settings must inherit")
	}
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
			if got.CircuitBreaker.Scope != wantScope || !reflect.DeepEqual(got.Retry.RetryOnStatuses, global.Retry.RetryOnStatuses) || !reflect.DeepEqual(got.CircuitBreaker.FailureOnStatuses, global.CircuitBreaker.FailureOnStatuses) {
				t.Fatalf("sanitized settings=%+v", got)
			}
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
		if err == nil || !strings.Contains(err.Error(), "invalid resilience configuration") {
			t.Fatalf("error=%v", err)
		}
	}
}
