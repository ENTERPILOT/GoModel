package providers

import (
	"reflect"
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
