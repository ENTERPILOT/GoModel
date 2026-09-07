package providers

import (
	"encoding/json"
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

func TestSanitizedStatusListsDistinguishInheritFromDisabled(t *testing.T) {
	global := config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()}
	var raw config.RawProviderConfig
	body := "type: openai\nresilience:\n  retry:\n    retry_on_statuses: []\n"
	if err := yaml.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	sanitized := SanitizeProviderConfigs(map[string]ProviderConfig{"test": buildProviderConfig(raw, global)})[0]
	encoded, err := json.Marshal(sanitized.Resilience)
	if err != nil {
		t.Fatal(err)
	}
	// An operator reading the admin API must be able to tell "no status
	// triggers" from "inherits the defaults"; both survive as distinct JSON.
	if !strings.Contains(string(encoded), `"retry_on_statuses":[]`) {
		t.Fatalf("disabled retry statuses must serialize as an empty list: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"failure_on_statuses":["429","5xx"]`) {
		t.Fatalf("inherited breaker statuses must serialize as the defaults: %s", encoded)
	}
}

func TestFactoryAcceptsValidResilience(t *testing.T) {
	factory := NewProviderFactory()
	cfg := ProviderConfig{Type: "not-registered", Resilience: config.ResilienceConfig{
		Retry:          config.RetryConfig{RetryOnStatuses: []string{"429", "5xx"}},
		CircuitBreaker: config.CircuitBreakerConfig{FailureOnStatuses: []string{}, Scope: "model"},
	}}
	_, err := factory.Create(cfg)
	if err == nil || strings.Contains(err.Error(), "invalid resilience configuration") {
		t.Fatalf("error=%v, want the policy accepted and the lookup to fail instead", err)
	}
	if !strings.Contains(err.Error(), "unknown provider type") {
		t.Fatalf("error=%v", err)
	}
}
