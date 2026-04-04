package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"time"
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
		"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "OPENROUTER_SITE_URL", "OPENROUTER_APP_NAME",
		"AZURE_API_KEY", "AZURE_BASE_URL", "AZURE_API_VERSION",
		"ORACLE_API_KEY", "ORACLE_BASE_URL",
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
		"PORT", "GOMODEL_MASTER_KEY", "BODY_SIZE_LIMIT", "SWAGGER_ENABLED", "PPROF_ENABLED", "ENABLE_PASSTHROUGH_ROUTES", "ALLOW_PASSTHROUGH_V1_ALIAS", "ENABLED_PASSTHROUGH_PROVIDERS",
		"GOMODEL_CACHE_DIR", "CACHE_REFRESH_INTERVAL",
		"REDIS_URL", "REDIS_KEY_MODELS", "REDIS_KEY_RESPONSES", "REDIS_TTL_MODELS", "REDIS_TTL_RESPONSES",
		"RESPONSE_CACHE_SIMPLE_ENABLED",
		"SEMANTIC_CACHE_ENABLED", "SEMANTIC_CACHE_THRESHOLD", "SEMANTIC_CACHE_TTL", "SEMANTIC_CACHE_MAX_CONV_MESSAGES",
		"SEMANTIC_CACHE_EXCLUDE_SYSTEM_PROMPT", "SEMANTIC_CACHE_EMBEDDER_PROVIDER", "SEMANTIC_CACHE_EMBEDDER_MODEL",
		"SEMANTIC_CACHE_VECTOR_STORE_TYPE",
		"SEMANTIC_CACHE_QDRANT_URL", "SEMANTIC_CACHE_QDRANT_COLLECTION", "SEMANTIC_CACHE_QDRANT_API_KEY",
		"SEMANTIC_CACHE_PGVECTOR_URL", "SEMANTIC_CACHE_PGVECTOR_TABLE", "SEMANTIC_CACHE_PGVECTOR_DIMENSION",
		"SEMANTIC_CACHE_PINECONE_HOST", "SEMANTIC_CACHE_PINECONE_API_KEY", "SEMANTIC_CACHE_PINECONE_NAMESPACE", "SEMANTIC_CACHE_PINECONE_DIMENSION",
		"SEMANTIC_CACHE_WEAVIATE_URL", "SEMANTIC_CACHE_WEAVIATE_CLASS", "SEMANTIC_CACHE_WEAVIATE_API_KEY",
		"STORAGE_TYPE", "SQLITE_PATH", "POSTGRES_URL", "POSTGRES_MAX_CONNS",
		"MONGODB_URL", "MONGODB_DATABASE",
		"METRICS_ENABLED", "METRICS_ENDPOINT",
		"LOGGING_ENABLED", "LOGGING_LOG_BODIES", "LOGGING_LOG_HEADERS",
		"LOGGING_ONLY_MODEL_INTERACTIONS", "LOGGING_BUFFER_SIZE",
		"LOGGING_FLUSH_INTERVAL", "LOGGING_RETENTION_DAYS",
		"USAGE_ENABLED", "ENFORCE_RETURNING_USAGE_DATA",
		"USAGE_BUFFER_SIZE", "USAGE_FLUSH_INTERVAL", "USAGE_RETENTION_DAYS",
		"GUARDRAILS_ENABLED", "ENABLE_GUARDRAILS_FOR_BATCH_PROCESSING",
		"FEATURE_FALLBACK_MODE", "FALLBACK_MANUAL_RULES_PATH",
		"HTTP_TIMEOUT", "HTTP_RESPONSE_HEADER_TIMEOUT",
		"EXECUTION_PLAN_REFRESH_INTERVAL",
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
	if cfg.Server.PprofEnabled {
		t.Error("expected Server.PprofEnabled=false")
	}
	if !cfg.Server.EnablePassthroughRoutes {
		t.Error("expected Server.EnablePassthroughRoutes=true")
	}
	if !cfg.Server.AllowPassthroughV1Alias {
		t.Error("expected Server.AllowPassthroughV1Alias=true")
	}
	if got, want := cfg.Server.EnabledPassthroughProviders, []string{"openai", "anthropic"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("expected Server.EnabledPassthroughProviders=%v, got %v", want, got)
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
	if cfg.ExecutionPlans.RefreshInterval != time.Minute {
		t.Errorf("expected ExecutionPlans.RefreshInterval=%s, got %s", time.Minute, cfg.ExecutionPlans.RefreshInterval)
	}
	if cfg.Guardrails.EnableForBatchProcessing {
		t.Error("expected Guardrails.EnableForBatchProcessing=false")
	}
	if cfg.Fallback.DefaultMode != FallbackModeAuto {
		t.Errorf("expected Fallback.DefaultMode=auto, got %q", cfg.Fallback.DefaultMode)
	}
	if cfg.Cache.Response.Simple != nil {
		t.Errorf("expected Cache.Response.Simple=nil in defaults, got %+v", cfg.Cache.Response.Simple)
	}
	if cfg.Cache.Response.Semantic != nil {
		t.Errorf("expected Cache.Response.Semantic=nil in defaults, got %+v", cfg.Cache.Response.Semantic)
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
  pprof_enabled: true
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
		if !cfg.Server.PprofEnabled {
			t.Error("expected Server.PprofEnabled=true from YAML")
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

func TestLoad_FallbackManualRules(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		manualRulesPath := filepath.Join(dir, "fallback.json")
		if err := os.WriteFile(manualRulesPath, []byte(`{
			"gpt-4o": ["azure/gpt-4o", "gemini/gemini-2.5-pro"],
			"claude-sonnet-4": ["openai/gpt-5-mini"]
		}`), 0644); err != nil {
			t.Fatalf("Failed to write fallback rules: %v", err)
		}

		type yamlConfig struct {
			Fallback struct {
				DefaultMode     string                       `yaml:"default_mode"`
				ManualRulesPath string                       `yaml:"manual_rules_path"`
				Overrides       map[string]map[string]string `yaml:"overrides"`
			} `yaml:"fallback"`
		}

		yamlCfg := yamlConfig{}
		yamlCfg.Fallback.DefaultMode = "auto"
		yamlCfg.Fallback.ManualRulesPath = manualRulesPath
		yamlCfg.Fallback.Overrides = map[string]map[string]string{
			"gpt-4o": {"mode": "manual"},
		}

		yamlData, err := yaml.Marshal(yamlCfg)
		if err != nil {
			t.Fatalf("Failed to marshal config.yaml: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), yamlData, 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		cfg := result.Config
		if cfg.Fallback.DefaultMode != FallbackModeAuto {
			t.Fatalf("Fallback.DefaultMode = %q, want %q", cfg.Fallback.DefaultMode, FallbackModeAuto)
		}
		if cfg.Fallback.Overrides["gpt-4o"].Mode != FallbackModeManual {
			t.Fatalf("Fallback.Overrides[gpt-4o].Mode = %q, want %q", cfg.Fallback.Overrides["gpt-4o"].Mode, FallbackModeManual)
		}
		got := cfg.Fallback.Manual["gpt-4o"]
		want := []string{"azure/gpt-4o", "gemini/gemini-2.5-pro"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("Fallback.Manual[gpt-4o] = %v, want %v", got, want)
		}
	})
}

func TestLoad_InvalidFallbackMode(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
fallback:
  default_mode: invalid
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		if _, err := Load(); err == nil {
			t.Fatal("expected Load() to fail for invalid fallback mode")
		}
	})
}

func TestLoad_EmptyFallbackOverrideMode(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
fallback:
  overrides:
    "gpt-4o": {}
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		if _, err := Load(); err == nil {
			t.Fatal("expected Load() to fail for empty fallback override mode")
		}
	})
}

func TestLoad_ManualFallbackModeRequiresManualRulesPath(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
fallback:
  default_mode: manual
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		_, err := Load()
		if err == nil {
			t.Fatal("expected Load() to fail when manual fallback mode has no manual rules path")
		}
		if !strings.Contains(err.Error(), "fallback.manual_rules_path must be set") {
			t.Fatalf("Load() error = %v, want manual rules path validation", err)
		}
	})
}

func TestLoad_ManualFallbackOverrideRequiresManualRulesPath(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
fallback:
  overrides:
    "gpt-4o":
      mode: manual
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		_, err := Load()
		if err == nil {
			t.Fatal("expected Load() to fail when manual fallback override has no manual rules path")
		}
		if !strings.Contains(err.Error(), "fallback.manual_rules_path must be set") {
			t.Fatalf("Load() error = %v, want manual rules path validation", err)
		}
	})
}

func TestLoad_FallbackOverrideDuplicateKeyAfterTrim(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
fallback:
  overrides:
    "gpt-4o":
      mode: manual
    " gpt-4o ":
      mode: auto
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		_, err := Load()
		if err == nil {
			t.Fatal("expected Load() to fail for duplicate fallback override keys after trimming")
		}
		if !strings.Contains(err.Error(), `fallback.overrides: duplicate model key after trimming: "gpt-4o"`) {
			t.Fatalf("Load() error = %v, want duplicate trimmed override key error", err)
		}
	})
}

func TestLoad_FallbackManualRulesDuplicateKeyAfterTrim(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		manualRulesPath := filepath.Join(dir, "fallback.json")
		if err := os.WriteFile(manualRulesPath, []byte(`{
			"gpt-4o": ["azure/gpt-4o"],
			" gpt-4o ": ["gemini/gemini-2.5-pro"]
		}`), 0644); err != nil {
			t.Fatalf("Failed to write fallback rules: %v", err)
		}

		type yamlConfig struct {
			Fallback struct {
				ManualRulesPath string `yaml:"manual_rules_path"`
			} `yaml:"fallback"`
		}

		yamlCfg := yamlConfig{}
		yamlCfg.Fallback.ManualRulesPath = manualRulesPath
		yamlData, err := yaml.Marshal(yamlCfg)
		if err != nil {
			t.Fatalf("Failed to marshal config.yaml: %v", err)
		}

		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), yamlData, 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		_, err = Load()
		if err == nil {
			t.Fatal("expected Load() to fail for duplicate fallback manual rule keys after trimming")
		}
		if !strings.Contains(err.Error(), `fallback.manual_rules_path: duplicate manual rule key after trimming: "gpt-4o"`) {
			t.Fatalf("Load() error = %v, want duplicate trimmed manual rule key error", err)
		}
	})
}

func TestLoad_FallbackManualRulesRejectsDuplicateRawJSONKeys(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		manualRulesPath := filepath.Join(dir, "fallback.json")
		if err := os.WriteFile(manualRulesPath, []byte(`{
			"gpt-4o": ["azure/gpt-4o"],
			"gpt-4o": ["gemini/gemini-2.5-pro"]
		}`), 0644); err != nil {
			t.Fatalf("Failed to write fallback rules: %v", err)
		}

		type yamlConfig struct {
			Fallback struct {
				ManualRulesPath string `yaml:"manual_rules_path"`
			} `yaml:"fallback"`
		}

		yamlCfg := yamlConfig{}
		yamlCfg.Fallback.ManualRulesPath = manualRulesPath
		yamlData, err := yaml.Marshal(yamlCfg)
		if err != nil {
			t.Fatalf("Failed to marshal config.yaml: %v", err)
		}

		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), yamlData, 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		_, err = Load()
		if err == nil {
			t.Fatal("expected Load() to fail for duplicate raw JSON keys in fallback manual rules")
		}
		if !strings.Contains(err.Error(), `fallback.manual_rules_path: duplicate JSON key "gpt-4o"`) {
			t.Fatalf("Load() error = %v, want duplicate raw JSON key error", err)
		}
	})
}

func TestLoad_FallbackManualRulesRejectsNullValues(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		manualRulesPath := filepath.Join(dir, "fallback.json")
		if err := os.WriteFile(manualRulesPath, []byte(`{
			"gpt-4o": null
		}`), 0644); err != nil {
			t.Fatalf("Failed to write fallback rules: %v", err)
		}

		type yamlConfig struct {
			Fallback struct {
				ManualRulesPath string `yaml:"manual_rules_path"`
			} `yaml:"fallback"`
		}

		yamlCfg := yamlConfig{}
		yamlCfg.Fallback.ManualRulesPath = manualRulesPath
		yamlData, err := yaml.Marshal(yamlCfg)
		if err != nil {
			t.Fatalf("Failed to marshal config.yaml: %v", err)
		}

		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), yamlData, 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		_, err = Load()
		if err == nil {
			t.Fatal("expected Load() to fail for null fallback manual rule values")
		}
		if !strings.Contains(err.Error(), `fallback.manual_rules_path: null not allowed for "gpt-4o"`) {
			t.Fatalf("Load() error = %v, want null manual rule value error", err)
		}
	})
}

func TestLoad_FeatureFallbackModeEnvOverridesFallbackDefaultMode(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("FEATURE_FALLBACK_MODE", "auto")

	withTempDir(t, func(_ string) {
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if result.Config.Fallback.DefaultMode != FallbackModeAuto {
			t.Fatalf("Fallback.DefaultMode = %q, want %q", result.Config.Fallback.DefaultMode, FallbackModeAuto)
		}
	})
}

func TestLoad_PassthroughFlags_EnvOverridesYAML(t *testing.T) {
	tests := []struct {
		name          string
		yamlEnabled   string
		yamlNormalize string
		envEnabled    string
		envNormalize  string
		wantEnabled   bool
		wantNormalize bool
	}{
		{
			name:          "env true overrides yaml false",
			yamlEnabled:   "false",
			yamlNormalize: "false",
			envEnabled:    "true",
			envNormalize:  "true",
			wantEnabled:   true,
			wantNormalize: true,
		},
		{
			name:          "env false overrides yaml true",
			yamlEnabled:   "true",
			yamlNormalize: "true",
			envEnabled:    "false",
			envNormalize:  "false",
			wantEnabled:   false,
			wantNormalize: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withTempDir(t, func(dir string) {
				clearAllConfigEnvVars(t)

				yaml := `
server:
  enable_passthrough_routes: ` + tt.yamlEnabled + `
  allow_passthrough_v1_alias: ` + tt.yamlNormalize + `
`
				if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
					t.Fatalf("Failed to write config.yaml: %v", err)
				}

				t.Setenv("ENABLE_PASSTHROUGH_ROUTES", tt.envEnabled)
				t.Setenv("ALLOW_PASSTHROUGH_V1_ALIAS", tt.envNormalize)

				result, err := Load()
				if err != nil {
					t.Fatalf("Load() failed: %v", err)
				}
				if result.Config.Server.EnablePassthroughRoutes != tt.wantEnabled {
					t.Fatalf("EnablePassthroughRoutes = %v, want %v", result.Config.Server.EnablePassthroughRoutes, tt.wantEnabled)
				}
				if result.Config.Server.AllowPassthroughV1Alias != tt.wantNormalize {
					t.Fatalf("AllowPassthroughV1Alias = %v, want %v", result.Config.Server.AllowPassthroughV1Alias, tt.wantNormalize)
				}
			})
		})
	}
}

func TestLoad_PassthroughFlags_YAMLExpansion(t *testing.T) {
	withTempDir(t, func(dir string) {
		clearAllConfigEnvVars(t)
		t.Setenv("PASSTHROUGH_ENABLED_FROM_YAML", "false")
		t.Setenv("PASSTHROUGH_NORMALIZE_FROM_YAML", "")

		yaml := `
server:
  enable_passthrough_routes: ${PASSTHROUGH_ENABLED_FROM_YAML}
  allow_passthrough_v1_alias: ${PASSTHROUGH_NORMALIZE_FROM_YAML:-false}
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if result.Config.Server.EnablePassthroughRoutes {
			t.Fatal("expected YAML ${VAR} expansion to set EnablePassthroughRoutes=false")
		}
		if result.Config.Server.AllowPassthroughV1Alias {
			t.Fatal("expected YAML ${VAR:-default} expansion to set AllowPassthroughV1Alias=false")
		}
	})
}

func TestLoad_ConfigExample_UsesNestedModelCacheSettings(t *testing.T) {
	clearAllConfigEnvVars(t)

	examplePath, err := filepath.Abs("config.example.yaml")
	if err != nil {
		t.Fatalf("Failed to resolve config.example.yaml path: %v", err)
	}
	exampleData, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("Failed to read config.example.yaml: %v", err)
	}
	fallbackExamplePath, err := filepath.Abs("fallback.example.json")
	if err != nil {
		t.Fatalf("Failed to resolve fallback.example.json path: %v", err)
	}
	fallbackExampleData, err := os.ReadFile(fallbackExamplePath)
	if err != nil {
		t.Fatalf("Failed to read fallback.example.json: %v", err)
	}

	withTempDir(t, func(dir string) {
		if err := os.MkdirAll(filepath.Join(dir, "config"), 0755); err != nil {
			t.Fatalf("Failed to create config directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config", "config.yaml"), exampleData, 0644); err != nil {
			t.Fatalf("Failed to write config/config.yaml: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config", "fallback.example.json"), fallbackExampleData, 0644); err != nil {
			t.Fatalf("Failed to write fallback.example.json: %v", err)
		}

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if result.Config.Cache.Model.RefreshInterval != 3600 {
			t.Fatalf("Cache.Model.RefreshInterval = %d, want 3600", result.Config.Cache.Model.RefreshInterval)
		}
		if result.Config.Cache.Model.Local == nil {
			t.Fatal("expected Cache.Model.Local to be configured from example config")
		}
		if result.Config.Cache.Model.Local.CacheDir != ".cache" {
			t.Fatalf("Cache.Model.Local.CacheDir = %q, want .cache", result.Config.Cache.Model.Local.CacheDir)
		}
		if result.Config.Cache.Model.Redis != nil {
			t.Fatalf("expected Cache.Model.Redis to be nil in example config, got %+v", result.Config.Cache.Model.Redis)
		}
		gotProviders := result.Config.Server.EnabledPassthroughProviders
		wantProviders := []string{"openai", "anthropic"}
		if len(gotProviders) != len(wantProviders) || gotProviders[0] != wantProviders[0] || gotProviders[1] != wantProviders[1] {
			t.Fatalf("Server.EnabledPassthroughProviders = %v, want %v", gotProviders, wantProviders)
		}
	})
}

func TestLoad_EnabledPassthroughProviders_EnvOverridesYAML(t *testing.T) {
	withTempDir(t, func(dir string) {
		clearAllConfigEnvVars(t)

		yaml := `
server:
  enabled_passthrough_providers:
    - openai
    - anthropic
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		t.Setenv("ENABLED_PASSTHROUGH_PROVIDERS", " groq , gemini ")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		got := result.Config.Server.EnabledPassthroughProviders
		want := []string{"groq", "gemini"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("EnabledPassthroughProviders = %v, want %v", got, want)
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

func TestLoad_ExecutionPlanRefreshInterval(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if result.Config.ExecutionPlans.RefreshInterval != time.Minute {
			t.Fatalf("ExecutionPlans.RefreshInterval = %s, want %s", result.Config.ExecutionPlans.RefreshInterval, time.Minute)
		}
	})

	withTempDir(t, func(dir string) {
		yaml := `
execution_plans:
  refresh_interval: 90s
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if result.Config.ExecutionPlans.RefreshInterval != 90*time.Second {
			t.Fatalf("ExecutionPlans.RefreshInterval = %s, want %s", result.Config.ExecutionPlans.RefreshInterval, 90*time.Second)
		}
	})

	withTempDir(t, func(_ string) {
		t.Setenv("EXECUTION_PLAN_REFRESH_INTERVAL", "45s")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if result.Config.ExecutionPlans.RefreshInterval != 45*time.Second {
			t.Fatalf("ExecutionPlans.RefreshInterval = %s, want %s", result.Config.ExecutionPlans.RefreshInterval, 45*time.Second)
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

	withTempDir(t, func(dir string) {
		cfgDir := filepath.Join(dir, "config")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		yamlContent := "cache:\n  response:\n    simple: {}\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(yamlContent), 0o644); err != nil {
			t.Fatal(err)
		}

		t.Setenv("REDIS_URL", "redis://env-host:6379")
		t.Setenv("REDIS_KEY_RESPONSES", "env:responses")
		t.Setenv("REDIS_TTL_RESPONSES", "1800")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		cfg := result.Config

		if cfg.Cache.Response.Simple == nil || cfg.Cache.Response.Simple.Redis == nil {
			t.Fatal("expected Cache.Response.Simple.Redis from env vars with simple: {} in config.yaml")
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

func TestLoad_RedisURLDoesNotAllocateResponseSimpleWithoutYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		t.Setenv("REDIS_URL", "redis://env-host:6379")
		t.Setenv("REDIS_KEY_RESPONSES", "env:responses")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if result.Config.Cache.Response.Simple != nil {
			t.Fatalf("expected no response simple cache without cache.response.simple in YAML and without RESPONSE_CACHE_SIMPLE_ENABLED")
		}
	})
}

func TestLoad_ResponseSimpleOptInViaEnvWithoutYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		t.Setenv("RESPONSE_CACHE_SIMPLE_ENABLED", "true")
		t.Setenv("REDIS_URL", "redis://env-host:6379")
		t.Setenv("REDIS_KEY_RESPONSES", "env:responses")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		cfg := result.Config
		if cfg.Cache.Response.Simple == nil || cfg.Cache.Response.Simple.Redis == nil {
			t.Fatal("expected simple + redis from env opt-in")
		}
		if cfg.Cache.Response.Simple.Redis.URL != "redis://env-host:6379" {
			t.Errorf("redis URL: got %q", cfg.Cache.Response.Simple.Redis.URL)
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
