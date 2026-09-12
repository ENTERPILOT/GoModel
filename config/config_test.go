package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"time"

	"gopkg.in/yaml.v3"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearProviderEnvVars unsets all known provider-related environment variables.
func clearProviderEnvVars(t *testing.T) {
	t.Helper()
	keys := []string{
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODELS",
		"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODELS",
		"COHERE_API_KEY", "COHERE_BASE_URL", "COHERE_MODELS",
		"GEMINI_API_KEY", "GEMINI_BASE_URL", "GEMINI_MODELS",
		"DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL", "DEEPSEEK_MODELS",
		"XAI_API_KEY", "XAI_BASE_URL", "XAI_MODELS",
		"GROQ_API_KEY", "GROQ_BASE_URL", "GROQ_MODELS",
		"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "OPENROUTER_MODELS", "OPENROUTER_SITE_URL", "OPENROUTER_APP_NAME",
		"KILO_API_KEY", "KILO_BASE_URL", "KILO_MODELS",
		"ZAI_API_KEY", "ZAI_BASE_URL", "ZAI_MODELS",
		"AZURE_API_KEY", "AZURE_BASE_URL", "AZURE_API_VERSION", "AZURE_MODELS",
		"ORACLE_API_KEY", "ORACLE_BASE_URL", "ORACLE_MODELS",
		"VLLM_API_KEY", "VLLM_BASE_URL", "VLLM_MODELS",
		"LLMD_API_KEY", "LLMD_BASE_URL", "LLMD_MODELS", "LLMD_INFERENCE_OBJECTIVE", "LLMD_FAIRNESS_FROM_USER_PATH",
		"SGLANG_API_KEY", "SGLANG_BASE_URL", "SGLANG_MODELS",
		"OLLAMA_API_KEY", "OLLAMA_BASE_URL", "OLLAMA_MODELS",
	}
	// Model filters are accepted for every provider prefix, so clear them for
	// each prefix above rather than tracking them provider by provider.
	prefixes := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if prefix, _, ok := strings.Cut(key, "_"); ok {
			prefixes[prefix] = struct{}{}
		}
	}
	for prefix := range prefixes {
		keys = append(keys,
			prefix+"_MODEL_FILTER_INCLUDE",
			prefix+"_MODEL_FILTER_EXCLUDE",
			prefix+"_MODEL_FILTER_MAX_PRICE_PER_MTOK",
		)
	}
	for _, key := range keys {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

// clearAllConfigEnvVars unsets all config-related environment variables.
func clearAllConfigEnvVars(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CONFIG_STRICT",
		"PORT", "BASE_PATH", "GOMODEL_MASTER_KEY", "BODY_SIZE_LIMIT", "STREAM_STALL_TIMEOUT", "SWAGGER_ENABLED", "PPROF_ENABLED", "ENABLE_PASSTHROUGH_ROUTES", "ALLOW_PASSTHROUGH_V1_ALIAS", "USER_PATH_HEADER", "ENABLED_PASSTHROUGH_PROVIDERS",
		"GOMODEL_CACHE_DIR", "CACHE_REFRESH_INTERVAL", "MODEL_LIST_URL", "GOMODEL_OFFLINE", "GOMODEL_VERSION_CHECK_ENABLED",
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
		"LOGGING_ENABLED", "LOGGING_LOG_BODIES", "LOGGING_LOG_REVISION_BODIES", "LOGGING_LOG_GUARDRAIL_STEPS", "LOGGING_LOG_HEADERS",
		"LOGGING_LOG_AUDIO_BODIES", "LOGGING_LOG_IMAGE_BODIES", "LOGGING_LOG_IMAGE_BODIES_SCOPE",
		"LOGGING_ONLY_MODEL_INTERACTIONS", "LOGGING_BUFFER_SIZE",
		"LOGGING_FLUSH_INTERVAL", "LOGGING_RETENTION_DAYS",
		"USAGE_ENABLED", "ENFORCE_RETURNING_USAGE_DATA",
		"USAGE_PRICING_RECALCULATION_ENABLED",
		"USAGE_BUFFER_SIZE", "USAGE_FLUSH_INTERVAL", "USAGE_RETENTION_DAYS",
		"BUDGETS_ENABLED",
		"RATE_LIMITS_ENABLED", "RATE_LIMITS_FLUSH_INTERVAL",
		"DASHBOARD_LIVE_LOGS_ENABLED", "DASHBOARD_LIVE_LOGS_BUFFER_SIZE",
		"DASHBOARD_LIVE_LOGS_REPLAY_LIMIT", "DASHBOARD_LIVE_LOGS_HEARTBEAT_SECONDS",
		"GUARDRAILS_ENABLED", "ENABLE_GUARDRAILS_FOR_BATCH_PROCESSING", "PLUGINS_ENABLED",
		"FAILOVER_MODE", "FAILOVER_MANUAL_RULES_PATH", "FAILOVER_ENABLED", "FAILOVER_RULES_JSON", "FAILOVER_DISABLED_MODELS", "FAILOVER_DISABLED_MODELS_JSON",
		"MODELS_ENABLED_BY_DEFAULT", "KEEP_ONLY_ALIASES_AT_MODELS_ENDPOINT", "UNQUALIFIED_MODEL_IDS_AT_MODELS_ENDPOINT", "CONFIGURED_PROVIDER_MODELS_MODE",
		"HTTP_TIMEOUT", "HTTP_RESPONSE_HEADER_TIMEOUT",
		"WORKFLOW_REFRESH_INTERVAL",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "SET_BUDGET_") || strings.HasPrefix(key, "SET_RATE_LIMIT_") || strings.HasPrefix(key, "SET_PROVIDER_RATE_LIMIT_") || strings.HasPrefix(key, "TAGGING_HEADER_") {
			t.Setenv(key, "")
			os.Unsetenv(key)
		}
	}
	clearProviderEnvVars(t)
}

// withTempDir runs fn in a temporary directory, restoring the original working directory afterward.
func withTempDir(t *testing.T, fn func(dir string)) {
	t.Helper()
	tempDir := t.TempDir()
	originalDir, err := os.Getwd()
	require.NoError(t, err)
	err = os.Chdir(tempDir)
	require.NoError(t, err)

	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	fn(tempDir)
}

func TestBuildDefaultConfig(t *testing.T) {
	cfg := buildDefaultConfig()

	assert.Equal(t, "8080", cfg.Server.Port)
	assert.Equal(t, "/", cfg.Server.BasePath)
	assert.Equal(t, "X-GoModel-User-Path", cfg.Server.UserPathHeader)
	assert.False(t, cfg.Server.PprofEnabled)
	assert.False(t, cfg.Server.SwaggerEnabled)
	assert.Equal(t, DefaultStreamStallTimeoutSeconds, cfg.Server.StreamStallTimeout)
	assert.True(t, cfg.Server.EnablePassthroughRoutes)
	assert.True(t, cfg.Server.AllowPassthroughV1Alias)
	assert.Equal(t, []string{"openai", "anthropic", "openrouter", "kilo", "zai", "sglang", "vllm", "llamacpp", "llmd", "deepseek"}, cfg.Server.EnabledPassthroughProviders)
	assert.Equal(t, ConfiguredProviderModelsModeFallback, cfg.Models.ConfiguredProviderModelsMode)
	assert.Nil(t, cfg.Cache.Model.Local)
	assert.Equal(t, 3600, cfg.Cache.Model.RefreshInterval)
	assert.Equal(t, "sqlite", cfg.Storage.Type)
	assert.Equal(t, storage.DefaultSQLitePath(), cfg.Storage.SQLite.Path)
	assert.Equal(t, 10, cfg.Storage.PostgreSQL.MaxConns)
	assert.Empty(t, cfg.Storage.MongoDB.Database)
	assert.True(t, cfg.Logging.LogBodies)
	assert.True(t, cfg.Logging.LogHeaders)
	assert.Equal(t, 1000, cfg.Logging.BufferSize)
	assert.Equal(t, 5, cfg.Logging.FlushInterval)
	assert.Equal(t, 30, cfg.Logging.RetentionDays)
	assert.True(t, cfg.Logging.OnlyModelInteractions)
	assert.True(t, cfg.Logging.Enabled)
	assert.True(t, cfg.Usage.Enabled)
	assert.True(t, cfg.Usage.EnforceReturningUsageData)
	assert.True(t, cfg.Usage.PricingRecalculationEnabled)
	assert.Equal(t, 1000, cfg.Usage.BufferSize)
	assert.Equal(t, 5, cfg.Usage.FlushInterval)
	assert.Equal(t, 90, cfg.Usage.RetentionDays)
	assert.True(t, cfg.Budgets.Enabled)
	assert.Equal(t, "/metrics", cfg.Metrics.Endpoint)
	assert.False(t, cfg.Metrics.Enabled)
	assert.Equal(t, 600, cfg.HTTP.Timeout)
	assert.Equal(t, 600, cfg.HTTP.ResponseHeaderTimeout)
	assert.Equal(t, time.Minute, cfg.Workflows.RefreshInterval)
	assert.True(t, cfg.Admin.EndpointsEnabled)
	assert.True(t, cfg.Admin.UIEnabled)
	assert.True(t, cfg.Admin.LiveLogsEnabled)
	assert.Equal(t, 10000, cfg.Admin.LiveLogsBufferSize)
	assert.Equal(t, 1000, cfg.Admin.LiveLogsReplayLimit)
	assert.Equal(t, 15, cfg.Admin.LiveLogsHeartbeatSeconds)
	assert.True(t, cfg.Models.EnabledByDefault)
	assert.False(t, cfg.Models.KeepOnlyAliasesAtModelsEndpoint)
	assert.False(t, cfg.Models.UnqualifiedModelIDsAtModelsEndpoint)
	assert.False(t, cfg.Guardrails.EnableForBatchProcessing)
	assert.Equal(t, FailoverModeManual, cfg.Failover.DefaultMode)
	assert.True(t, cfg.Failover.Enabled)
	assert.Nil(t, cfg.Cache.Response.Simple)
	assert.Nil(t, cfg.Cache.Response.Semantic)

	expectedRetry := DefaultRetryConfig()
	assert.Equal(t, expectedRetry, cfg.Resilience.Retry)

	expectedCB := DefaultCircuitBreakerConfig()
	assert.Equal(t, expectedCB, cfg.Resilience.CircuitBreaker)
}

func TestDecodeExtensionStrictlyDecodesOpaqueConfig(t *testing.T) {
	var node yaml.Node
	err := yaml.Unmarshal([]byte("enabled: true\npkce_enabled: false\n"), &node)
	require.NoError(t, err)

	type ssoConfig struct {
		Enabled     bool `yaml:"enabled"`
		PKCEEnabled bool `yaml:"pkce_enabled"`
	}
	result := &LoadResult{Config: &Config{Extensions: map[string]yaml.Node{"sso": node}}}
	var got ssoConfig
	found, err := result.DecodeExtension("sso", &got)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, got.Enabled)
	require.False(t, got.PKCEEnabled)

	var unknown yaml.Node
	err = yaml.Unmarshal([]byte("unknown: true\n"), &unknown)
	require.NoError(t, err)

	result.Config.Extensions["sso"] = unknown
	_, err = result.DecodeExtension("sso", &got)
	require.Error(t, err)
}

func TestDecodeExtensionHandlesAbsentConfiguration(t *testing.T) {
	var nilResult *LoadResult
	found, err := nilResult.DecodeExtension("sso", &struct{}{})
	require.NoError(t, err)
	require.False(t, found)

	result := &LoadResult{Config: &Config{}}
	found, err = result.DecodeExtension("sso", &struct{}{})
	require.NoError(t, err)
	require.False(t, found)
	found, err = result.DecodeExtension("sso", nil)
	require.NoError(t, err)
	require.False(t, found)
}

func TestLoadPreservesOpaqueExtensionConfiguration(t *testing.T) {
	clearAllConfigEnvVars(t)
	withTempDir(t, func(dir string) {
		contents := []byte("extensions:\n  sso:\n    enabled: true\n    provider_specific_option: value\n")
		err := os.WriteFile(filepath.Join(dir, "config.yaml"), contents, 0o644)
		require.NoError(t, err)

		result, err := Load()
		require.NoError(t, err)

		var decoded struct {
			Enabled                bool   `yaml:"enabled"`
			ProviderSpecificOption string `yaml:"provider_specific_option"`
		}
		found, err := result.DecodeExtension("sso", &decoded)
		require.NoError(t, err)
		require.True(t, found)
		require.True(t, decoded.Enabled)
		require.Equal(t, "value", decoded.ProviderSpecificOption)
	})
}

func TestLoadBudgetEnvUserPath(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("SET_BUDGET_USER__PATH__EXAMPLE", "daily=12.5,weekly=50")

		result, err := Load()
		require.NoError(t, err)

		entries := result.Config.Budgets.UserPaths
		require.Len(t, entries, 1)
		require.Equal(t, "/user/path/example", entries[0].Path)
		require.Len(t, entries[0].Limits, 2)
		assert.Equal(t, int64(86400), entries[0].Limits[0].PeriodSeconds)
		assert.Equal(t, 12.5, entries[0].Limits[0].Amount)
		assert.Equal(t, int64(604800), entries[0].Limits[1].PeriodSeconds)
		assert.Equal(t, 50.0, entries[0].Limits[1].Amount)
	})
}

func TestBudgetEnvPathUsesDoubleUnderscoreSeparator(t *testing.T) {
	tests := []struct {
		name   string
		suffix string
		want   string
	}{
		{name: "root", suffix: "", want: "/"},
		{name: "double underscore separator", suffix: "TEAM__ALPHA", want: "/team/alpha"},
		{name: "single underscore preserved", suffix: "USER_123", want: "/user_123"},
		{name: "single underscores preserved per segment", suffix: "USER_123__PROJECT_A", want: "/user_123/project_a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := userPathEnvSuffixPath(tt.suffix)
			require.Equal(t, tt.want, got, "userPathEnvSuffixPath(%q)", tt.suffix)
		})
	}
}

func TestLoadBudgetEnvJSONLimitsAreSorted(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("SET_BUDGET_TEAM__ALPHA", `{"weekly":50,"daily":10,"monthly":100}`)

		result, err := Load()
		require.NoError(t, err)

		limits := result.Config.Budgets.UserPaths[0].Limits
		got := []string{limits[0].Period, limits[1].Period, limits[2].Period}
		want := []string{"daily", "monthly", "weekly"}
		require.Equal(t, want, got)
	})
}

func TestLoadBudgetEnvJSONArraySupportsPerChild(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("SET_BUDGET_USERS", `[{"period":"daily","amount":10,"per_child":true}]`)
		result, err := Load()
		require.NoError(t, err)

		entry := result.Config.Budgets.UserPaths[0]
		require.Equal(t, "/users", entry.Path)
		require.Len(t, entry.Limits, 1)
		require.True(t, entry.Limits[0].PerChild, "budget env entry = %+v, want per-child /users", entry)
	})
}

func TestLoadBudgetYAMLSupportsPerChild(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yamlConfig := `
budgets:
  user_paths:
    - path: /customers
      per_child: true
      limits:
        - period: daily
          amount: 10
`
		result := loadConfigYAML(t, dir, yamlConfig)

		entry := result.Config.Budgets.UserPaths[0]
		require.Equal(t, "/customers", entry.Path)
		require.True(t, entry.PerChild, "budget YAML entry = %+v, want per-child /customers", entry)
	})
}

func TestLoadBudgetEnvReplacesMatchingYAMLUserPath(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yamlConfig := `
budgets:
  user_paths:
    - path: /team/alpha
      limits:
        - period: daily
          amount: 1
    - path: /team/beta
      limits:
        - period: daily
          amount: 2
`
		writeConfigYAML(t, dir, yamlConfig)

		t.Setenv("SET_BUDGET_TEAM__ALPHA", "weekly=50")

		result, err := Load()
		require.NoError(t, err)

		entries := result.Config.Budgets.UserPaths
		require.Len(t, entries, 2)
		require.Equal(t, "/team/beta", entries[0].Path)
		require.Equal(t, float64(2), entries[0].Limits[0].Amount, "first budget entry = %+v, want untouched /team/beta YAML entry", entries[0])
		require.Equal(t, "/team/alpha", entries[1].Path)
		require.Len(t, entries[1].Limits, 1)
		require.Equal(t, int64(604800), entries[1].Limits[0].PeriodSeconds)
		require.Equal(t, float64(50), entries[1].Limits[0].Amount)
	})
}

func TestLoadBudgetEnvReplacesNonCanonicalYAMLUserPath(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		// YAML uses non-canonical forms (no leading slash, trailing slash) that
		// only match the env entry after core.NormalizeUserPath canonicalizes them.
		yamlConfig := `
budgets:
  user_paths:
    - path: team/alpha
      limits:
        - period: daily
          amount: 1
    - path: /team/beta/
      limits:
        - period: daily
          amount: 2
`
		writeConfigYAML(t, dir, yamlConfig)

		t.Setenv("SET_BUDGET_TEAM__ALPHA", "weekly=50")

		result, err := Load()
		require.NoError(t, err)

		entries := result.Config.Budgets.UserPaths
		require.Len(t, entries, 2)

		// /team/beta/ stays (different canonical from env), /team/alpha is replaced.
		require.Equal(t, "/team/beta", entries[0].Path)
		require.Equal(t, float64(2), entries[0].Limits[0].Amount, "first budget entry = %+v, want /team/beta YAML entry (normalized)", entries[0])
		require.Equal(t, "/team/alpha", entries[1].Path)
		require.Len(t, entries[1].Limits, 1)
		require.Equal(t, float64(50), entries[1].Limits[0].Amount)
	})
}

func TestLoadBudgetEnvRejectsNonFiniteAmount(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("SET_BUDGET_TEAM__ALPHA", "daily=NaN")

		_, err := Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "amount must be a finite number greater than 0")
	})
}

func TestLoadBudgetConfigRejectsDuplicateLogicalBudgets(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yamlConfig := `
budgets:
  user_paths:
    - path: team/alpha
      limits:
        - period: daily
          amount: 1
    - path: /team/alpha
      limits:
        - period_seconds: 86400
          amount: 2
`
		writeConfigYAML(t, dir, yamlConfig)

		_, err := Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "duplicate budget for user_path /team/alpha period 86400")
	})
}

func TestLoadBudgetEnvDisablesBudgetsWhenUsageTrackingDisabled(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("USAGE_ENABLED", "false")
		t.Setenv("SET_BUDGET_USER__PATH__EXAMPLE", "daily=12.5")

		result, err := Load()
		require.NoError(t, err)
		require.False(t, result.Config.Budgets.Enabled)
		require.Empty(t, result.Config.Budgets.UserPaths)
	})
}

func TestLoadBudgetsEnabledDisablesBudgetsWhenUsageTrackingDisabledWithoutSeedBudgets(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("USAGE_ENABLED", "false")

		result, err := Load()
		require.NoError(t, err)
		require.False(t, result.Config.Budgets.Enabled)
	})
}

func TestLoadUsagePricingRecalculationFromYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
usage:
  pricing_recalculation_enabled: false
`
		result := loadConfigYAML(t, dir, yaml)
		require.False(t, result.Config.Usage.PricingRecalculationEnabled)
	})
}

func TestLoadDisabledBudgetsIgnoreMalformedBudgetEnv(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("BUDGETS_ENABLED", "false")
		t.Setenv("SET_BUDGET_USER__PATH__EXAMPLE", "not-a-budget-limit")

		result, err := Load()
		require.NoError(t, err)
		require.False(t, result.Config.Budgets.Enabled)
		require.Empty(t, result.Config.Budgets.UserPaths)
	})
}

func TestLoad_ZeroConfig(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "8080", result.Config.Server.Port)
		assert.Empty(t, result.RawProviders)
	})
}

func TestLoad_YAMLOverridesDefaults(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
server:
  port: "3000"
  pprof_enabled: true
models:
  enabled_by_default: false
  keep_only_aliases_at_models_endpoint: true
  unqualified_model_ids_at_models_endpoint: true
  configured_provider_models_mode: allowlist
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
		result := loadConfigYAML(t, dir, yaml)

		cfg := result.Config

		assert.Equal(t, "3000", cfg.Server.Port)
		assert.True(t, cfg.Server.PprofEnabled)
		assert.False(t, cfg.Models.EnabledByDefault)
		assert.True(t, cfg.Models.KeepOnlyAliasesAtModelsEndpoint)
		assert.True(t, cfg.Models.UnqualifiedModelIDsAtModelsEndpoint)
		assert.Equal(t, ConfiguredProviderModelsModeAllowlist, cfg.Models.ConfiguredProviderModelsMode)
		require.NotNil(t, cfg.Cache.Model.Redis)
		assert.Equal(t, "redis://myhost:6379", cfg.Cache.Model.Redis.URL)
		assert.Equal(t, "custom:key", cfg.Cache.Model.Redis.Key)
		assert.Equal(t, 3600, cfg.Cache.Model.Redis.TTL)
		assert.Nil(t, cfg.Cache.Model.Local)
		assert.True(t, cfg.Logging.Enabled)
		assert.False(t, cfg.Logging.LogBodies)
		assert.Equal(t, 500, cfg.Logging.BufferSize)
		assert.Equal(t, 5, cfg.Logging.FlushInterval)
		assert.Equal(t, "sqlite", cfg.Storage.Type)
	})
}

func TestLoad_FailoverManualRules(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		manualRulesPath := filepath.Join(dir, "failover.json")
		err := os.WriteFile(manualRulesPath, []byte(`{
			"gpt-4o": ["azure/gpt-4o", "gemini/gemini-2.5-pro"],
			"claude-sonnet-4": ["openai/gpt-5-mini"]
		}`), 0644)
		require.NoError(t, err)

		type yamlConfig struct {
			Failover struct {
				DefaultMode     string                       `yaml:"default_mode"`
				ManualRulesPath string                       `yaml:"manual_rules_path"`
				Overrides       map[string]map[string]string `yaml:"overrides"`
			} `yaml:"failover"`
		}

		yamlCfg := yamlConfig{}
		yamlCfg.Failover.DefaultMode = "auto"
		yamlCfg.Failover.ManualRulesPath = manualRulesPath
		// A legacy failover.overrides block (removed feature) must still load
		// without error and must no longer affect behavior — even mode: off no
		// longer disables failover. Operators migrate to disabled_models.
		yamlCfg.Failover.Overrides = map[string]map[string]string{
			"gpt-4o": {"mode": "off"},
		}

		yamlData, err := yaml.Marshal(yamlCfg)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(dir, "config.yaml"), yamlData, 0644)
		require.NoError(t, err)

		result, err := Load()
		require.NoError(t, err)

		cfg := result.Config
		require.Equal(t, FailoverModeAuto, cfg.Failover.DefaultMode)
		require.False(t, cfg.Failover.Disabled["gpt-4o"], "legacy failover.overrides mode:off must no longer disable failover")
		require.Equal(t, []string{"azure/gpt-4o", "gemini/gemini-2.5-pro"}, cfg.Failover.Manual["gpt-4o"])
	})
}

func TestLoad_DeprecatedFailoverDefaultModeIsAccepted(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
failover:
  default_mode: invalid
`
		result := loadConfigYAML(t, dir, yaml)
		require.Equal(t, FailoverMode("invalid"), result.Config.Failover.DefaultMode)
	})
}

func TestLoad_MergeConfiguredProviderModelsMode(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
models:
  configured_provider_models_mode: merge
`
		result := loadConfigYAML(t, dir, yaml)
		require.Equal(t, ConfiguredProviderModelsModeMerge, result.Config.Models.ConfiguredProviderModelsMode)
	})
}

func TestLoad_InvalidConfiguredProviderModelsMode(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
models:
  configured_provider_models_mode: strict
`
		writeConfigYAML(t, dir, yaml)

		_, err := Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "models.configured_provider_models_mode must be one of")
	})
}

func TestLoad_EmptyFailoverOverrideModeIsAccepted(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
failover:
  overrides:
    "gpt-4o": {}
`
		writeConfigYAML(t, dir, yaml)
		_, err := Load()
		require.NoError(t, err)
	})
}

func TestLoad_ManualFailoverModeAllowsMissingManualRulesPath(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
failover:
  default_mode: manual
`
		result := loadConfigYAML(t, dir, yaml)
		require.Equal(t, FailoverModeManual, result.Config.Failover.DefaultMode)
		require.Nil(t, result.Config.Failover.Manual)
	})
}

func TestLoad_LegacyFailoverOverridesAreIgnored(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		// The removed failover.overrides block must still load without error
		// (yaml ignores the unknown key) and must have no effect: even mode: off
		// no longer disables failover. Operators migrate to disabled_models.
		yamlData := `
failover:
  overrides:
    "gpt-4o":
      mode: "off"
`
		result := loadConfigYAML(t, dir, yamlData)
		require.False(t, result.Config.Failover.Disabled["gpt-4o"], "legacy failover.overrides mode:off must no longer disable failover")
		require.Nil(t, result.Config.Failover.Manual)
	})
}

func TestLoad_FailoverManualRulesDuplicateKeyAfterTrim(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		manualRulesPath := filepath.Join(dir, "failover.json")
		err := os.WriteFile(manualRulesPath, []byte(`{
			"gpt-4o": ["azure/gpt-4o"],
			" gpt-4o ": ["gemini/gemini-2.5-pro"]
		}`), 0644)
		require.NoError(t, err)

		type yamlConfig struct {
			Failover struct {
				ManualRulesPath string `yaml:"manual_rules_path"`
			} `yaml:"failover"`
		}

		yamlCfg := yamlConfig{}
		yamlCfg.Failover.ManualRulesPath = manualRulesPath
		yamlData, err := yaml.Marshal(yamlCfg)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(dir, "config.yaml"), yamlData, 0644)
		require.NoError(t, err)

		_, err = Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), `failover.manual_rules_path: duplicate manual rule key after trimming: "gpt-4o"`)
	})
}

func TestLoad_FailoverManualRulesRejectsDuplicateRawJSONKeys(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		manualRulesPath := filepath.Join(dir, "failover.json")
		err := os.WriteFile(manualRulesPath, []byte(`{
			"gpt-4o": ["azure/gpt-4o"],
			"gpt-4o": ["gemini/gemini-2.5-pro"]
		}`), 0644)
		require.NoError(t, err)

		type yamlConfig struct {
			Failover struct {
				ManualRulesPath string `yaml:"manual_rules_path"`
			} `yaml:"failover"`
		}

		yamlCfg := yamlConfig{}
		yamlCfg.Failover.ManualRulesPath = manualRulesPath
		yamlData, err := yaml.Marshal(yamlCfg)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(dir, "config.yaml"), yamlData, 0644)
		require.NoError(t, err)

		_, err = Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), `duplicate JSON key "gpt-4o"`)
	})
}

func TestLoad_FailoverManualRulesRejectsNullValues(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		manualRulesPath := filepath.Join(dir, "failover.json")
		err := os.WriteFile(manualRulesPath, []byte(`{
			"gpt-4o": null
		}`), 0644)
		require.NoError(t, err)

		type yamlConfig struct {
			Failover struct {
				ManualRulesPath string `yaml:"manual_rules_path"`
			} `yaml:"failover"`
		}

		yamlCfg := yamlConfig{}
		yamlCfg.Failover.ManualRulesPath = manualRulesPath
		yamlData, err := yaml.Marshal(yamlCfg)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(dir, "config.yaml"), yamlData, 0644)
		require.NoError(t, err)

		_, err = Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), `null not allowed for "gpt-4o"`)
	})
}

func TestLoad_FeatureFailoverModeEnvOverridesFailoverDefaultMode(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("FAILOVER_MODE", "auto")

	withTempDir(t, func(_ string) {
		result, err := Load()
		require.NoError(t, err)
		require.Equal(t, FailoverModeAuto, result.Config.Failover.DefaultMode)
	})
}

func TestLoad_FailoverRulesJSONEnvOnly(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("FAILOVER_RULES_JSON", `{"gpt-4o":["azure/gpt-4o","gemini/gemini-2.5-pro"]}`)
	t.Setenv("FAILOVER_DISABLED_MODELS_JSON", `["claude-sonnet-4"]`)

	withTempDir(t, func(_ string) {
		result, err := Load()
		require.NoError(t, err)

		got := result.Config.Failover.Manual["gpt-4o"]
		want := []string{"azure/gpt-4o", "gemini/gemini-2.5-pro"}
		require.Equal(t, want, got)
		require.True(t, result.Config.Failover.Disabled["claude-sonnet-4"])
	})
}

func TestLoad_BlankFailoverDefaultModeResolvesToManual(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
failover:
  default_mode: ""
`
		result := loadConfigYAML(t, dir, yaml)
		require.Equal(t, FailoverModeManual, result.Config.Failover.DefaultMode)
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
				writeConfigYAML(t, dir, yaml)

				t.Setenv("ENABLE_PASSTHROUGH_ROUTES", tt.envEnabled)
				t.Setenv("ALLOW_PASSTHROUGH_V1_ALIAS", tt.envNormalize)

				result, err := Load()
				require.NoError(t, err)
				require.Equal(t, tt.wantEnabled, result.Config.Server.EnablePassthroughRoutes)
				require.Equal(t, tt.wantNormalize, result.Config.Server.AllowPassthroughV1Alias)
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
		result := loadConfigYAML(t, dir, yaml)
		require.False(t, result.Config.Server.EnablePassthroughRoutes)
		require.False(t, result.Config.Server.AllowPassthroughV1Alias)
	})
}

func TestLoad_ConfigExample_UsesNestedModelCacheSettings(t *testing.T) {
	clearAllConfigEnvVars(t)

	examplePath, err := filepath.Abs("config.example.yaml")
	require.NoError(t, err)

	exampleData, err := os.ReadFile(examplePath)
	require.NoError(t, err)

	withTempDir(t, func(dir string) {
		err := os.MkdirAll(filepath.Join(dir, "config"), 0755)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(dir, "config", "config.yaml"), exampleData, 0644)
		require.NoError(t, err)

		result, err := Load()
		require.NoError(t, err)
		require.Equal(t, 3600, result.Config.Cache.Model.RefreshInterval)
		require.NotNil(t, result.Config.Cache.Model.Local)
		require.Equal(t, ".cache", result.Config.Cache.Model.Local.CacheDir)
		require.Nil(t, result.Config.Cache.Model.Redis)

		gotProviders := result.Config.Server.EnabledPassthroughProviders
		wantProviders := []string{"openai", "anthropic", "cohere", "openrouter", "kilo", "zai", "sglang", "vllm", "llmd", "deepseek", "bailian"}
		require.Equal(t, wantProviders, gotProviders)
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
		writeConfigYAML(t, dir, yaml)

		t.Setenv("ENABLED_PASSTHROUGH_PROVIDERS", " groq , gemini ")

		result, err := Load()
		require.NoError(t, err)

		require.Equal(t, []string{"groq", "gemini"}, result.Config.Server.EnabledPassthroughProviders)
	})
}

func TestLoad_UserPathHeaderConfig(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
server:
  user_path_header: "x-tenant-path"
`
		result := loadConfigYAML(t, dir, yaml)
		got := result.Config.Server.UserPathHeader
		require.Equal(t, "X-Tenant-Path", got)
	})

	withTempDir(t, func(dir string) {
		yaml := `
server:
  user_path_header: "X-Yaml-Path"
`
		writeConfigYAML(t, dir, yaml)

		t.Setenv("USER_PATH_HEADER", "x-env-path")

		result, err := Load()
		require.NoError(t, err)
		got := result.Config.Server.UserPathHeader
		require.Equal(t, "X-Env-Path", got)
	})
}

func TestLoad_UserPathHeaderRejectsInvalidName(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		t.Setenv("USER_PATH_HEADER", "Bad Header")

		_, err := Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid server.user_path_header")
	})
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
server:
  port: "3000"
  base_path: "internal/"
cache:
  model:
    local: null
    redis:
      url: "redis://myhost:6379"
logging:
  enabled: true
`
		writeConfigYAML(t, dir, yaml)

		t.Setenv("PORT", "9090")
		t.Setenv("BASE_PATH", "g/")
		t.Setenv("CACHE_REFRESH_INTERVAL", "1800")
		t.Setenv("LOGGING_ENABLED", "false")

		result, err := Load()
		require.NoError(t, err)

		cfg := result.Config

		assert.Equal(t, "9090", cfg.Server.Port)
		assert.Equal(t, "/g", cfg.Server.BasePath)
		assert.Equal(t, 1800, cfg.Cache.Model.RefreshInterval)
		assert.False(t, cfg.Logging.Enabled)
	})
}

func TestLoad_EnvOverridesDefaults(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		t.Setenv("PORT", "5555")
		t.Setenv("MODELS_ENABLED_BY_DEFAULT", "false")
		t.Setenv("KEEP_ONLY_ALIASES_AT_MODELS_ENDPOINT", "true")
		t.Setenv("UNQUALIFIED_MODEL_IDS_AT_MODELS_ENDPOINT", "true")
		t.Setenv("CONFIGURED_PROVIDER_MODELS_MODE", "allowlist")
		t.Setenv("USAGE_PRICING_RECALCULATION_ENABLED", "false")
		t.Setenv("STORAGE_TYPE", "postgresql")
		t.Setenv("POSTGRES_URL", "postgres://localhost/test")
		t.Setenv("POSTGRES_MAX_CONNS", "20")

		result, err := Load()
		require.NoError(t, err)

		cfg := result.Config

		assert.Equal(t, "5555", cfg.Server.Port)
		assert.False(t, cfg.Models.EnabledByDefault)
		assert.True(t, cfg.Models.KeepOnlyAliasesAtModelsEndpoint)
		assert.True(t, cfg.Models.UnqualifiedModelIDsAtModelsEndpoint)
		assert.Equal(t, ConfiguredProviderModelsModeAllowlist, cfg.Models.ConfiguredProviderModelsMode)
		assert.False(t, cfg.Usage.PricingRecalculationEnabled)
		assert.Equal(t, "postgresql", cfg.Storage.Type)
		assert.Equal(t, "postgres://localhost/test", cfg.Storage.PostgreSQL.URL)
		assert.Equal(t, 20, cfg.Storage.PostgreSQL.MaxConns)
	})
}

func TestLoad_ModelListURLEnv(t *testing.T) {
	const defaultURL = "https://raw.githubusercontent.com/ENTERPILOT/ai-model-list/refs/heads/main/models.min.json"

	tests := []struct {
		name  string
		set   bool
		value string
		want  string
	}{
		{name: "UnsetKeepsDefault", set: false, want: defaultURL},
		{name: "MirrorOverridesDefault", set: true, value: "https://mirror.internal/models.min.json", want: "https://mirror.internal/models.min.json"},
		{name: "EmptyIsSkippedLikeAnyEnvVar", set: true, value: "", want: defaultURL},
		{name: "OffDisablesDownloads", set: true, value: "off", want: ""},
		{name: "OffIsCaseInsensitive", set: true, value: "OFF", want: ""},
		{name: "OffTrimsWhitespace", set: true, value: " off ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			withTempDir(t, func(_ string) {
				if tt.set {
					t.Setenv("MODEL_LIST_URL", tt.value)
				}
				result, err := Load()
				require.NoError(t, err)
				got := result.Config.Cache.Model.ModelList.URL
				assert.Equal(t, tt.want, got)
			})
		})
	}

	t.Run("EnvOffWinsOverConfigYAML", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		withTempDir(t, func(dir string) {
			yaml := "cache:\n  model:\n    model_list:\n      url: \"https://mirror.internal/models.min.json\"\n"
			err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644)
			require.NoError(t, err)

			t.Setenv("MODEL_LIST_URL", "off")
			result, err := Load()
			require.NoError(t, err)
			got := result.Config.Cache.Model.ModelList.URL
			assert.Empty(t, got)
		})
	})

	t.Run("ConfigYAMLOffDisablesDownloads", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		withTempDir(t, func(dir string) {
			yaml := "cache:\n  model:\n    model_list:\n      url: \"off\"\n"
			err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644)
			require.NoError(t, err)

			result, err := Load()
			require.NoError(t, err)
			got := result.Config.Cache.Model.ModelList.URL
			assert.Empty(t, got)
		})
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
		result := loadConfigYAML(t, dir, yaml)

		provider, exists := result.RawProviders["openai"]
		require.True(t, exists)
		assert.Equal(t, "sk-yaml-key", provider.APIKey)
		assert.Equal(t, "https://custom.openai.com", provider.BaseURL)
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
		result := loadConfigYAML(t, dir, yamlContent)
		assert.Equal(t, 5, result.Config.Resilience.Retry.MaxRetries)

		openai, exists := result.RawProviders["openai"]
		require.True(t, exists)
		require.NotNil(t, openai.Resilience)
		require.NotNil(t, openai.Resilience.Retry)
		assert.Equal(t, 10, *openai.Resilience.Retry.MaxRetries)

		_, exists = result.RawProviders["anthropic"]
		require.True(t, exists)
	})
}

func TestLoad_HTTPConfig(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		require.NoError(t, err)
		assert.Equal(t, 600, result.Config.HTTP.Timeout)
		assert.Equal(t, 600, result.Config.HTTP.ResponseHeaderTimeout)
	})

	withTempDir(t, func(_ string) {
		t.Setenv("HTTP_TIMEOUT", "30")
		t.Setenv("HTTP_RESPONSE_HEADER_TIMEOUT", "60")

		result, err := Load()
		require.NoError(t, err)
		assert.Equal(t, 30, result.Config.HTTP.Timeout)
		assert.Equal(t, 60, result.Config.HTTP.ResponseHeaderTimeout)
	})
}

func TestLoad_WorkflowRefreshInterval(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		require.NoError(t, err)
		require.Equal(t, time.Minute, result.Config.Workflows.RefreshInterval)
	})

	withTempDir(t, func(dir string) {
		yaml := `
workflows:
  refresh_interval: 90s
`
		result := loadConfigYAML(t, dir, yaml)
		require.Equal(t, 90*time.Second, result.Config.Workflows.RefreshInterval)
	})

	withTempDir(t, func(_ string) {
		t.Setenv("WORKFLOW_REFRESH_INTERVAL", "45s")

		result, err := Load()
		require.NoError(t, err)
		require.Equal(t, 45*time.Second, result.Config.Workflows.RefreshInterval)
	})
}

func TestLoad_CacheDir(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		require.NoError(t, err)
		assert.NotNil(t, result.Config.Cache.Model.Local)
	})

	withTempDir(t, func(_ string) {
		t.Setenv("GOMODEL_CACHE_DIR", "/tmp/gomodel-cache")

		result, err := Load()
		require.NoError(t, err)
		require.NotNil(t, result.Config.Cache.Model.Local)
		assert.Equal(t, "/tmp/gomodel-cache", result.Config.Cache.Model.Local.CacheDir)
	})
}

func TestLoad_LoggingOnlyModelInteractionsDefault(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		require.NoError(t, err)
		assert.True(t, result.Config.Logging.OnlyModelInteractions)
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
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result.Config.Logging.OnlyModelInteractions, "env value %q", tt.envValue)
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
		result := loadConfigYAML(t, dir, yaml)
		assert.Equal(t, "9999", result.Config.Server.Port)

		provider, exists := result.RawProviders["openai"]
		require.True(t, exists)
		assert.Equal(t, "default-key", provider.APIKey)
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
		writeConfigYAML(t, dir, yaml)

		t.Setenv("TEST_PORT_CFG", "1111")
		t.Setenv("TEST_KEY_CFG", "real-key")

		result, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "1111", result.Config.Server.Port)

		provider, exists := result.RawProviders["openai"]
		require.True(t, exists)
		assert.Equal(t, "real-key", provider.APIKey)
	})
}

func TestLoad_YAMLInConfigSubdir(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		configDir := filepath.Join(dir, "config")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		yaml := `
server:
  port: "4444"
`
		err = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yaml), 0644)
		require.NoError(t, err)

		result, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "4444", result.Config.Server.Port)
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
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoad_LocalYAMLAndRedisURLAreBothKept(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		cfgDir := filepath.Join(dir, "config")
		err := os.MkdirAll(cfgDir, 0o755)
		require.NoError(t, err)

		yamlContent := "cache:\n  model:\n    local:\n      cache_dir: \".cache\"\n"
		err = os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(yamlContent), 0o644)
		require.NoError(t, err)

		t.Setenv("REDIS_URL", "redis://env-host:6379")

		result, err := Load()
		require.NoError(t, err)

		cfg := result.Config
		require.NotNil(t, cfg.Cache.Model.Local)
		require.Equal(t, ".cache", cfg.Cache.Model.Local.CacheDir)
		require.NotNil(t, cfg.Cache.Model.Redis)
		assert.Equal(t, "redis://env-host:6379", cfg.Cache.Model.Redis.URL)
	})
}

func TestLoad_EnvOnlyRedisModelCache(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		t.Setenv("REDIS_URL", "redis://env-host:6379")
		t.Setenv("REDIS_KEY_MODELS", "env:models")
		t.Setenv("REDIS_TTL_MODELS", "7200")

		result, err := Load()
		require.NoError(t, err)

		cfg := result.Config

		require.NotNil(t, cfg.Cache.Model.Redis)
		assert.Equal(t, "redis://env-host:6379", cfg.Cache.Model.Redis.URL)
		assert.Equal(t, "env:models", cfg.Cache.Model.Redis.Key)
		assert.Equal(t, 7200, cfg.Cache.Model.Redis.TTL)
		assert.Nil(t, cfg.Cache.Model.Local)
	})
}

func TestLoad_EnvOnlyRedisResponseCache(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		cfgDir := filepath.Join(dir, "config")
		err := os.MkdirAll(cfgDir, 0o755)
		require.NoError(t, err)

		yamlContent := "cache:\n  response:\n    simple: {}\n"
		err = os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(yamlContent), 0o644)
		require.NoError(t, err)

		t.Setenv("REDIS_URL", "redis://env-host:6379")
		t.Setenv("REDIS_KEY_RESPONSES", "env:responses")
		t.Setenv("REDIS_TTL_RESPONSES", "1800")

		result, err := Load()
		require.NoError(t, err)

		cfg := result.Config

		require.NotNil(t, cfg.Cache.Response.Simple)
		require.NotNil(t, cfg.Cache.Response.Simple.Redis)
		assert.Equal(t, "redis://env-host:6379", cfg.Cache.Response.Simple.Redis.URL)
		assert.Equal(t, "env:responses", cfg.Cache.Response.Simple.Redis.Key)
		assert.Equal(t, 1800, cfg.Cache.Response.Simple.Redis.TTL)
	})
}

func TestLoad_RedisURLDoesNotAllocateResponseSimpleWithoutYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		t.Setenv("REDIS_URL", "redis://env-host:6379")
		t.Setenv("REDIS_KEY_RESPONSES", "env:responses")

		result, err := Load()
		require.NoError(t, err)
		require.Nil(t, result.Config.Cache.Response.Simple)
	})
}

func TestLoad_ResponseSimpleOptInViaEnvWithoutYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		t.Setenv("RESPONSE_CACHE_SIMPLE_ENABLED", "true")
		t.Setenv("REDIS_URL", "redis://env-host:6379")
		t.Setenv("REDIS_KEY_RESPONSES", "env:responses")

		result, err := Load()
		require.NoError(t, err)

		cfg := result.Config
		require.NotNil(t, cfg.Cache.Response.Simple)
		require.NotNil(t, cfg.Cache.Response.Simple.Redis)
		assert.Equal(t, "redis://env-host:6379", cfg.Cache.Response.Simple.Redis.URL)
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
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expected, got, "ParseBodySizeLimitBytes(%q)", tt.input)
		})
	}
}

func TestLoad_ProviderModelFilterFromYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yaml := `
providers:
  openrouter:
    type: openrouter
    api_key: "sk-yaml-key"
    model_filter:
      include:
        - "*:free"
      exclude:
        - "*-preview:free"
      max_price_per_mtok: 0
`
		result := loadConfigYAML(t, dir, yaml)

		filter := result.RawProviders["openrouter"].ModelFilter
		assert.Len(t, filter.Include, 1)
		assert.Equal(t, "*:free", filter.Include[0])
		assert.Len(t, filter.Exclude, 1)
		assert.Equal(t, "*-preview:free", filter.Exclude[0])
		require.NotNil(t, filter.MaxPricePerMtok)
		assert.Equal(t, float64(0), *filter.MaxPricePerMtok)
		assert.False(t, filter.Empty())
	})
}

func TestModelFilterEmptyAndNormalize(t *testing.T) {
	tests := []struct {
		name      string
		filter    ModelFilter
		wantEmpty bool
	}{
		{name: "zero value", filter: ModelFilter{}, wantEmpty: true},
		{name: "blank patterns only", filter: ModelFilter{Include: []string{" ", ""}}, wantEmpty: true},
		{name: "include", filter: ModelFilter{Include: []string{"*:free"}}, wantEmpty: false},
		{name: "exclude", filter: ModelFilter{Exclude: []string{"*-preview"}}, wantEmpty: false},
		// A zero cap means "free models only", not "no cap configured".
		{name: "zero price cap", filter: ModelFilter{MaxPricePerMtok: new(float64)}, wantEmpty: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.filter.Empty()
			assert.Equal(t, tt.wantEmpty, got)

			normalized := tt.filter.Normalize()
			for _, pattern := range append(normalized.Include, normalized.Exclude...) {
				assert.NotEmpty(t, strings.TrimSpace(pattern), "Normalize() kept a blank pattern in %+v", normalized)
			}
		})
	}
}

// A cap that cannot express a real limit must fail loudly: NaN rejects every
// model, +Inf disables the cap, and a negative cap can never be met.
func TestModelFilterValidate(t *testing.T) {
	tests := []struct {
		name    string
		cap     *float64
		wantErr bool
	}{
		{name: "unset", cap: nil},
		{name: "zero", cap: new(0.0)},
		{name: "positive", cap: new(1.5)},
		{name: "negative", cap: new(-1.0), wantErr: true},
		{name: "NaN", cap: new(math.NaN()), wantErr: true},
		{name: "positive infinity", cap: new(math.Inf(1)), wantErr: true},
		{name: "negative infinity", cap: new(math.Inf(-1)), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ModelFilter{MaxPricePerMtok: tt.cap}.Validate("providers.openrouter.model_filter")
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "providers.openrouter.model_filter.max_price_per_mtok", "error should name the offending field")
		})
	}
}

func TestOfflineModeDisablesEveryUnsolicitedOutboundCall(t *testing.T) {
	tests := []struct {
		name        string
		modelList   string
		wantList    string
		wantVersion bool
	}{
		{name: "RemoteCatalogIsDropped", modelList: "", wantList: "", wantVersion: false},
		{name: "MirrorIsDropped", modelList: "https://mirror.internal/models.min.json", wantList: "", wantVersion: false},
		{name: "FileURLIsKept", modelList: "file:///etc/gomodel/models.json", wantList: "file:///etc/gomodel/models.json", wantVersion: false},
		{name: "BarePathIsKept", modelList: "/etc/gomodel/models.json", wantList: "/etc/gomodel/models.json", wantVersion: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			withTempDir(t, func(_ string) {
				t.Setenv("GOMODEL_OFFLINE", "true")
				if tt.modelList != "" {
					t.Setenv("MODEL_LIST_URL", tt.modelList)
				}
				result, err := Load()
				require.NoError(t, err)
				require.True(t, result.Config.Offline)
				got := result.Config.Cache.Model.ModelList.URL
				assert.Equal(t, tt.wantList, got)
				assert.Equal(t, tt.wantVersion, result.Config.VersionCheck.Enabled)
			})
		})
	}

	t.Run("OfflineWinsOverExplicitVersionCheckEnable", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		withTempDir(t, func(dir string) {
			yaml := "offline: true\nversion_check:\n  enabled: true\n"
			err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644)
			require.NoError(t, err)

			t.Setenv("GOMODEL_VERSION_CHECK_ENABLED", "true")
			result, err := Load()
			require.NoError(t, err)
			assert.False(t, result.Config.VersionCheck.Enabled)
		})
	})

	t.Run("DefaultIsOnline", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		withTempDir(t, func(_ string) {
			result, err := Load()
			require.NoError(t, err)
			assert.False(t, result.Config.Offline)
			assert.True(t, result.Config.VersionCheck.Enabled)
			assert.NotEmpty(t, result.Config.Cache.Model.ModelList.URL)
		})
	})
}

func TestIsLocalModelListSource(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"https://example.com/models.json", false},
		{"HTTP://example.com/models.json", false},
		{"file:///etc/gomodel/models.json", true},
		{"FILE:///etc/gomodel/models.json", true},
		{"/etc/gomodel/models.json", true},
		{"./models.json", true},
		{"models.json", true},
	}
	for _, tt := range tests {
		got := IsLocalModelListSource(tt.in)
		assert.Equal(t, tt.want, got, "IsLocalModelListSource(%q)", tt.in)
	}
}

func TestLoad_StreamStallTimeout(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(_ string) {
		result, err := Load()
		require.NoError(t, err)
		got := result.Config.Server.StreamStallTimeout
		require.Equal(t, DefaultStreamStallTimeoutSeconds, got)
	})

	withTempDir(t, func(dir string) {
		yaml := `
server:
  stream_stall_timeout: 0
`
		result := loadConfigYAML(t, dir, yaml)
		got := result.Config.Server.StreamStallTimeout
		require.Equal(t, 0, got)
	})

	withTempDir(t, func(dir string) {
		yaml := `
server:
  stream_stall_timeout: 120
`
		writeConfigYAML(t, dir, yaml)

		t.Setenv("STREAM_STALL_TIMEOUT", "15")

		result, err := Load()
		require.NoError(t, err)
		got := result.Config.Server.StreamStallTimeout
		require.Equal(t, 15, got)
	})

	withTempDir(t, func(_ string) {
		t.Setenv("STREAM_STALL_TIMEOUT", "-1")

		_, err := Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "server.stream_stall_timeout")
	})
}
