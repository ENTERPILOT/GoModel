package config

import (
	"os"
	"path/filepath"
	"testing"
)

// clearProviderEnvVars unsets all known provider-related environment variables.
func clearProviderEnvVars(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"OPENAI_API_KEY", "OPENAI_BASE_URL",
		"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL",
		"GEMINI_API_KEY", "GEMINI_BASE_URL",
		"XAI_API_KEY", "XAI_BASE_URL",
		"GROQ_API_KEY", "GROQ_BASE_URL",
		"OLLAMA_API_KEY", "OLLAMA_BASE_URL",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

// clearAllConfigEnvVars unsets all config-related environment variables.
func clearAllConfigEnvVars(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"PORT", "GOMODEL_MASTER_KEY", "BODY_SIZE_LIMIT", "ENABLE_PROVIDER_PASSTHROUGH", "NORMALIZE_OPENAI_COMPATIBLE_PASSTHROUGH_V1_PREFIX",
		"GOMODEL_CACHE_DIR", "CACHE_REFRESH_INTERVAL",
		"REDIS_URL", "REDIS_KEY_MODELS", "REDIS_KEY_RESPONSES", "REDIS_TTL_MODELS", "REDIS_TTL_RESPONSES",
		"STORAGE_TYPE", "SQLITE_PATH", "POSTGRES_URL", "POSTGRES_MAX_CONNS",
		"MONGODB_URL", "MONGODB_DATABASE",
		"METRICS_ENABLED", "METRICS_ENDPOINT",
		"LOGGING_ENABLED", "LOGGING_LOG_BODIES", "LOGGING_LOG_HEADERS",
		"LOGGING_ONLY_MODEL_INTERACTIONS", "LOGGING_BUFFER_SIZE",
		"LOGGING_FLUSH_INTERVAL", "LOGGING_RETENTION_DAYS",
		"USAGE_ENABLED", "ENFORCE_RETURNING_USAGE_DATA",
		"USAGE_BUFFER_SIZE", "USAGE_FLUSH_INTERVAL", "USAGE_RETENTION_DAYS",
		"GUARDRAILS_ENABLED", "ENABLE_GUARDRAILS_FOR_BATCH_PROCESSING",
		"HTTP_TIMEOUT", "HTTP_RESPONSE_HEADER_TIMEOUT",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	clearProviderEnvVars(t)
}

// withTempDir runs fn in a temporary directory, restoring the original working directory afterward.
func withTempDir(t *testing.T, fn func(dir string)) {
	t.Helper()
	tempDir := t.TempDir()
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	fn(tempDir)
}

func TestBuildDefaultConfig(t *testing.T) {
	cfg := buildDefaultConfig()

	if cfg.Server.Port != "8080" {
		t.Errorf("expected Server.Port=8080, got %s", cfg.Server.Port)
	}
	if !cfg.Server.EnableProviderPassthrough {
		t.Error("expected Server.EnableProviderPassthrough=true")
	}
	if !cfg.Server.NormalizeOpenAICompatiblePassthroughV1Prefix {
		t.Error("expected Server.NormalizeOpenAICompatiblePassthroughV1Prefix=true")
	}
	if cfg.Cache.Model.Local != nil {
		t.Error("expected Cache.Model.Local to be nil in raw defaults")
	}
	if cfg.Cache.Model.RefreshInterval != 3600 {
		t.Errorf("expected Cache.Model.RefreshInterval=3600, got %d", cfg.Cache.Model.RefreshInterval)
	}
	if cfg.Storage.Type != "sqlite" {
		t.Errorf("expected Storage.Type=sqlite, got %s", cfg.Storage.Type)
	}
	if cfg.Storage.SQLite.Path != "data/gomodel.db" {
		t.Errorf("expected Storage.SQLite.Path=data/gomodel.db, got %s", cfg.Storage.SQLite.Path)
	}
	if cfg.Storage.PostgreSQL.MaxConns != 10 {
		t.Errorf("expected Storage.PostgreSQL.MaxConns=10, got %d", cfg.Storage.PostgreSQL.MaxConns)
	}
	if cfg.Storage.MongoDB.Database != "gomodel" {
		t.Errorf("expected Storage.MongoDB.Database=gomodel, got %s", cfg.Storage.MongoDB.Database)
	}
	if !cfg.Logging.LogBodies {
		t.Error("expected Logging.LogBodies=true")
	}
	if !cfg.Logging.LogHeaders {
		t.Error("expected Logging.LogHeaders=true")
	}
	if cfg.Logging.BufferSize != 1000 {
		t.Errorf("expected Logging.BufferSize=1000, got %d", cfg.Logging.BufferSize)
	}
	if cfg.Logging.FlushInterval != 5 {
		t.Errorf("expected Logging.FlushInterval=5, got %d", cfg.Logging.FlushInterval)
	}
	if cfg.Logging.RetentionDays != 30 {
		t.Errorf("expected Logging.RetentionDays=30, got %d", cfg.Logging.RetentionDays)
	}
	if !cfg.Logging.OnlyModelInteractions {
		t.Error("expected Logging.OnlyModelInteractions=true")
	}
	if cfg.Logging.Enabled {
		t.Error("expected Logging.Enabled=false")
	}
	if !cfg.Usage.Enabled {
		t.Error("expected Usage.Enabled=true")
	}
	if !cfg.Usage.EnforceReturningUsageData {
		t.Error("expected Usage.EnforceReturningUsageData=true")
	}
	if cfg.Usage.BufferSize != 1000 {
		t.Errorf("expected Usage.BufferSize=1000, got %d", cfg.Usage.BufferSize)
	}
	if cfg.Usage.FlushInterval != 5 {
		t.Errorf("expected Usage.FlushInterval=5, got %d", cfg.Usage.FlushInterval)
	}
	if cfg.Usage.RetentionDays != 90 {
		t.Errorf("expected Usage.RetentionDays=90, got %d", cfg.Usage.RetentionDays)
	}
	if cfg.Metrics.Endpoint != "/metrics" {
		t.Errorf("expected Metrics.Endpoint=/metrics, got %s", cfg.Metrics.Endpoint)
	}
	if cfg.Metrics.Enabled {
		t.Error("expected Metrics.Enabled=false")
	}
	if cfg.HTTP.Timeout != 600 {
		t.Errorf("expected HTTP.Timeout=600, got %d", cfg.HTTP.Timeout)
	}
	if cfg.HTTP.ResponseHeaderTimeout != 600 {
		t.Errorf("expected HTTP.ResponseHeaderTimeout=600, got %d", cfg.HTTP.ResponseHeaderTimeout)
	}
	if cfg.Guardrails.EnableForBatchProcessing {
		t.Error("expected Guardrails.EnableForBatchProcessing=false")
	}

	expectedRetry := DefaultRetryConfig()
	if cfg.Resilience.Retry != expectedRetry {
		t.Errorf("expected Resilience.Retry=%+v, got %+v", expectedRetry, cfg.Resilience.Retry)
	}

	expectedCB := DefaultCircuitBreakerConfig()
	if cfg.Resilience.CircuitBreaker != expectedCB {
		t.Errorf("expected Resilience.CircuitBreaker=%+v, got %+v", expectedCB, cfg.Resilience.CircuitBreaker)
	}
}

func TestLoad_ZeroConfig(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if result.Config.Server.Port != "8080" {
			t.Errorf("expected default port 8080, got %s", result.Config.Server.Port)
		}
		if len(result.RawProviders) != 0 {
			t.Errorf("expected no raw providers, got %d", len(result.RawProviders))
		}
	})
}

func TestLoad_YAMLOverridesDefaults(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
server:
  port: "3000"
cache:
  model:
    redis:
      url: "redis://myhost:6379"
      key: "custom:key"
      ttl: 3600
logging:
  enabled: true
  log_bodies: false
  buffer_size: 500
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		cfg := result.Config

		if cfg.Server.Port != "3000" {
			t.Errorf("expected port 3000, got %s", cfg.Server.Port)
		}
		if cfg.Cache.Model.Redis == nil {
			t.Fatal("expected Cache.Model.Redis to be set")
		}
		if cfg.Cache.Model.Redis.URL != "redis://myhost:6379" {
			t.Errorf("expected redis URL redis://myhost:6379, got %s", cfg.Cache.Model.Redis.URL)
		}
		if cfg.Cache.Model.Redis.Key != "custom:key" {
			t.Errorf("expected redis key custom:key, got %s", cfg.Cache.Model.Redis.Key)
		}
		if cfg.Cache.Model.Redis.TTL != 3600 {
			t.Errorf("expected redis TTL 3600, got %d", cfg.Cache.Model.Redis.TTL)
		}
		if cfg.Cache.Model.Local != nil {
			t.Errorf("expected Cache.Model.Local to be nil when redis is configured, got %v", cfg.Cache.Model.Local)
		}
		if !cfg.Logging.Enabled {
			t.Error("expected Logging.Enabled=true from YAML")
		}
		if cfg.Logging.LogBodies {
			t.Error("expected Logging.LogBodies=false from YAML")
		}
		if cfg.Logging.BufferSize != 500 {
			t.Errorf("expected Logging.BufferSize=500, got %d", cfg.Logging.BufferSize)
		}
		if cfg.Logging.FlushInterval != 5 {
			t.Errorf("expected Logging.FlushInterval=5 (default), got %d", cfg.Logging.FlushInterval)
		}
		if cfg.Storage.Type != "sqlite" {
			t.Errorf("expected Storage.Type=sqlite (default), got %s", cfg.Storage.Type)
		}
	})
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
server:
  port: "3000"
cache:
  model:
    local: null
    redis:
      url: "redis://myhost:6379"
logging:
  enabled: true
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		t.Setenv("PORT", "9090")
		t.Setenv("CACHE_REFRESH_INTERVAL", "1800")
		t.Setenv("LOGGING_ENABLED", "false")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		cfg := result.Config

		if cfg.Server.Port != "9090" {
			t.Errorf("expected port 9090 (env override), got %s", cfg.Server.Port)
		}
		if cfg.Cache.Model.RefreshInterval != 1800 {
			t.Errorf("expected Cache.Model.RefreshInterval=1800 (env override), got %d", cfg.Cache.Model.RefreshInterval)
		}
		if cfg.Logging.Enabled {
			t.Error("expected Logging.Enabled=false (env override)")
		}
	})
}

func TestLoad_EnvOverridesDefaults(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		t.Setenv("PORT", "5555")
		t.Setenv("STORAGE_TYPE", "postgresql")
		t.Setenv("POSTGRES_URL", "postgres://localhost/test")
		t.Setenv("POSTGRES_MAX_CONNS", "20")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		cfg := result.Config

		if cfg.Server.Port != "5555" {
			t.Errorf("expected port 5555, got %s", cfg.Server.Port)
		}
		if cfg.Storage.Type != "postgresql" {
			t.Errorf("expected storage type postgresql, got %s", cfg.Storage.Type)
		}
		if cfg.Storage.PostgreSQL.URL != "postgres://localhost/test" {
			t.Errorf("expected postgres URL, got %s", cfg.Storage.PostgreSQL.URL)
		}
		if cfg.Storage.PostgreSQL.MaxConns != 20 {
			t.Errorf("expected max conns 20, got %d", cfg.Storage.PostgreSQL.MaxConns)
		}
	})
}

func TestLoad_ProviderFromYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
providers:
  openai:
    type: openai
    api_key: "sk-yaml-key"
    base_url: "https://custom.openai.com"
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		provider, exists := result.RawProviders["openai"]
		if !exists {
			t.Fatal("expected 'openai' raw provider to exist")
		}
		if provider.APIKey != "sk-yaml-key" {
			t.Errorf("expected API key sk-yaml-key, got %s", provider.APIKey)
		}
		if provider.BaseURL != "https://custom.openai.com" {
			t.Errorf("expected base URL https://custom.openai.com, got %s", provider.BaseURL)
		}
	})
}

func TestLoad_ProviderResilienceInRawProviders(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yamlContent := `
resilience:
  retry:
    max_retries: 5
providers:
  openai:
    type: openai
    api_key: "sk-yaml-key"
    resilience:
      retry:
        max_retries: 10
  anthropic:
    type: anthropic
    api_key: "sk-ant-key"
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlContent), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if result.Config.Resilience.Retry.MaxRetries != 5 {
			t.Errorf("expected global MaxRetries=5, got %d", result.Config.Resilience.Retry.MaxRetries)
		}

		openai, exists := result.RawProviders["openai"]
		if !exists {
			t.Fatal("expected openai in raw providers")
		}
		if openai.Resilience == nil || openai.Resilience.Retry == nil || *openai.Resilience.Retry.MaxRetries != 10 {
			t.Error("expected openai raw provider to have MaxRetries override of 10")
		}

		_, exists = result.RawProviders["anthropic"]
		if !exists {
			t.Fatal("expected anthropic in raw providers")
		}
	})
}

func TestLoad_HTTPConfig(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if result.Config.HTTP.Timeout != 600 {
			t.Errorf("expected HTTP.Timeout=600, got %d", result.Config.HTTP.Timeout)
		}
		if result.Config.HTTP.ResponseHeaderTimeout != 600 {
			t.Errorf("expected HTTP.ResponseHeaderTimeout=600, got %d", result.Config.HTTP.ResponseHeaderTimeout)
		}
	})

	withTempDir(t, func(_ string) {
		t.Setenv("HTTP_TIMEOUT", "30")
		t.Setenv("HTTP_RESPONSE_HEADER_TIMEOUT", "60")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if result.Config.HTTP.Timeout != 30 {
			t.Errorf("expected HTTP.Timeout=30, got %d", result.Config.HTTP.Timeout)
		}
		if result.Config.HTTP.ResponseHeaderTimeout != 60 {
			t.Errorf("expected HTTP.ResponseHeaderTimeout=60, got %d", result.Config.HTTP.ResponseHeaderTimeout)
		}
	})
}

func TestLoad_CacheDir(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if result.Config.Cache.Model.Local == nil {
			t.Error("expected Cache.Model.Local to be set by default")
		}
	})

	withTempDir(t, func(_ string) {
		t.Setenv("GOMODEL_CACHE_DIR", "/tmp/gomodel-cache")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if result.Config.Cache.Model.Local == nil || result.Config.Cache.Model.Local.CacheDir != "/tmp/gomodel-cache" {
			t.Errorf("expected Cache.Model.Local.CacheDir=/tmp/gomodel-cache, got %v", result.Config.Cache.Model.Local)
		}
	})
}

func TestLoad_LoggingOnlyModelInteractionsDefault(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if !result.Config.Logging.OnlyModelInteractions {
			t.Error("expected OnlyModelInteractions to default to true")
		}
	})
}

func TestLoad_LoggingOnlyModelInteractionsFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected bool
	}{
		{"true lowercase", "true", true},
		{"TRUE uppercase", "TRUE", true},
		{"True mixed", "True", true},
		{"false lowercase", "false", false},
		{"FALSE uppercase", "FALSE", false},
		{"False mixed", "False", false},
		{"1 numeric", "1", true},
		{"0 numeric", "0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)

			withTempDir(t, func(_ string) {
				t.Setenv("LOGGING_ONLY_MODEL_INTERACTIONS", tt.envValue)

				result, err := Load()
				if err != nil {
					t.Fatalf("Load() failed: %v", err)
				}

				if result.Config.Logging.OnlyModelInteractions != tt.expected {
					t.Errorf("expected OnlyModelInteractions=%v for env value %q, got %v",
						tt.expected, tt.envValue, result.Config.Logging.OnlyModelInteractions)
				}
			})
		})
	}
}

func TestLoad_YAMLWithEnvVarExpansion(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
server:
  port: "${TEST_PORT_CFG:-9999}"
providers:
  openai:
    type: "openai"
    api_key: "${TEST_KEY_CFG:-default-key}"
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if result.Config.Server.Port != "9999" {
			t.Errorf("expected port 9999 (YAML default), got %s", result.Config.Server.Port)
		}
		provider, exists := result.RawProviders["openai"]
		if !exists {
			t.Fatal("expected openai in raw providers")
		}
		if provider.APIKey != "default-key" {
			t.Errorf("expected API key 'default-key', got %s", provider.APIKey)
		}
	})
}

func TestLoad_YAMLWithEnvVarOverride(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
server:
  port: "${TEST_PORT_CFG:-9999}"
providers:
  openai:
    type: "openai"
    api_key: "${TEST_KEY_CFG:-default-key}"
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		t.Setenv("TEST_PORT_CFG", "1111")
		t.Setenv("TEST_KEY_CFG", "real-key")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if result.Config.Server.Port != "1111" {
			t.Errorf("expected port 1111 (env override), got %s", result.Config.Server.Port)
		}
		provider, exists := result.RawProviders["openai"]
		if !exists {
			t.Fatal("expected openai in raw providers")
		}
		if provider.APIKey != "real-key" {
			t.Errorf("expected API key 'real-key', got %s", provider.APIKey)
		}
	})
}

func TestLoad_YAMLInConfigSubdir(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		configDir := filepath.Join(dir, "config")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			t.Fatalf("Failed to create config dir: %v", err)
		}

		yaml := `
server:
  port: "4444"
`
		if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config/config.yaml: %v", err)
		}

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if result.Config.Server.Port != "4444" {
			t.Errorf("expected port 4444 from config/config.yaml, got %s", result.Config.Server.Port)
		}
	})
}

func TestValidateBodySizeLimit(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
	}{
		{"empty string is valid", "", false},
		{"plain number", "1048576", false},
		{"kilobytes lowercase", "100k", false},
		{"kilobytes uppercase", "100K", false},
		{"kilobytes with B suffix", "100KB", false},
		{"megabytes lowercase", "10m", false},
		{"megabytes uppercase", "10M", false},
		{"megabytes with B suffix", "10MB", false},
		{"whitespace trimmed", "  10M  ", false},
		{"minimum valid (1KB)", "1K", false},
		{"maximum valid (100MB)", "100M", false},
		{"invalid format with letters", "abc", true},
		{"invalid unit", "10X", true},
		{"negative number", "-10M", true},
		{"decimal number", "10.5M", true},
		{"empty unit with B", "10B", true},
		{"below minimum (100 bytes)", "100", true},
		{"above maximum (200MB)", "200M", true},
		{"above maximum (1GB)", "1G", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBodySizeLimit(tt.input)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error for input %q, got nil", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for input %q: %v", tt.input, err)
				}
			}
		})
	}
}

func TestLoad_EnvOnlyRedisModelCache(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		t.Setenv("REDIS_URL", "redis://env-host:6379")
		t.Setenv("REDIS_KEY_MODELS", "env:models")
		t.Setenv("REDIS_TTL_MODELS", "7200")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		cfg := result.Config

		if cfg.Cache.Model.Redis == nil {
			t.Fatal("expected Cache.Model.Redis to be allocated from env vars")
		}
		if cfg.Cache.Model.Redis.URL != "redis://env-host:6379" {
			t.Errorf("expected REDIS_URL=redis://env-host:6379, got %s", cfg.Cache.Model.Redis.URL)
		}
		if cfg.Cache.Model.Redis.Key != "env:models" {
			t.Errorf("expected REDIS_KEY_MODELS=env:models, got %s", cfg.Cache.Model.Redis.Key)
		}
		if cfg.Cache.Model.Redis.TTL != 7200 {
			t.Errorf("expected REDIS_TTL_MODELS=7200, got %d", cfg.Cache.Model.Redis.TTL)
		}
		if cfg.Cache.Model.Local != nil {
			t.Errorf("expected Cache.Model.Local to be nil when Redis is configured via env, got %v", cfg.Cache.Model.Local)
		}
	})
}

func TestLoad_EnvOnlyRedisResponseCache(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		t.Setenv("REDIS_URL", "redis://env-host:6379")
		t.Setenv("REDIS_KEY_RESPONSES", "env:responses")
		t.Setenv("REDIS_TTL_RESPONSES", "1800")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		cfg := result.Config

		if cfg.Cache.Response.Simple.Redis == nil {
			t.Fatal("expected Cache.Response.Simple.Redis to be allocated from env vars")
		}
		if cfg.Cache.Response.Simple.Redis.URL != "redis://env-host:6379" {
			t.Errorf("expected REDIS_URL=redis://env-host:6379, got %s", cfg.Cache.Response.Simple.Redis.URL)
		}
		if cfg.Cache.Response.Simple.Redis.Key != "env:responses" {
			t.Errorf("expected REDIS_KEY_RESPONSES=env:responses, got %s", cfg.Cache.Response.Simple.Redis.Key)
		}
		if cfg.Cache.Response.Simple.Redis.TTL != 1800 {
			t.Errorf("expected REDIS_TTL_RESPONSES=1800, got %d", cfg.Cache.Response.Simple.Redis.TTL)
		}
	})
}

func TestParseBodySizeLimitBytes(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    int64
		expectError bool
	}{
		{"empty string", "", 0, false},
		{"plain number", "1048576", 1048576, false},
		{"kilobytes", "2K", 2 * 1024, false},
		{"megabytes", "10MB", 10 * 1024 * 1024, false},
		{"whitespace trimmed", " 1M ", 1024 * 1024, false},
		{"invalid format", "10B", 0, true},
		{"below minimum", "100", 0, true},
		{"above maximum", "1G", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBodySizeLimitBytes(tt.input)
			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.input, err)
			}
			if got != tt.expected {
				t.Fatalf("ParseBodySizeLimitBytes(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}
