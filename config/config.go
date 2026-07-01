// Package config provides configuration management for the application.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"gomodel/internal/storage"
)

// Config holds the application configuration.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Models     ModelsConfig     `yaml:"models"`
	Cache      CacheConfig      `yaml:"cache"`
	Storage    StorageConfig    `yaml:"storage"`
	Logging    LogConfig        `yaml:"logging"`
	Usage      UsageConfig      `yaml:"usage"`
	Budgets    BudgetsConfig    `yaml:"budgets"`
	Metrics    MetricsConfig    `yaml:"metrics"`
	HTTP       HTTPConfig       `yaml:"http"`
	Admin      AdminConfig      `yaml:"admin"`
	Guardrails GuardrailsConfig `yaml:"guardrails"`
	Failover   FailoverConfig   `yaml:"failover"`
	Workflows  WorkflowsConfig  `yaml:"workflows"`
	Resilience ResilienceConfig `yaml:"resilience"`

	// VirtualModels declares redirects, load balancers, and access policies as
	// infrastructure-as-code. They override admin-store rows of the same source.
	VirtualModels []VirtualModelConfig `yaml:"virtual_models"`
}

// LoadResult is returned by Load and bundles the application config with the raw
// provider map parsed from YAML. Provider env vars and resolution are handled by
// the providers package.
type LoadResult struct {
	Config       *Config
	RawProviders map[string]RawProviderConfig
}

// buildDefaultConfig returns the single source of truth for all configuration defaults.
func buildDefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:                    "8080",
			BasePath:                "/",
			UserPathHeader:          "X-GoModel-User-Path",
			SwaggerEnabled:          false,
			PprofEnabled:            false,
			EnablePassthroughRoutes: true,
			AllowPassthroughV1Alias: true,
			RealtimeEnabled:         true,
			EnabledPassthroughProviders: []string{
				"openai",
				"anthropic",
				"openrouter",
				"zai",
				"vllm",
				"deepseek",
			},
		},
		Models: ModelsConfig{
			EnabledByDefault:                true,
			KeepOnlyAliasesAtModelsEndpoint: false,
			ConfiguredProviderModelsMode:    ConfiguredProviderModelsModeFallback,
		},
		Cache: CacheConfig{
			Model: ModelCacheConfig{
				RefreshInterval: 3600,
				ModelList: ModelListConfig{
					URL: "https://raw.githubusercontent.com/ENTERPILOT/ai-model-list/refs/heads/main/models.min.json",
				},
				Local: nil,
				Redis: nil,
			},
			Response: ResponseCacheConfig{},
		},
		Storage: StorageConfig{
			Type: "sqlite",
			SQLite: SQLiteStorageConfig{
				Path: storage.DefaultSQLitePath,
			},
			PostgreSQL: PostgreSQLStorageConfig{
				MaxConns: 10,
			},
		},
		Logging: LogConfig{
			LogBodies:             true,
			LogHeaders:            true,
			BufferSize:            1000,
			FlushInterval:         5,
			RetentionDays:         30,
			OnlyModelInteractions: true,
		},
		Usage: UsageConfig{
			Enabled:                     true,
			EnforceReturningUsageData:   true,
			PricingRecalculationEnabled: true,
			BufferSize:                  1000,
			FlushInterval:               5,
			RetentionDays:               90,
		},
		Budgets: BudgetsConfig{
			Enabled: true,
		},
		Metrics: MetricsConfig{
			Endpoint: "/metrics",
		},
		HTTP: HTTPConfig{
			Timeout:               600,
			ResponseHeaderTimeout: 600,
		},
		Failover: FailoverConfig{
			Enabled:     true,
			DefaultMode: FailoverModeManual,
		},
		Workflows: WorkflowsConfig{
			RefreshInterval: time.Minute,
		},
		Resilience: ResilienceConfig{
			Retry:          DefaultRetryConfig(),
			CircuitBreaker: DefaultCircuitBreakerConfig(),
		},
		Admin: AdminConfig{
			EndpointsEnabled:         true,
			UIEnabled:                true,
			LiveLogsEnabled:          true,
			LiveLogsBufferSize:       10000,
			LiveLogsReplayLimit:      1000,
			LiveLogsHeartbeatSeconds: 15,
		},
		Guardrails: GuardrailsConfig{},
	}
}

// Load reads configuration from file and environment using a three-layer pipeline:
//
//	defaults (code) → config.yaml (optional overlay) → env vars (always win)
//
// The returned LoadResult contains the resolved application Config and the raw
// provider map parsed from YAML. Provider env var discovery, credential filtering,
// and resilience merging are handled by the providers package.
func Load() (*LoadResult, error) {
	cfg := buildDefaultConfig()

	rawProviders, err := applyYAML(cfg)
	if err != nil {
		return nil, err
	}

	if err := applyResponseSimpleEnv(&cfg.Cache.Response); err != nil {
		return nil, err
	}
	if err := applyResponseSemanticEnv(&cfg.Cache.Response); err != nil {
		return nil, err
	}
	mergeSemanticResponseDefaults(cfg.Cache.Response.Semantic)

	if err := applyEnvOverrides(cfg); err != nil {
		return nil, err
	}
	if err := applyVirtualModelsEnv(cfg); err != nil {
		return nil, err
	}
	applyBudgetDependencies(cfg)
	if err := applyBudgetEnv(cfg); err != nil {
		return nil, err
	}
	if err := validateBudgetConfig(&cfg.Budgets); err != nil {
		return nil, err
	}
	cfg.Server.BasePath = NormalizeBasePath(cfg.Server.BasePath)
	cfg.Server.UserPathHeader, err = NormalizeHeaderName(cfg.Server.UserPathHeader, "X-GoModel-User-Path")
	if err != nil {
		return nil, fmt.Errorf("invalid server.user_path_header: %w", err)
	}
	cfg.Models.ConfiguredProviderModelsMode = ResolveConfiguredProviderModelsMode(cfg.Models.ConfiguredProviderModelsMode)
	if !cfg.Models.ConfiguredProviderModelsMode.Valid() {
		return nil, fmt.Errorf("models.configured_provider_models_mode must be one of: fallback, allowlist")
	}

	if err := loadFailoverConfig(&cfg.Failover); err != nil {
		return nil, err
	}

	// When no model cache backend was specified at all, default to local.
	if cfg.Cache.Model.Local == nil && cfg.Cache.Model.Redis == nil {
		cfg.Cache.Model.Local = &LocalCacheConfig{}
	}

	if cfg.Server.BodySizeLimit != "" {
		if err := ValidateBodySizeLimit(cfg.Server.BodySizeLimit); err != nil {
			return nil, fmt.Errorf("invalid BODY_SIZE_LIMIT: %w", err)
		}
	}

	if err := ValidateCacheConfig(&cfg.Cache); err != nil {
		return nil, err
	}

	return &LoadResult{
		Config:       cfg,
		RawProviders: rawProviders,
	}, nil
}

// applyYAML reads an optional config.yaml and overlays it onto cfg.
// Returns the raw provider map parsed from the providers: YAML section.
// If no config file is found, this is a no-op (not an error).
func applyYAML(cfg *Config) (map[string]RawProviderConfig, error) {
	paths := []string{
		"config/config.yaml",
		"config.yaml",
	}

	var data []byte
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err == nil {
			data = raw
			break
		}
	}

	rawProviders := make(map[string]RawProviderConfig)

	if data == nil {
		return rawProviders, nil
	}

	expanded := expandString(string(data))

	// yamlTarget is a local struct that mirrors Config for YAML unmarshaling,
	// using RawProviderConfig for providers so nullable resilience overrides are preserved.
	type yamlTarget struct {
		*Config      `yaml:",inline"`
		RawProviders map[string]RawProviderConfig `yaml:"providers"`
	}

	target := yamlTarget{Config: cfg}
	if err := yaml.Unmarshal([]byte(expanded), &target); err != nil {
		return nil, fmt.Errorf("failed to parse config.yaml: %w", err)
	}

	if target.RawProviders != nil {
		rawProviders = target.RawProviders
	}

	return rawProviders, nil
}
