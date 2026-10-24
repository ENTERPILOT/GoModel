package providertest

import (
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// Resilience returns the production retry and circuit-breaker settings with
// millisecond backoffs. Tests that exercise retryable statuses keep the
// production semantics (attempt count, retryable status set, breaker
// thresholds) without sitting through the 1s/2s/4s schedule, which made a
// single mocked 429 cost seven seconds.
func Resilience() config.ResilienceConfig {
	r := config.ResilienceConfig{
		Retry:          config.DefaultRetryConfig(),
		CircuitBreaker: config.DefaultCircuitBreakerConfig(),
	}
	r.Retry.InitialBackoff = time.Millisecond
	r.Retry.MaxBackoff = 2 * time.Millisecond
	return r
}

// Options returns the ProviderOptions a test passes to a provider's factory
// constructor: production defaults except for Resilience.
func Options(hooks llmclient.Hooks) providers.ProviderOptions {
	return providers.ProviderOptions{Hooks: hooks, Resilience: Resilience()}
}
