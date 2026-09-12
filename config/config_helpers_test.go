package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
			assert.Equal(t, tt.expected, result, "expandString(%q)", tt.input)
		})
	}
}

func TestNormalizeBasePath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "empty defaults to root", input: "", expected: "/"},
		{name: "root remains root", input: "/", expected: "/"},
		{name: "adds leading slash", input: "g", expected: "/g"},
		{name: "trims trailing slash", input: "/g/", expected: "/g"},
		{name: "cleans duplicate separators", input: "//g//api/", expected: "/g/api"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeBasePath(tt.input)
			assert.Equal(t, tt.expected, got, "NormalizeBasePath(%q)", tt.input)
		})
	}
}

func TestJoinBasePath(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		urlPath  string
		expected string
	}{
		{name: "root leaves absolute path unchanged", basePath: "/", urlPath: "/admin", expected: "/admin"},
		{name: "root adds leading slash", basePath: "/", urlPath: "admin", expected: "/admin"},
		{name: "prefixes normalized base path", basePath: "g/", urlPath: "/admin", expected: "/g/admin"},
		{name: "empty app path resolves to base path", basePath: "/g", urlPath: "", expected: "/g"},
		{name: "root app path resolves to base path", basePath: "/g", urlPath: "/", expected: "/g"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := JoinBasePath(tt.basePath, tt.urlPath)
			assert.Equal(t, tt.expected, got, "JoinBasePath(%q, %q)", tt.basePath, tt.urlPath)
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
				assert.Equal(t, "3000", cfg.Server.Port)
			},
		},
		{
			name:    "GOMODEL_MASTER_KEY override",
			envVars: map[string]string{"GOMODEL_MASTER_KEY": "my-secret"},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "my-secret", cfg.Server.MasterKey)
			},
		},
		{
			name:    "PPROF_ENABLED override",
			envVars: map[string]string{"PPROF_ENABLED": "true"},
			check: func(t *testing.T, cfg *Config) {
				assert.True(t, cfg.Server.PprofEnabled)
			},
		},
		{
			name:    "passthrough v1 normalization override",
			envVars: map[string]string{"ALLOW_PASSTHROUGH_V1_ALIAS": "false"},
			check: func(t *testing.T, cfg *Config) {
				assert.False(t, cfg.Server.AllowPassthroughV1Alias)
			},
		},
		{
			name:    "storage overrides",
			envVars: map[string]string{"STORAGE_TYPE": "postgresql", "POSTGRES_URL": "postgres://localhost/test", "POSTGRES_MAX_CONNS": "20"},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "postgresql", cfg.Storage.Type)
				assert.Equal(t, "postgres://localhost/test", cfg.Storage.PostgreSQL.URL)
				assert.Equal(t, 20, cfg.Storage.PostgreSQL.MaxConns)
			},
		},
		{
			name:    "bool overrides",
			envVars: map[string]string{"METRICS_ENABLED": "true", "LOGGING_ENABLED": "1", "LOGGING_LOG_BODIES": "false"},
			check: func(t *testing.T, cfg *Config) {
				assert.True(t, cfg.Metrics.Enabled)
				assert.True(t, cfg.Logging.Enabled)
				assert.False(t, cfg.Logging.LogBodies)
			},
		},
		{
			name:    "guardrails batch flag override",
			envVars: map[string]string{"ENABLE_GUARDRAILS_FOR_BATCH_PROCESSING": "true"},
			check: func(t *testing.T, cfg *Config) {
				assert.True(t, cfg.Guardrails.EnableForBatchProcessing)
			},
		},
		{
			name:    "HTTP timeout overrides",
			envVars: map[string]string{"HTTP_TIMEOUT": "30", "HTTP_RESPONSE_HEADER_TIMEOUT": "60"},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 30, cfg.HTTP.Timeout)
				assert.Equal(t, 60, cfg.HTTP.ResponseHeaderTimeout)
			},
		},
		{
			name:    "CACHE_REFRESH_INTERVAL override",
			envVars: map[string]string{"CACHE_REFRESH_INTERVAL": "1800"},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 1800, cfg.Cache.Model.RefreshInterval)
			},
		},
		{
			name:    "no env vars set preserves defaults",
			envVars: map[string]string{},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "8080", cfg.Server.Port)
				assert.Equal(t, 600, cfg.HTTP.Timeout)
			},
		},
		{
			name:    "retry int override",
			envVars: map[string]string{"RETRY_MAX_RETRIES": "7"},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 7, cfg.Resilience.Retry.MaxRetries)
			},
		},
		{
			name: "retry duration overrides",
			envVars: map[string]string{
				"RETRY_INITIAL_BACKOFF": "500ms",
				"RETRY_MAX_BACKOFF":     "20s",
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 500*time.Millisecond, cfg.Resilience.Retry.InitialBackoff)
				assert.Equal(t, 20*time.Second, cfg.Resilience.Retry.MaxBackoff)
			},
		},
		{
			name: "retry float overrides",
			envVars: map[string]string{
				"RETRY_BACKOFF_FACTOR": "3.5",
				"RETRY_JITTER_FACTOR":  "0.25",
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 3.5, cfg.Resilience.Retry.BackoffFactor)
				assert.Equal(t, 0.25, cfg.Resilience.Retry.JitterFactor)
			},
		},
		{
			name: "circuit breaker int overrides",
			envVars: map[string]string{
				"CIRCUIT_BREAKER_FAILURE_THRESHOLD": "3",
				"CIRCUIT_BREAKER_SUCCESS_THRESHOLD": "1",
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 3, cfg.Resilience.CircuitBreaker.FailureThreshold)
				assert.Equal(t, 1, cfg.Resilience.CircuitBreaker.SuccessThreshold)
			},
		},
		{
			name:    "provider passthrough override",
			envVars: map[string]string{"ENABLE_PASSTHROUGH_ROUTES": "false"},
			check: func(t *testing.T, cfg *Config) {
				assert.False(t, cfg.Server.EnablePassthroughRoutes)
			},
		},
		{
			name: "circuit breaker enabled by default",
			// Empty value clears any CIRCUIT_BREAKER_ENABLED inherited from the
			// test process (t.Setenv restores it) so the default is isolated.
			envVars: map[string]string{"CIRCUIT_BREAKER_ENABLED": ""},
			check: func(t *testing.T, cfg *Config) {
				assert.True(t, cfg.Resilience.CircuitBreaker.Enabled)
			},
		},
		{
			name:    "circuit breaker enabled override",
			envVars: map[string]string{"CIRCUIT_BREAKER_ENABLED": "false"},
			check: func(t *testing.T, cfg *Config) {
				assert.False(t, cfg.Resilience.CircuitBreaker.Enabled)

				// Disabling must not clobber the tuning values.
				assert.Equal(t, 5, cfg.Resilience.CircuitBreaker.FailureThreshold)
			},
		},
		{
			name:    "circuit breaker timeout override",
			envVars: map[string]string{"CIRCUIT_BREAKER_TIMEOUT": "10s"},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 10*time.Second, cfg.Resilience.CircuitBreaker.Timeout)
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
