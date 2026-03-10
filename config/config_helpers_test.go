package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestExpandString tests the expandString function with various scenarios
func TestExpandString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		envVars  map[string]string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			envVars:  map[string]string{},
			expected: "",
		},
		{
			name:     "string without placeholders",
			input:    "simple-string",
			envVars:  map[string]string{},
			expected: "simple-string",
		},
		{
			name:     "simple variable expansion",
			input:    "${API_KEY}",
			envVars:  map[string]string{"API_KEY": "sk-12345"},
			expected: "sk-12345",
		},
		{
			name:     "variable in middle of string",
			input:    "prefix-${API_KEY}-suffix",
			envVars:  map[string]string{"API_KEY": "sk-12345"},
			expected: "prefix-sk-12345-suffix",
		},
		{
			name:     "multiple variables",
			input:    "${SCHEME}://${HOST}:${PORT}",
			envVars:  map[string]string{"SCHEME": "https", "HOST": "api.example.com", "PORT": "8080"},
			expected: "https://api.example.com:8080",
		},
		{
			name:     "variable with default value - env var exists",
			input:    "${API_KEY:-default-key}",
			envVars:  map[string]string{"API_KEY": "sk-real-key"},
			expected: "sk-real-key",
		},
		{
			name:     "variable with default value - env var missing",
			input:    "${API_KEY:-default-key}",
			envVars:  map[string]string{},
			expected: "default-key",
		},
		{
			name:     "variable with default value - env var empty",
			input:    "${API_KEY:-default-key}",
			envVars:  map[string]string{"API_KEY": ""},
			expected: "default-key",
		},
		{
			name:     "unresolved variable - no default",
			input:    "${MISSING_VAR}",
			envVars:  map[string]string{},
			expected: "${MISSING_VAR}",
		},
		{
			name:     "partially resolved string",
			input:    "${RESOLVED}-${UNRESOLVED}",
			envVars:  map[string]string{"RESOLVED": "value1"},
			expected: "value1-${UNRESOLVED}",
		},
		{
			name:     "mixed resolved and unresolved with defaults",
			input:    "${RESOLVED}:${UNRESOLVED:-fallback}:${MISSING}",
			envVars:  map[string]string{"RESOLVED": "value1"},
			expected: "value1:fallback:${MISSING}",
		},
		{
			name:     "default value with special characters",
			input:    "${API_KEY:-https://api.example.com/v1}",
			envVars:  map[string]string{},
			expected: "https://api.example.com/v1",
		},
		{
			name:     "default value with colon in it",
			input:    "${URL:-http://localhost:8080}",
			envVars:  map[string]string{},
			expected: "http://localhost:8080",
		},
		{
			name:     "complex real-world example",
			input:    "${BASE_URL:-https://api.openai.com}/v1/chat/completions",
			envVars:  map[string]string{},
			expected: "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "environment variable set to empty string (no default)",
			input:    "${EMPTY_VAR}",
			envVars:  map[string]string{"EMPTY_VAR": ""},
			expected: "${EMPTY_VAR}",
		},
		{
			name:     "empty default value - env var missing",
			input:    "${OPTIONAL_VAR:-}",
			envVars:  map[string]string{},
			expected: "",
		},
		{
			name:     "empty default value - env var set",
			input:    "${OPTIONAL_VAR:-}",
			envVars:  map[string]string{"OPTIONAL_VAR": "actual-value"},
			expected: "actual-value",
		},
		{
			name:     "empty default value - env var empty",
			input:    "${OPTIONAL_VAR:-}",
			envVars:  map[string]string{"OPTIONAL_VAR": ""},
			expected: "",
		},
		{
			name:     "master key pattern - not set should be empty",
			input:    "${GOMODEL_MASTER_KEY:-}",
			envVars:  map[string]string{},
			expected: "",
		},
		{
			name:     "master key pattern - set to value",
			input:    "${GOMODEL_MASTER_KEY:-}",
			envVars:  map[string]string{"GOMODEL_MASTER_KEY": "secret-key"},
			expected: "secret-key",
		},
		{
			name:     "multiple placeholders some resolved some not",
			input:    "prefix-${VAR1}-${VAR2}-${VAR3}-suffix",
			envVars:  map[string]string{"VAR1": "a", "VAR3": "c"},
			expected: "prefix-a-${VAR2}-c-suffix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				_ = os.Setenv(k, v)
			}
			defer func() {
				for k := range tt.envVars {
					_ = os.Unsetenv(k)
				}
			}()

			result := expandString(tt.input)
			if result != tt.expected {
				t.Errorf("expandString(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestApplyEnvOverrides tests the applyEnvOverrides function
func TestApplyEnvOverrides(t *testing.T) {
	tests := []struct {
		name    string
		envVars map[string]string
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name:    "PORT override",
			envVars: map[string]string{"PORT": "3000"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Server.Port != "3000" {
					t.Errorf("Server.Port = %q, want %q", cfg.Server.Port, "3000")
				}
			},
		},
		{
			name:    "GOMODEL_MASTER_KEY override",
			envVars: map[string]string{"GOMODEL_MASTER_KEY": "my-secret"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Server.MasterKey != "my-secret" {
					t.Errorf("Server.MasterKey = %q, want %q", cfg.Server.MasterKey, "my-secret")
				}
			},
		},
		{
			name:    "passthrough v1 normalization override",
			envVars: map[string]string{"NORMALIZE_OPENAI_COMPATIBLE_PASSTHROUGH_V1_PREFIX": "false"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Server.NormalizeOpenAICompatiblePassthroughV1Prefix {
					t.Error("Server.NormalizeOpenAICompatiblePassthroughV1Prefix should be false")
				}
			},
		},
		{
			name:    "storage overrides",
			envVars: map[string]string{"STORAGE_TYPE": "postgresql", "POSTGRES_URL": "postgres://localhost/test", "POSTGRES_MAX_CONNS": "20"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Storage.Type != "postgresql" {
					t.Errorf("Storage.Type = %q, want %q", cfg.Storage.Type, "postgresql")
				}
				if cfg.Storage.PostgreSQL.URL != "postgres://localhost/test" {
					t.Errorf("Storage.PostgreSQL.URL = %q, want %q", cfg.Storage.PostgreSQL.URL, "postgres://localhost/test")
				}
				if cfg.Storage.PostgreSQL.MaxConns != 20 {
					t.Errorf("Storage.PostgreSQL.MaxConns = %d, want %d", cfg.Storage.PostgreSQL.MaxConns, 20)
				}
			},
		},
		{
			name:    "bool overrides",
			envVars: map[string]string{"METRICS_ENABLED": "true", "LOGGING_ENABLED": "1", "LOGGING_LOG_BODIES": "false"},
			check: func(t *testing.T, cfg *Config) {
				if !cfg.Metrics.Enabled {
					t.Error("Metrics.Enabled should be true")
				}
				if !cfg.Logging.Enabled {
					t.Error("Logging.Enabled should be true")
				}
				if cfg.Logging.LogBodies {
					t.Error("Logging.LogBodies should be false")
				}
			},
		},
		{
			name:    "guardrails batch flag override",
			envVars: map[string]string{"ENABLE_GUARDRAILS_FOR_BATCH_PROCESSING": "true"},
			check: func(t *testing.T, cfg *Config) {
				if !cfg.Guardrails.EnableForBatchProcessing {
					t.Error("Guardrails.EnableForBatchProcessing should be true")
				}
			},
		},
		{
			name:    "HTTP timeout overrides",
			envVars: map[string]string{"HTTP_TIMEOUT": "30", "HTTP_RESPONSE_HEADER_TIMEOUT": "60"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.HTTP.Timeout != 30 {
					t.Errorf("HTTP.Timeout = %d, want 30", cfg.HTTP.Timeout)
				}
				if cfg.HTTP.ResponseHeaderTimeout != 60 {
					t.Errorf("HTTP.ResponseHeaderTimeout = %d, want 60", cfg.HTTP.ResponseHeaderTimeout)
				}
			},
		},
		{
			name:    "CACHE_REFRESH_INTERVAL override",
			envVars: map[string]string{"CACHE_REFRESH_INTERVAL": "1800"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Cache.Model.RefreshInterval != 1800 {
					t.Errorf("Cache.Model.RefreshInterval = %d, want 1800", cfg.Cache.Model.RefreshInterval)
				}
			},
		},
		{
			name:    "no env vars set preserves defaults",
			envVars: map[string]string{},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Server.Port != "8080" {
					t.Errorf("Server.Port = %q, want %q", cfg.Server.Port, "8080")
				}
				if cfg.HTTP.Timeout != 600 {
					t.Errorf("HTTP.Timeout = %d, want 600", cfg.HTTP.Timeout)
				}
			},
		},
		{
			name:    "retry int override",
			envVars: map[string]string{"RETRY_MAX_RETRIES": "7"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Resilience.Retry.MaxRetries != 7 {
					t.Errorf("Resilience.Retry.MaxRetries = %d, want 7", cfg.Resilience.Retry.MaxRetries)
				}
			},
		},
		{
			name: "retry duration overrides",
			envVars: map[string]string{
				"RETRY_INITIAL_BACKOFF": "500ms",
				"RETRY_MAX_BACKOFF":     "20s",
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Resilience.Retry.InitialBackoff != 500*time.Millisecond {
					t.Errorf("InitialBackoff = %v, want 500ms", cfg.Resilience.Retry.InitialBackoff)
				}
				if cfg.Resilience.Retry.MaxBackoff != 20*time.Second {
					t.Errorf("MaxBackoff = %v, want 20s", cfg.Resilience.Retry.MaxBackoff)
				}
			},
		},
		{
			name: "retry float overrides",
			envVars: map[string]string{
				"RETRY_BACKOFF_FACTOR": "3.5",
				"RETRY_JITTER_FACTOR":  "0.25",
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Resilience.Retry.BackoffFactor != 3.5 {
					t.Errorf("BackoffFactor = %f, want 3.5", cfg.Resilience.Retry.BackoffFactor)
				}
				if cfg.Resilience.Retry.JitterFactor != 0.25 {
					t.Errorf("JitterFactor = %f, want 0.25", cfg.Resilience.Retry.JitterFactor)
				}
			},
		},
		{
			name: "circuit breaker int overrides",
			envVars: map[string]string{
				"CIRCUIT_BREAKER_FAILURE_THRESHOLD": "3",
				"CIRCUIT_BREAKER_SUCCESS_THRESHOLD": "1",
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Resilience.CircuitBreaker.FailureThreshold != 3 {
					t.Errorf("FailureThreshold = %d, want 3", cfg.Resilience.CircuitBreaker.FailureThreshold)
				}
				if cfg.Resilience.CircuitBreaker.SuccessThreshold != 1 {
					t.Errorf("SuccessThreshold = %d, want 1", cfg.Resilience.CircuitBreaker.SuccessThreshold)
				}
			},
		},
		{
			name:    "circuit breaker timeout override",
			envVars: map[string]string{"CIRCUIT_BREAKER_TIMEOUT": "10s"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Resilience.CircuitBreaker.Timeout != 10*time.Second {
					t.Errorf("Timeout = %v, want 10s", cfg.Resilience.CircuitBreaker.Timeout)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			cfg := buildDefaultConfig()
			require.NoError(t, applyEnvOverrides(cfg))
			tt.check(t, cfg)
		})
	}
}
