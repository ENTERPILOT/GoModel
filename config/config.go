// Package config provides configuration management for the application.
package config

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"

	"gomodel/internal/storage"
)

// Body size limit constants
const (
	DefaultBodySizeLimit int64 = 10 * 1024 * 1024  // 10MB
	MinBodySizeLimit     int64 = 1 * 1024          // 1KB
	MaxBodySizeLimit     int64 = 100 * 1024 * 1024 // 100MB
)

var bodySizeLimitRegex = regexp.MustCompile(`(?i)^(\d+)([KMG])?B?$`)

// Config holds the application configuration.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Cache      CacheConfig      `yaml:"cache"`
	Storage    StorageConfig    `yaml:"storage"`
	Logging    LogConfig        `yaml:"logging"`
	Usage      UsageConfig      `yaml:"usage"`
	Metrics    MetricsConfig    `yaml:"metrics"`
	HTTP       HTTPConfig       `yaml:"http"`
	Admin      AdminConfig      `yaml:"admin"`
	Guardrails GuardrailsConfig `yaml:"guardrails"`
	Resilience ResilienceConfig `yaml:"resilience"`
}

// LoadResult is returned by Load and bundles the application config with the raw
// provider map parsed from YAML. Provider env vars and resolution are handled by
// the providers package.
type LoadResult struct {
	Config       *Config
	RawProviders map[string]RawProviderConfig
}

// RawProviderConfig is the YAML-sourced provider configuration before env var
// overrides, credential filtering, or resilience merging. Exported so the
// providers package can resolve it into a fully-configured ProviderConfig.
type RawProviderConfig struct {
	Type       string               `yaml:"type"`
	APIKey     string               `yaml:"api_key"`
	BaseURL    string               `yaml:"base_url"`
	APIVersion string               `yaml:"api_version"`
	Models     []string             `yaml:"models"`
	Resilience *RawResilienceConfig `yaml:"resilience"`
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
	FailureThreshold *int           `yaml:"failure_threshold"`
	SuccessThreshold *int           `yaml:"success_threshold"`
	Timeout          *time.Duration `yaml:"timeout"`
}

// RawRetryConfig holds optional per-provider retry overrides from YAML.
// Nil fields inherit from the global RetryConfig.
type RawRetryConfig struct {
	MaxRetries     *int           `yaml:"max_retries"`
	InitialBackoff *time.Duration `yaml:"initial_backoff"`
	MaxBackoff     *time.Duration `yaml:"max_backoff"`
	BackoffFactor  *float64       `yaml:"backoff_factor"`
	JitterFactor   *float64       `yaml:"jitter_factor"`
}

// AdminConfig holds configuration for the admin API and dashboard UI.
type AdminConfig struct {
	// EndpointsEnabled controls whether the admin REST API is active
	// Default: true
	EndpointsEnabled bool `yaml:"endpoints_enabled" env:"ADMIN_ENDPOINTS_ENABLED"`

	// UIEnabled controls whether the admin dashboard UI is active
	// Requires EndpointsEnabled — if endpoints are disabled and UI is enabled,
	// a warning is logged and UI is forced to false.
	// Default: true
	UIEnabled bool `yaml:"ui_enabled" env:"ADMIN_UI_ENABLED"`
}

// GuardrailsConfig holds configuration for the request guardrails pipeline.
type GuardrailsConfig struct {
	// Enabled controls whether guardrails are active
	// Default: false
	Enabled bool `yaml:"enabled" env:"GUARDRAILS_ENABLED"`

	// EnableForBatchProcessing controls whether guardrails are applied to inline
	// batch items for /v1/batches requests.
	// Default: false
	EnableForBatchProcessing bool `yaml:"enable_for_batch_processing" env:"ENABLE_GUARDRAILS_FOR_BATCH_PROCESSING"`

	// Rules is a list of guardrail instances. Each entry defines one guardrail
	// with its own name, type, order, and type-specific settings. Multiple
	// instances of the same type are allowed (e.g. two system_prompt guardrails
	// with different content).
	Rules []GuardrailRuleConfig `yaml:"rules"`
}

// GuardrailRuleConfig defines a single guardrail instance.
type GuardrailRuleConfig struct {
	// Name is a unique identifier for this guardrail instance (used in logs and errors)
	Name string `yaml:"name"`

	// Type selects the guardrail implementation: "system_prompt"
	Type string `yaml:"type"`

	// Order controls execution ordering relative to other guardrails.
	// Guardrails with the same order run in parallel; different orders run sequentially.
	// Default: 0
	Order int `yaml:"order"`

	// SystemPrompt holds settings when Type is "system_prompt"
	SystemPrompt SystemPromptSettings `yaml:"system_prompt"`
}

// SystemPromptSettings holds the type-specific settings for a system_prompt guardrail.
type SystemPromptSettings struct {
	// Mode controls how the system prompt is applied: "inject", "override", or "decorator"
	//   - inject: adds a system message only if none exists
	//   - override: replaces all existing system messages
	//   - decorator: prepends to the first existing system message
	// Default: "inject"
	Mode string `yaml:"mode"`

	// Content is the system prompt text to apply
	Content string `yaml:"content"`
}

// HTTPConfig holds HTTP client configuration for upstream API requests.
// These values are also readable via the HTTP_TIMEOUT and HTTP_RESPONSE_HEADER_TIMEOUT
// environment variables in internal/httpclient/client.go.
type HTTPConfig struct {
	// Timeout is the overall HTTP request timeout in seconds (default: 600)
	Timeout int `yaml:"timeout" env:"HTTP_TIMEOUT"`

	// ResponseHeaderTimeout is the time to wait for response headers in seconds (default: 600)
	ResponseHeaderTimeout int `yaml:"response_header_timeout" env:"HTTP_RESPONSE_HEADER_TIMEOUT"`
}

// LogConfig holds audit logging configuration
type LogConfig struct {
	// Enabled controls whether audit logging is active
	// Default: false
	Enabled bool `yaml:"enabled" env:"LOGGING_ENABLED"`

	// LogBodies enables logging of full request/response bodies
	// WARNING: May contain sensitive data (PII, API keys in prompts)
	// Default: true
	LogBodies bool `yaml:"log_bodies" env:"LOGGING_LOG_BODIES"`

	// LogHeaders enables logging of request/response headers
	// Sensitive headers (Authorization, Cookie, etc.) are auto-redacted
	// Default: true
	LogHeaders bool `yaml:"log_headers" env:"LOGGING_LOG_HEADERS"`

	// BufferSize is the number of log entries to buffer before flushing
	// Default: 1000
	BufferSize int `yaml:"buffer_size" env:"LOGGING_BUFFER_SIZE"`

	// FlushInterval is how often to flush buffered logs (in seconds)
	// Default: 5
	FlushInterval int `yaml:"flush_interval" env:"LOGGING_FLUSH_INTERVAL"`

	// RetentionDays is how long to keep logs (0 = forever)
	// Default: 30
	RetentionDays int `yaml:"retention_days" env:"LOGGING_RETENTION_DAYS"`

	// OnlyModelInteractions limits audit logging to AI model endpoints only
	// When true, only /v1/chat/completions, /v1/responses, /v1/embeddings, /v1/files, and /v1/batches are logged
	// Endpoints like /health, /metrics, /admin, /v1/models are skipped
	// Default: true
	OnlyModelInteractions bool `yaml:"only_model_interactions" env:"LOGGING_ONLY_MODEL_INTERACTIONS"`
}

// UsageConfig holds token usage tracking configuration
type UsageConfig struct {
	// Enabled controls whether usage tracking is active
	// Default: true
	Enabled bool `yaml:"enabled" env:"USAGE_ENABLED"`

	// EnforceReturningUsageData controls whether to ask streaming providers to return usage data when possible.
	// When true, stream_options: {"include_usage": true} is added for provider paths that support it.
	// Default: true
	EnforceReturningUsageData bool `yaml:"enforce_returning_usage_data" env:"ENFORCE_RETURNING_USAGE_DATA"`

	// BufferSize is the number of usage entries to buffer before flushing
	// Default: 1000
	BufferSize int `yaml:"buffer_size" env:"USAGE_BUFFER_SIZE"`

	// FlushInterval is how often to flush buffered usage entries (in seconds)
	// Default: 5
	FlushInterval int `yaml:"flush_interval" env:"USAGE_FLUSH_INTERVAL"`

	// RetentionDays is how long to keep usage data (0 = forever)
	// Default: 90
	RetentionDays int `yaml:"retention_days" env:"USAGE_RETENTION_DAYS"`
}

// StorageConfig holds database storage configuration (used by audit logging, usage tracking, future IAM, etc.)
type StorageConfig struct {
	// Type specifies the storage backend: "sqlite" (default), "postgresql", or "mongodb"
	Type string `yaml:"type" env:"STORAGE_TYPE"`

	// SQLite configuration
	SQLite SQLiteStorageConfig `yaml:"sqlite"`

	// PostgreSQL configuration
	PostgreSQL PostgreSQLStorageConfig `yaml:"postgresql"`

	// MongoDB configuration
	MongoDB MongoDBStorageConfig `yaml:"mongodb"`
}

// SQLiteStorageConfig holds SQLite-specific storage configuration
type SQLiteStorageConfig struct {
	// Path is the database file path (default: data/gomodel.db)
	Path string `yaml:"path" env:"SQLITE_PATH"`
}

// PostgreSQLStorageConfig holds PostgreSQL-specific storage configuration
type PostgreSQLStorageConfig struct {
	// URL is the connection string (e.g., postgres://user:pass@localhost/dbname)
	URL string `yaml:"url" env:"POSTGRES_URL"`
	// MaxConns is the maximum connection pool size (default: 10)
	MaxConns int `yaml:"max_conns" env:"POSTGRES_MAX_CONNS"`
}

// MongoDBStorageConfig holds MongoDB-specific storage configuration
type MongoDBStorageConfig struct {
	// URL is the connection string (e.g., mongodb://localhost:27017)
	URL string `yaml:"url" env:"MONGODB_URL"`
	// Database is the database name (default: gomodel)
	Database string `yaml:"database" env:"MONGODB_DATABASE"`
}

// CacheConfig holds model and response cache configuration.
type CacheConfig struct {
	Model    ModelCacheConfig    `yaml:"model"`
	Response ResponseCacheConfig `yaml:"response"`
}

// ModelCacheConfig holds cache configuration for model registry.
// Exactly one of Local or Redis must be non-nil.
type ModelCacheConfig struct {
	RefreshInterval int               `yaml:"refresh_interval" env:"CACHE_REFRESH_INTERVAL"`
	ModelList       ModelListConfig   `yaml:"model_list"`
	Local           *LocalCacheConfig `yaml:"local"`
	Redis           *RedisModelConfig `yaml:"redis"`
}

// LocalCacheConfig holds local file cache configuration.
type LocalCacheConfig struct {
	CacheDir string `yaml:"cache_dir" env:"GOMODEL_CACHE_DIR"`
}

// ModelListConfig holds configuration for fetching the external model metadata registry.
type ModelListConfig struct {
	// URL is the HTTP(S) URL to fetch models.json from (empty = disabled)
	URL string `yaml:"url" env:"MODEL_LIST_URL"`
}

// RedisModelConfig holds Redis connection configuration for the model registry cache.
type RedisModelConfig struct {
	URL string `yaml:"url" env:"REDIS_URL"`
	Key string `yaml:"key" env:"REDIS_KEY_MODELS"`
	TTL int    `yaml:"ttl" env:"REDIS_TTL_MODELS"`
}

// RedisResponseConfig holds Redis connection configuration for the response cache.
// Uses separate env vars from RedisModelConfig for key and TTL to allow independent
// configuration. The URL is shared via REDIS_URL to simplify single-Redis deployments;
// use YAML config if different Redis instances are needed for model and response caches.
type RedisResponseConfig struct {
	URL string `yaml:"url" env:"REDIS_URL"`
	Key string `yaml:"key" env:"REDIS_KEY_RESPONSES"`
	TTL int    `yaml:"ttl" env:"REDIS_TTL_RESPONSES"`
}

// ResponseCacheConfig holds configuration for response cache middleware.
type ResponseCacheConfig struct {
	Simple SimpleCacheConfig `yaml:"simple"`
}

// SimpleCacheConfig holds configuration for exact-match response caching.
type SimpleCacheConfig struct {
	Redis *RedisResponseConfig `yaml:"redis"`
}

// ValidateCacheConfig validates the cache configuration in c.
// For the model cache, exactly one backend (Local or Redis) must be configured;
// having both or neither is an error. When Redis is selected, its URL must be
// non-empty. Returns a descriptive error if any constraint is violated, or nil
// if the configuration is valid.
func ValidateCacheConfig(c *CacheConfig) error {
	if c == nil {
		return fmt.Errorf("cache: configuration is required")
	}
	m := &c.Model
	hasLocal := m.Local != nil
	hasRedis := m.Redis != nil

	if hasLocal && hasRedis {
		return fmt.Errorf("cache.model: cannot have both local and redis configured; choose one")
	}
	if !hasLocal && !hasRedis {
		return fmt.Errorf("cache.model: must have either local or redis configured")
	}
	if hasRedis && m.Redis.URL == "" {
		return fmt.Errorf("cache.model.redis: URL is required when using redis")
	}
	return nil
}

// ServerConfig holds HTTP server configuration
type ServerConfig struct {
	Port           string `yaml:"port" env:"PORT"`
	MasterKey      string `yaml:"master_key" env:"GOMODEL_MASTER_KEY"`   // Optional: Master key for authentication
	BodySizeLimit  string `yaml:"body_size_limit" env:"BODY_SIZE_LIMIT"` // Max request body size (e.g., "10M", "1024K")
	SwaggerEnabled bool   `yaml:"swagger_enabled" env:"SWAGGER_ENABLED"` // Whether to expose the Swagger UI at /swagger/index.html
	PprofEnabled   bool   `yaml:"pprof_enabled" env:"PPROF_ENABLED"`     // Whether to expose debug profiling routes at /debug/pprof/*
	// EnablePassthroughRoutes exposes provider-native passthrough endpoints under
	// /p/{provider}/{endpoint}. Default: true.
	EnablePassthroughRoutes bool `yaml:"enable_passthrough_routes" env:"ENABLE_PASSTHROUGH_ROUTES"`
	// AllowPassthroughV1Alias allows /p/{provider}/v1/... style passthrough routes
	// while keeping /p/{provider}/... as the canonical form. Default: true.
	AllowPassthroughV1Alias bool `yaml:"allow_passthrough_v1_alias" env:"ALLOW_PASSTHROUGH_V1_ALIAS"`
	// EnabledPassthroughProviders lists the provider types enabled on
	// /p/{provider}/... passthrough routes. Default: ["openai", "anthropic"].
	EnabledPassthroughProviders []string `yaml:"enabled_passthrough_providers" env:"ENABLED_PASSTHROUGH_PROVIDERS"`
}

// MetricsConfig holds observability configuration for Prometheus metrics
type MetricsConfig struct {
	// Enabled controls whether Prometheus metrics are collected and exposed
	// Default: false
	Enabled bool `yaml:"enabled" env:"METRICS_ENABLED"`

	// Endpoint is the HTTP path where metrics are exposed
	// Default: "/metrics"
	Endpoint string `yaml:"endpoint" env:"METRICS_ENDPOINT"`
}

// RetryConfig holds resolved retry settings for an LLM client.
// This is the canonical type shared between config and llmclient.
type RetryConfig struct {
	MaxRetries     int           `yaml:"max_retries"     env:"RETRY_MAX_RETRIES"`
	InitialBackoff time.Duration `yaml:"initial_backoff" env:"RETRY_INITIAL_BACKOFF"`
	MaxBackoff     time.Duration `yaml:"max_backoff"     env:"RETRY_MAX_BACKOFF"`
	BackoffFactor  float64       `yaml:"backoff_factor"  env:"RETRY_BACKOFF_FACTOR"`
	JitterFactor   float64       `yaml:"jitter_factor"   env:"RETRY_JITTER_FACTOR"`
}

// DefaultRetryConfig returns the default retry settings.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     30 * time.Second,
		BackoffFactor:  2.0,
		JitterFactor:   0.1,
	}
}

// CircuitBreakerConfig holds resolved circuit breaker settings.
// This is the canonical type shared between config and llmclient.
type CircuitBreakerConfig struct {
	FailureThreshold int           `yaml:"failure_threshold" env:"CIRCUIT_BREAKER_FAILURE_THRESHOLD"`
	SuccessThreshold int           `yaml:"success_threshold" env:"CIRCUIT_BREAKER_SUCCESS_THRESHOLD"`
	Timeout          time.Duration `yaml:"timeout"           env:"CIRCUIT_BREAKER_TIMEOUT"`
}

// DefaultCircuitBreakerConfig returns the default circuit breaker settings.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Timeout:          30 * time.Second,
	}
}

// ResilienceConfig holds resolved resilience settings (retry and circuit breaker).
type ResilienceConfig struct {
	Retry          RetryConfig          `yaml:"retry"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
}

// buildDefaultConfig returns the single source of truth for all configuration defaults.
func buildDefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:                    "8080",
			SwaggerEnabled:          true,
			PprofEnabled:            false,
			EnablePassthroughRoutes: true,
			AllowPassthroughV1Alias: true,
			EnabledPassthroughProviders: []string{
				"openai",
				"anthropic",
			},
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
			MongoDB: MongoDBStorageConfig{
				Database: "gomodel",
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
			Enabled:                   true,
			EnforceReturningUsageData: true,
			BufferSize:                1000,
			FlushInterval:             5,
			RetentionDays:             90,
		},
		Metrics: MetricsConfig{
			Endpoint: "/metrics",
		},
		HTTP: HTTPConfig{
			Timeout:               600,
			ResponseHeaderTimeout: 600,
		},
		Resilience: ResilienceConfig{
			Retry:          DefaultRetryConfig(),
			CircuitBreaker: DefaultCircuitBreakerConfig(),
		},
		Admin:      AdminConfig{EndpointsEnabled: true, UIEnabled: true},
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
	_ = godotenv.Load()

	cfg := buildDefaultConfig()

	rawProviders, err := applyYAML(cfg)
	if err != nil {
		return nil, err
	}

	if err := applyEnvOverrides(cfg); err != nil {
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

// applyEnvOverrides walks cfg's struct fields and applies env var overrides
// based on `env` struct tags. Maps are skipped.
func applyEnvOverrides(cfg *Config) error {
	return applyEnvOverridesValue(reflect.ValueOf(cfg).Elem())
}

// hasEnvDescendants reports whether t (a struct type) contains any field (at
// any depth) with a non-empty "env" struct tag. Used to decide whether to
// allocate a nil pointer-to-struct before recursing into it.
func hasEnvDescendants(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for f := range t.Fields() {
		f := f
		if f.Tag.Get("env") != "" {
			return true
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && hasEnvDescendants(ft) {
			return true
		}
	}
	return false
}

func applyEnvOverridesValue(v reflect.Value) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldVal := v.Field(i)

		if field.Type.Kind() == reflect.Map {
			continue
		}
		if field.Type.Kind() == reflect.Struct {
			if err := applyEnvOverridesValue(fieldVal); err != nil {
				return err
			}
			continue
		}
		if field.Type.Kind() == reflect.Pointer {
			if fieldVal.IsNil() {
				// Only allocate if the pointed-to struct has env-tagged descendants;
				// otherwise leave it nil so optional config sections stay absent.
				elemType := field.Type.Elem()
				if elemType.Kind() != reflect.Struct || !hasEnvDescendants(elemType) {
					continue
				}
				// Allocate a zero-value struct so env vars can populate its fields.
				newVal := reflect.New(elemType)
				if err := applyEnvOverridesValue(newVal.Elem()); err != nil {
					return err
				}
				// Only keep the allocation if at least one field was actually set.
				if !reflect.DeepEqual(newVal.Elem().Interface(), reflect.Zero(elemType).Interface()) {
					fieldVal.Set(newVal)
				}
			} else {
				if err := applyEnvOverridesValue(fieldVal.Elem()); err != nil {
					return err
				}
			}
			continue
		}

		envKey := field.Tag.Get("env")
		if envKey == "" {
			continue
		}
		envVal := os.Getenv(envKey)
		if envVal == "" {
			continue
		}

		switch field.Type.Kind() {
		case reflect.String:
			fieldVal.SetString(envVal)
		case reflect.Bool:
			fieldVal.SetBool(parseBool(envVal))
		case reflect.Slice:
			if field.Type.Elem().Kind() != reflect.String {
				continue
			}
			items := strings.Split(envVal, ",")
			values := make([]string, 0, len(items))
			for _, item := range items {
				trimmed := strings.TrimSpace(item)
				if trimmed == "" {
					continue
				}
				values = append(values, trimmed)
			}
			fieldVal.Set(reflect.ValueOf(values))
		case reflect.Int:
			n, err := strconv.Atoi(envVal)
			if err != nil {
				return fmt.Errorf("invalid value for %s (%s): %q is not a valid integer", field.Name, envKey, envVal)
			}
			fieldVal.SetInt(int64(n))
		case reflect.Int64:
			if field.Type == reflect.TypeFor[time.Duration]() {
				// time.Duration is represented as int64; accept Go duration strings (e.g. "1s", "500ms").
				d, err := time.ParseDuration(envVal)
				if err != nil {
					return fmt.Errorf("invalid value for %s (%s): %q is not a valid duration", field.Name, envKey, envVal)
				}
				fieldVal.SetInt(int64(d))
			} else {
				n, err := strconv.ParseInt(envVal, 10, 64)
				if err != nil {
					return fmt.Errorf("invalid value for %s (%s): %q is not a valid integer", field.Name, envKey, envVal)
				}
				fieldVal.SetInt(n)
			}
		case reflect.Float64:
			f, err := strconv.ParseFloat(envVal, 64)
			if err != nil {
				return fmt.Errorf("invalid value for %s (%s): %q is not a valid float", field.Name, envKey, envVal)
			}
			fieldVal.SetFloat(f)
		}
	}
	return nil
}

// expandString expands environment variable references like ${VAR} or ${VAR:-default} in a string.
func expandString(s string) string {
	if s == "" {
		return s
	}
	return os.Expand(s, func(key string) string {
		varname := key
		defaultValue := ""
		hasDefault := false
		if before, after, ok := strings.Cut(key, ":-"); ok {
			varname = before
			defaultValue = after
			hasDefault = true
		}
		value := os.Getenv(varname)
		if value == "" {
			if hasDefault {
				return defaultValue
			}
			return "${" + key + "}"
		}
		return value
	})
}

// parseBool returns true if s is "true" or "1" (case-insensitive).
func parseBool(s string) bool {
	return strings.EqualFold(s, "true") || s == "1"
}

// ValidateBodySizeLimit validates a body size limit string.
// Accepts formats like: "10M", "10MB", "1024K", "1024KB", "104857600"
// Returns an error if the format is invalid or value is outside bounds (1KB - 100MB).
func ValidateBodySizeLimit(s string) error {
	_, err := ParseBodySizeLimitBytes(s)
	return err
}

// ParseBodySizeLimitBytes parses a configured body size limit into bytes.
// Accepts formats like: "10M", "10MB", "1024K", "1024KB", "104857600".
// Returns an error if the format is invalid or value is outside bounds (1KB - 100MB).
func ParseBodySizeLimitBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	matches := bodySizeLimitRegex.FindStringSubmatch(s)
	if matches == nil {
		return 0, fmt.Errorf("invalid format %q: expected pattern like '10M', '1024K', or '104857600'", s)
	}

	value, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in %q: %w", s, err)
	}

	switch strings.ToUpper(matches[2]) {
	case "K":
		value *= 1024
	case "M":
		value *= 1024 * 1024
	case "G":
		value *= 1024 * 1024 * 1024
	}

	if value < MinBodySizeLimit {
		return 0, fmt.Errorf("value %d bytes is below minimum of %d bytes (1KB)", value, MinBodySizeLimit)
	}
	if value > MaxBodySizeLimit {
		return 0, fmt.Errorf("value %d bytes exceeds maximum of %d bytes (100MB)", value, MaxBodySizeLimit)
	}

	return value, nil
}
