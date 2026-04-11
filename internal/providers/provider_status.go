package providers

import (
	"sort"
	"strings"
	"time"
)

// SanitizedRetryConfig exposes effective retry settings without secrets.
type SanitizedRetryConfig struct {
	MaxRetries     int     `json:"max_retries"`
	InitialBackoff string  `json:"initial_backoff"`
	MaxBackoff     string  `json:"max_backoff"`
	BackoffFactor  float64 `json:"backoff_factor"`
	JitterFactor   float64 `json:"jitter_factor"`
}

// SanitizedCircuitBreakerConfig exposes effective circuit-breaker settings.
type SanitizedCircuitBreakerConfig struct {
	FailureThreshold int    `json:"failure_threshold"`
	SuccessThreshold int    `json:"success_threshold"`
	Timeout          string `json:"timeout"`
}

// SanitizedResilienceConfig exposes effective resilience settings.
type SanitizedResilienceConfig struct {
	Retry          SanitizedRetryConfig          `json:"retry"`
	CircuitBreaker SanitizedCircuitBreakerConfig `json:"circuit_breaker"`
}

// SanitizedProviderConfig is the admin-safe provider configuration view.
type SanitizedProviderConfig struct {
	Name       string                    `json:"name"`
	Type       string                    `json:"type"`
	BaseURL    string                    `json:"base_url,omitempty"`
	APIVersion string                    `json:"api_version,omitempty"`
	Models     []string                  `json:"models,omitempty"`
	Resilience SanitizedResilienceConfig `json:"resilience"`
}

// ProviderRuntimeSnapshot describes runtime diagnostics for a configured provider.
type ProviderRuntimeSnapshot struct {
	Name                    string     `json:"name"`
	Type                    string     `json:"type"`
	Registered              bool       `json:"registered"`
	RegistryInitialized     bool       `json:"registry_initialized"`
	DiscoveredModelCount    int        `json:"discovered_model_count"`
	UsingCachedModels       bool       `json:"using_cached_models"`
	LastModelFetchAt        *time.Time `json:"last_model_fetch_at,omitempty"`
	LastModelFetchSuccessAt *time.Time `json:"last_model_fetch_success_at,omitempty"`
	LastModelFetchError     string     `json:"last_model_fetch_error,omitempty"`
	LastAvailabilityCheckAt *time.Time `json:"last_availability_check_at,omitempty"`
	LastAvailabilityOKAt    *time.Time `json:"last_availability_ok_at,omitempty"`
	LastAvailabilityError   string     `json:"last_availability_error,omitempty"`
}

type providerRuntimeState struct {
	registered              bool
	lastModelFetchAt        time.Time
	lastModelFetchSuccessAt time.Time
	lastModelFetchError     string
	lastAvailabilityCheckAt time.Time
	lastAvailabilityOKAt    time.Time
	lastAvailabilityError   string
}

// SanitizeProviderConfigs converts effective provider configs into a stable,
// admin-safe slice keyed by configured provider name.
func SanitizeProviderConfigs(configs map[string]ProviderConfig) []SanitizedProviderConfig {
	if len(configs) == 0 {
		return nil
	}

	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]SanitizedProviderConfig, 0, len(names))
	for _, name := range names {
		cfg := configs[name]
		models := make([]string, 0, len(cfg.Models))
		for _, model := range cfg.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			models = append(models, model)
		}

		result = append(result, SanitizedProviderConfig{
			Name:       strings.TrimSpace(name),
			Type:       strings.TrimSpace(cfg.Type),
			BaseURL:    strings.TrimSpace(cfg.BaseURL),
			APIVersion: strings.TrimSpace(cfg.APIVersion),
			Models:     models,
			Resilience: SanitizedResilienceConfig{
				Retry: SanitizedRetryConfig{
					MaxRetries:     cfg.Resilience.Retry.MaxRetries,
					InitialBackoff: cfg.Resilience.Retry.InitialBackoff.String(),
					MaxBackoff:     cfg.Resilience.Retry.MaxBackoff.String(),
					BackoffFactor:  cfg.Resilience.Retry.BackoffFactor,
					JitterFactor:   cfg.Resilience.Retry.JitterFactor,
				},
				CircuitBreaker: SanitizedCircuitBreakerConfig{
					FailureThreshold: cfg.Resilience.CircuitBreaker.FailureThreshold,
					SuccessThreshold: cfg.Resilience.CircuitBreaker.SuccessThreshold,
					Timeout:          cfg.Resilience.CircuitBreaker.Timeout.String(),
				},
			},
		})
	}

	return result
}

func timePtrUTC(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	value := t.UTC()
	return &value
}
