package config

import "time"

// RetryConfig holds resolved retry settings for an LLM client.
// This is the canonical type shared between config and llmclient.
type RetryConfig struct {
	// RetryOnStatuses uses defaults when nil; an empty list disables status triggers.
	RetryOnStatuses []string      `yaml:"retry_on_statuses" env:"RETRY_ON_STATUSES"`
	MaxRetries      int           `yaml:"max_retries"     env:"RETRY_MAX_RETRIES"`
	InitialBackoff  time.Duration `yaml:"initial_backoff" env:"RETRY_INITIAL_BACKOFF"`
	MaxBackoff      time.Duration `yaml:"max_backoff"     env:"RETRY_MAX_BACKOFF"`
	BackoffFactor   float64       `yaml:"backoff_factor"  env:"RETRY_BACKOFF_FACTOR"`
	JitterFactor    float64       `yaml:"jitter_factor"   env:"RETRY_JITTER_FACTOR"`
}

// DefaultRetryConfig returns the default retry settings.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		RetryOnStatuses: []string{"429", "502", "503", "504", "522", "524"},
		MaxRetries:      3,
		InitialBackoff:  1 * time.Second,
		MaxBackoff:      30 * time.Second,
		BackoffFactor:   2.0,
		JitterFactor:    0.1,
	}
}

// CircuitBreakerConfig holds resolved circuit breaker settings.
// This is the canonical type shared between config and llmclient.
type CircuitBreakerConfig struct {
	// FailureOnStatuses uses defaults when nil; an empty list disables status triggers.
	FailureOnStatuses []string `yaml:"failure_on_statuses" env:"CIRCUIT_BREAKER_FAILURE_ON_STATUSES"`
	Scope             string   `yaml:"scope" env:"CIRCUIT_BREAKER_SCOPE"`
	// Enabled switches the circuit breaker on or off. When false, requests are
	// never short-circuited regardless of the thresholds below.
	// Default: true
	Enabled          bool          `yaml:"enabled"           env:"CIRCUIT_BREAKER_ENABLED"`
	FailureThreshold int           `yaml:"failure_threshold" env:"CIRCUIT_BREAKER_FAILURE_THRESHOLD"`
	SuccessThreshold int           `yaml:"success_threshold" env:"CIRCUIT_BREAKER_SUCCESS_THRESHOLD"`
	Timeout          time.Duration `yaml:"timeout"           env:"CIRCUIT_BREAKER_TIMEOUT"`
}

// DefaultCircuitBreakerConfig returns the default circuit breaker settings.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureOnStatuses: []string{"429", "5xx"},
		Scope:             "provider",
		Enabled:           true,
		FailureThreshold:  5,
		SuccessThreshold:  2,
		Timeout:           30 * time.Second,
	}
}

// ResilienceConfig holds resolved resilience settings (retry and circuit breaker).
type ResilienceConfig struct {
	Retry          RetryConfig          `yaml:"retry"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
}

// RawResilienceConfig holds optional per-provider resilience overrides from YAML.
// Nil fields inherit from the global ResilienceConfig.
type RawResilienceConfig struct {
	Retry          *RawRetryConfig          `yaml:"retry"`
	CircuitBreaker *RawCircuitBreakerConfig `yaml:"circuit_breaker"`
}

// RawCircuitBreakerConfig holds optional per-provider circuit breaker overrides from YAML.
// Nil fields inherit from the global CircuitBreakerConfig.
type RawCircuitBreakerConfig struct {
	FailureOnStatuses []string       `yaml:"failure_on_statuses"`
	Scope             *string        `yaml:"scope"`
	Enabled           *bool          `yaml:"enabled"`
	FailureThreshold  *int           `yaml:"failure_threshold"`
	SuccessThreshold  *int           `yaml:"success_threshold"`
	Timeout           *time.Duration `yaml:"timeout"`
}

// RawRetryConfig holds optional per-provider retry overrides from YAML.
// Nil fields inherit from the global RetryConfig.
type RawRetryConfig struct {
	RetryOnStatuses []string       `yaml:"retry_on_statuses"`
	MaxRetries      *int           `yaml:"max_retries"`
	InitialBackoff  *time.Duration `yaml:"initial_backoff"`
	MaxBackoff      *time.Duration `yaml:"max_backoff"`
	BackoffFactor   *float64       `yaml:"backoff_factor"`
	JitterFactor    *float64       `yaml:"jitter_factor"`
}
