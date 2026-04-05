// Package config provides configuration management for the application.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
	Server         ServerConfig         `yaml:"server"`
	Cache          CacheConfig          `yaml:"cache"`
	Storage        StorageConfig        `yaml:"storage"`
	Logging        LogConfig            `yaml:"logging"`
	Usage          UsageConfig          `yaml:"usage"`
	Metrics        MetricsConfig        `yaml:"metrics"`
	HTTP           HTTPConfig           `yaml:"http"`
	Admin          AdminConfig          `yaml:"admin"`
	Guardrails     GuardrailsConfig     `yaml:"guardrails"`
	Fallback       FallbackConfig       `yaml:"fallback"`
	ExecutionPlans ExecutionPlansConfig `yaml:"execution_plans"`
	Resilience     ResilienceConfig     `yaml:"resilience"`
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

// FallbackMode controls how alternate models are selected when the primary
// model is unavailable.
type FallbackMode string

const (
	FallbackModeOff    FallbackMode = "off"
	FallbackModeManual FallbackMode = "manual"
	FallbackModeAuto   FallbackMode = "auto"
)

// Valid reports whether mode is one of the supported fallback modes.
func (m FallbackMode) Valid() bool {
	switch normalizeFallbackMode(m) {
	case FallbackModeOff, FallbackModeManual, FallbackModeAuto:
		return true
	default:
		return false
	}
}

func normalizeFallbackMode(mode FallbackMode) FallbackMode {
	return FallbackMode(strings.ToLower(strings.TrimSpace(string(mode))))
}

// FallbackModelOverride holds per-model mode overrides.
type FallbackModelOverride struct {
	Mode FallbackMode `yaml:"mode" json:"mode"`
}

// FallbackConfig holds translated-route model fallback policy.
type FallbackConfig struct {
	// DefaultMode controls the fallback behavior when no per-model override exists.
	// Supported values: "auto", "manual", "off". Default: "off".
	DefaultMode FallbackMode `yaml:"default_mode" env:"FEATURE_FALLBACK_MODE"`

	// ManualRulesPath points to a JSON file that maps source model selectors to
	// ordered fallback model selector lists. Empty disables manual rules.
	ManualRulesPath string `yaml:"manual_rules_path" env:"FALLBACK_MANUAL_RULES_PATH"`

	// Overrides controls per-model mode overrides. Keys may be bare models
	// ("gpt-4o") or provider-qualified public selectors ("azure/gpt-4o").
	Overrides map[string]FallbackModelOverride `yaml:"overrides"`

	// Manual holds the parsed manual fallback lists loaded from ManualRulesPath.
	Manual map[string][]string `yaml:"-"`
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

	// Type selects the guardrail implementation: "system_prompt" or "llm_based_altering"
	Type string `yaml:"type"`

	// UserPath scopes internal auxiliary guardrail requests for workflow
	// selection and audit logging. When empty, the caller user path is used.
	UserPath string `yaml:"user_path"`

	// Order controls execution ordering relative to other guardrails.
	// Guardrails with the same order run in parallel; different orders run sequentially.
	// Default: 0
	Order int `yaml:"order"`

	// SystemPrompt holds settings when Type is "system_prompt"
	SystemPrompt SystemPromptSettings `yaml:"system_prompt"`

	// LLMBasedAltering holds settings when Type is "llm_based_altering"
	LLMBasedAltering LLMBasedAlteringSettings `yaml:"llm_based_altering"`
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

// LLMBasedAlteringSettings holds the type-specific settings for an llm_based_altering guardrail.
type LLMBasedAlteringSettings struct {
	// Model is the model selector used for the auxiliary rewrite call.
	// This can be a concrete model name, provider-qualified selector, or alias.
	Model string `yaml:"model"`

	// Provider is an optional routing hint for Model.
	Provider string `yaml:"provider"`

	// Prompt is the system prompt used to rewrite targeted messages.
	// When empty, the built-in LiteLLM-derived anonymization prompt is used.
	Prompt string `yaml:"prompt"`

	// Roles selects which message roles are rewritten.
	// Default: ["user"]
	Roles []string `yaml:"roles"`

	// SkipContentPrefix skips rewriting for messages whose trimmed text begins with this prefix.
	SkipContentPrefix string `yaml:"skip_content_prefix"`

	// MaxTokens limits the auxiliary rewrite completion.
	// Default: 4096
	MaxTokens int `yaml:"max_tokens"`
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

// ExecutionPlansConfig holds runtime refresh behavior for persisted execution plans.
type ExecutionPlansConfig struct {
	// RefreshInterval controls how often the in-memory execution-plan snapshot
	// is refreshed from storage. Default: 1m.
	RefreshInterval time.Duration `yaml:"refresh_interval" env:"EXECUTION_PLAN_REFRESH_INTERVAL"`
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

// BackendConfig converts the application storage config into the internal storage config.
func (c StorageConfig) BackendConfig() storage.Config {
	cfg := storage.Config{
		Type: c.Type,
		SQLite: storage.SQLiteConfig{
			Path: c.SQLite.Path,
		},
		PostgreSQL: storage.PostgreSQLConfig{
			URL:      c.PostgreSQL.URL,
			MaxConns: c.PostgreSQL.MaxConns,
		},
		MongoDB: storage.MongoDBConfig{
			URL:      c.MongoDB.URL,
			Database: c.MongoDB.Database,
		},
	}
	if cfg.Type == "" {
		cfg.Type = storage.TypeSQLite
	}
	if cfg.SQLite.Path == "" {
		cfg.SQLite.Path = storage.DefaultSQLitePath
	}
	if cfg.MongoDB.Database == "" {
		cfg.MongoDB.Database = "gomodel"
	}
	return cfg
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
// Env vars are applied in Load via applyResponseSimpleEnv, only when cache.response.simple is present
// (see RESPONSE_CACHE_SIMPLE_ENABLED for env-only opt-in without YAML).
type RedisResponseConfig struct {
	URL string `yaml:"url"`
	Key string `yaml:"key"`
	TTL int    `yaml:"ttl"`
}

// ResponseCacheConfig holds configuration for response cache middleware.
type ResponseCacheConfig struct {
	Simple   *SimpleCacheConfig   `yaml:"simple"`
	Semantic *SemanticCacheConfig `yaml:"semantic"`
}

// SimpleCacheConfig holds configuration for exact-match response caching.
// When the simple block is omitted from config.yaml, this layer stays off unless
// RESPONSE_CACHE_SIMPLE_ENABLED=true is set (e.g. Helm without a response-cache YAML fragment).
// Omitted enabled (nil) means true whenever the simple block exists.
type SimpleCacheConfig struct {
	Enabled *bool                `yaml:"enabled"`
	Redis   *RedisResponseConfig `yaml:"redis"`
}

// SemanticCacheConfig holds configuration for the semantic (vector-similarity) response cache.
// When the semantic block is omitted from config.yaml, this layer stays off unless
// SEMANTIC_CACHE_ENABLED=true is set. Omitted enabled (nil) means true whenever the semantic block exists.
// Tuning env vars are applied in Load via applyResponseSemanticEnv when this block exists.
type SemanticCacheConfig struct {
	Enabled                 *bool             `yaml:"enabled"`
	SimilarityThreshold     float64           `yaml:"similarity_threshold"`
	TTL                     *int              `yaml:"ttl"`
	MaxConversationMessages *int              `yaml:"max_conversation_messages"`
	ExcludeSystemPrompt     bool              `yaml:"exclude_system_prompt"`
	Embedder                EmbedderConfig    `yaml:"embedder"`
	VectorStore             VectorStoreConfig `yaml:"vector_store"`
}

// EmbedderConfig selects how embeddings are generated.
// Provider must match a key in the top-level providers map when semantic
// caching is active; that provider's api_key and base_url are reused for
// POST /v1/embeddings. There is no default provider.
type EmbedderConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// VectorStoreConfig selects the vector DB backend.
// Type must be set when semantic caching is enabled: qdrant, pgvector, pinecone, weaviate.
type VectorStoreConfig struct {
	Type     string         `yaml:"type"`
	Qdrant   QdrantConfig   `yaml:"qdrant"`
	PGVector PGVectorConfig `yaml:"pgvector"`
	Pinecone PineconeConfig `yaml:"pinecone"`
	Weaviate WeaviateConfig `yaml:"weaviate"`
}

// QdrantConfig holds connection configuration for the Qdrant vector store.
type QdrantConfig struct {
	URL        string `yaml:"url"`
	Collection string `yaml:"collection"`
	APIKey     string `yaml:"api_key"`
}

// PGVectorConfig holds connection configuration for the pgvector vector store.
type PGVectorConfig struct {
	URL       string `yaml:"url"`
	Table     string `yaml:"table"`
	Dimension int    `yaml:"dimension"`
}

// PineconeConfig holds connection configuration for Pinecone (data-plane HTTP API).
type PineconeConfig struct {
	Host      string `yaml:"host"`
	APIKey    string `yaml:"api_key"`
	Namespace string `yaml:"namespace"`
	Dimension int    `yaml:"dimension"`
}

// WeaviateConfig holds connection configuration for Weaviate.
type WeaviateConfig struct {
	URL    string `yaml:"url"`
	Class  string `yaml:"class"`
	APIKey string `yaml:"api_key"`
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

	sem := c.Response.Semantic
	if sem != nil && SemanticCacheActive(sem) {
		vsType := strings.TrimSpace(sem.VectorStore.Type)
		if vsType == "" {
			return fmt.Errorf("cache.response.semantic.vector_store.type: required when semantic cache is enabled; use qdrant, pgvector, pinecone, or weaviate")
		}
		switch vsType {
		case "qdrant", "pgvector", "pinecone", "weaviate":
		default:
			return fmt.Errorf("cache.response.semantic.vector_store.type: must be one of qdrant, pgvector, pinecone, weaviate; got %q", sem.VectorStore.Type)
		}
		if vsType == "qdrant" {
			if strings.TrimSpace(sem.VectorStore.Qdrant.URL) == "" {
				return fmt.Errorf("cache.response.semantic.vector_store.qdrant.url: required when using qdrant")
			}
			if strings.TrimSpace(sem.VectorStore.Qdrant.Collection) == "" {
				return fmt.Errorf("cache.response.semantic.vector_store.qdrant.collection: required when using qdrant")
			}
		}
		if vsType == "pgvector" {
			if strings.TrimSpace(sem.VectorStore.PGVector.URL) == "" {
				return fmt.Errorf("cache.response.semantic.vector_store.pgvector.url: required when using pgvector")
			}
			if sem.VectorStore.PGVector.Dimension <= 0 {
				return fmt.Errorf("cache.response.semantic.vector_store.pgvector.dimension: must be > 0 when using pgvector")
			}
		}
		if vsType == "pinecone" {
			if strings.TrimSpace(sem.VectorStore.Pinecone.Host) == "" {
				return fmt.Errorf("cache.response.semantic.vector_store.pinecone.host: required when using pinecone")
			}
			if strings.TrimSpace(sem.VectorStore.Pinecone.APIKey) == "" {
				return fmt.Errorf("cache.response.semantic.vector_store.pinecone.api_key: required when using pinecone")
			}
			if sem.VectorStore.Pinecone.Dimension <= 0 {
				return fmt.Errorf("cache.response.semantic.vector_store.pinecone.dimension: must be > 0 when using pinecone")
			}
		}
		if vsType == "weaviate" {
			if strings.TrimSpace(sem.VectorStore.Weaviate.URL) == "" {
				return fmt.Errorf("cache.response.semantic.vector_store.weaviate.url: required when using weaviate")
			}
			if strings.TrimSpace(sem.VectorStore.Weaviate.Class) == "" {
				return fmt.Errorf("cache.response.semantic.vector_store.weaviate.class: required when using weaviate")
			}
		}
		st := sem.SimilarityThreshold
		if math.IsNaN(st) || math.IsInf(st, 0) || st <= 0 || st > 1 {
			return fmt.Errorf("cache.response.semantic.similarity_threshold: must be greater than 0 and at most 1 (yaml: similarity_threshold, env: SEMANTIC_CACHE_THRESHOLD); got %v", st)
		}
		if sem.TTL != nil && *sem.TTL < 0 {
			return fmt.Errorf("cache.response.semantic.ttl: must be >= 0 (yaml: ttl, env: SEMANTIC_CACHE_TTL); got %d", *sem.TTL)
		}
		ep := strings.TrimSpace(sem.Embedder.Provider)
		if ep == "" {
			return fmt.Errorf("cache.response.semantic.embedder.provider: required when semantic cache is enabled; use a key from the top-level providers map (e.g. openai, gemini)")
		}
		if strings.EqualFold(ep, "local") {
			return fmt.Errorf("cache.response.semantic.embedder.provider: local embedding is not supported; use a named API provider")
		}
	}
	return nil
}

// SimpleCacheEnabled reports whether the exact-match response cache layer is
// allowed to run for a non-nil simple config. Omitted enabled means true.
func SimpleCacheEnabled(s *SimpleCacheConfig) bool {
	if s == nil {
		return false
	}
	if s.Enabled != nil && !*s.Enabled {
		return false
	}
	return true
}

// SemanticCacheActive reports whether the semantic response cache should be
// validated and constructed. The semantic block must be present (YAML or
// SEMANTIC_CACHE_ENABLED=true); omitted enabled means true.
func SemanticCacheActive(sem *SemanticCacheConfig) bool {
	if sem == nil {
		return false
	}
	if sem.Enabled != nil && !*sem.Enabled {
		return false
	}
	return true
}

func mergeSemanticResponseDefaults(sem *SemanticCacheConfig) {
	if sem == nil {
		return
	}
	if sem.SimilarityThreshold == 0 {
		sem.SimilarityThreshold = 0.92
	}
	if sem.TTL == nil {
		sem.TTL = intPtr(3600)
	}
	if sem.MaxConversationMessages == nil {
		sem.MaxConversationMessages = intPtr(3)
	}
}

func intPtr(v int) *int { return &v }

func applyResponseSimpleEnv(resp *ResponseCacheConfig) error {
	v, ok := os.LookupEnv("RESPONSE_CACHE_SIMPLE_ENABLED")
	if ok && !parseBool(v) {
		resp.Simple = nil
		return nil
	}
	if resp.Simple == nil {
		if ok && parseBool(v) {
			resp.Simple = &SimpleCacheConfig{}
		} else {
			return nil
		}
	}
	simple := resp.Simple
	if ok {
		b := parseBool(v)
		simple.Enabled = &b
	}
	if u := os.Getenv("REDIS_URL"); u != "" {
		if simple.Redis == nil {
			simple.Redis = &RedisResponseConfig{}
		}
		simple.Redis.URL = u
	}
	if k := os.Getenv("REDIS_KEY_RESPONSES"); k != "" {
		if simple.Redis == nil {
			simple.Redis = &RedisResponseConfig{}
		}
		simple.Redis.Key = k
	}
	if ts := os.Getenv("REDIS_TTL_RESPONSES"); ts != "" {
		if simple.Redis == nil {
			simple.Redis = &RedisResponseConfig{}
		}
		n, err := strconv.Atoi(ts)
		if err != nil {
			return fmt.Errorf("invalid value for REDIS_TTL_RESPONSES: %q is not a valid integer", ts)
		}
		simple.Redis.TTL = n
	}
	return nil
}

func applyResponseSemanticEnv(resp *ResponseCacheConfig) error {
	v, enabledKeySet := os.LookupEnv("SEMANTIC_CACHE_ENABLED")
	if enabledKeySet && !parseBool(v) {
		resp.Semantic = nil
		return nil
	}
	if resp.Semantic == nil {
		if enabledKeySet && parseBool(v) {
			resp.Semantic = &SemanticCacheConfig{}
		} else {
			return nil
		}
	}
	sem := resp.Semantic
	if enabledKeySet {
		b := parseBool(v)
		sem.Enabled = &b
	}
	if val := os.Getenv("SEMANTIC_CACHE_THRESHOLD"); val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid value for SEMANTIC_CACHE_THRESHOLD: %q is not a valid float", val)
		}
		sem.SimilarityThreshold = f
	}
	if val := os.Getenv("SEMANTIC_CACHE_TTL"); val != "" {
		i, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid value for SEMANTIC_CACHE_TTL: %q is not a valid integer", val)
		}
		sem.TTL = &i
	}
	if val := os.Getenv("SEMANTIC_CACHE_MAX_CONV_MESSAGES"); val != "" {
		i, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid value for SEMANTIC_CACHE_MAX_CONV_MESSAGES: %q is not a valid integer", val)
		}
		sem.MaxConversationMessages = &i
	}
	if val := os.Getenv("SEMANTIC_CACHE_EXCLUDE_SYSTEM_PROMPT"); val != "" {
		sem.ExcludeSystemPrompt = parseBool(val)
	}
	if val := os.Getenv("SEMANTIC_CACHE_EMBEDDER_PROVIDER"); val != "" {
		sem.Embedder.Provider = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_EMBEDDER_MODEL"); val != "" {
		sem.Embedder.Model = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_VECTOR_STORE_TYPE"); val != "" {
		sem.VectorStore.Type = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_QDRANT_URL"); val != "" {
		sem.VectorStore.Qdrant.URL = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_QDRANT_COLLECTION"); val != "" {
		sem.VectorStore.Qdrant.Collection = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_QDRANT_API_KEY"); val != "" {
		sem.VectorStore.Qdrant.APIKey = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_PGVECTOR_URL"); val != "" {
		sem.VectorStore.PGVector.URL = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_PGVECTOR_TABLE"); val != "" {
		sem.VectorStore.PGVector.Table = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_PGVECTOR_DIMENSION"); val != "" {
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid value for SEMANTIC_CACHE_PGVECTOR_DIMENSION: %q is not a valid integer", val)
		}
		sem.VectorStore.PGVector.Dimension = n
	}
	if val := os.Getenv("SEMANTIC_CACHE_PINECONE_HOST"); val != "" {
		sem.VectorStore.Pinecone.Host = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_PINECONE_API_KEY"); val != "" {
		sem.VectorStore.Pinecone.APIKey = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_PINECONE_NAMESPACE"); val != "" {
		sem.VectorStore.Pinecone.Namespace = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_PINECONE_DIMENSION"); val != "" {
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid value for SEMANTIC_CACHE_PINECONE_DIMENSION: %q is not a valid integer", val)
		}
		sem.VectorStore.Pinecone.Dimension = n
	}
	if val := os.Getenv("SEMANTIC_CACHE_WEAVIATE_URL"); val != "" {
		sem.VectorStore.Weaviate.URL = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_WEAVIATE_CLASS"); val != "" {
		sem.VectorStore.Weaviate.Class = val
	}
	if val := os.Getenv("SEMANTIC_CACHE_WEAVIATE_API_KEY"); val != "" {
		sem.VectorStore.Weaviate.APIKey = val
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
		Fallback: FallbackConfig{
			DefaultMode: FallbackModeOff,
		},
		ExecutionPlans: ExecutionPlansConfig{
			RefreshInterval: time.Minute,
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

	if err := loadFallbackConfig(&cfg.Fallback); err != nil {
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

func loadFallbackConfig(cfg *FallbackConfig) error {
	if cfg == nil {
		return nil
	}

	cfg.DefaultMode = normalizeFallbackMode(cfg.DefaultMode)
	if cfg.DefaultMode == "" {
		cfg.DefaultMode = FallbackModeOff
	}
	if !cfg.DefaultMode.Valid() {
		return fmt.Errorf("fallback.default_mode must be one of: auto, manual, off")
	}

	if len(cfg.Overrides) > 0 {
		normalized := make(map[string]FallbackModelOverride, len(cfg.Overrides))
		for key, override := range cfg.Overrides {
			key = strings.TrimSpace(key)
			if key == "" {
				return fmt.Errorf("fallback.overrides: model key cannot be empty")
			}
			if _, exists := normalized[key]; exists {
				return fmt.Errorf("fallback.overrides: duplicate model key after trimming: %q", key)
			}
			override.Mode = normalizeFallbackMode(override.Mode)
			if override.Mode == "" {
				return fmt.Errorf("fallback.overrides[%q].mode must be one of: auto, manual, off", key)
			}
			if !override.Mode.Valid() {
				return fmt.Errorf("fallback.overrides[%q].mode must be one of: auto, manual, off", key)
			}
			normalized[key] = override
		}
		cfg.Overrides = normalized
	}

	path := strings.TrimSpace(cfg.ManualRulesPath)
	if path == "" {
		if cfg.DefaultMode == FallbackModeManual {
			return fmt.Errorf("fallback.manual_rules_path must be set when fallback.default_mode or any fallback.overrides[].mode is 'manual'")
		}
		for _, override := range cfg.Overrides {
			if override.Mode == FallbackModeManual {
				return fmt.Errorf("fallback.manual_rules_path must be set when fallback.default_mode or any fallback.overrides[].mode is 'manual'")
			}
		}
	}
	if path == "" {
		cfg.Manual = nil
		return nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("fallback.manual_rules_path: failed to read %q: %w", path, err)
	}

	expanded := expandString(string(raw))
	decoded := make(map[string][]string)
	decoder := json.NewDecoder(strings.NewReader(expanded))

	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("fallback.manual_rules_path: failed to parse %q: %w", path, err)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("fallback.manual_rules_path: failed to parse %q: top-level JSON value must be an object", path)
	}

	seenKeys := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("fallback.manual_rules_path: failed to parse %q: %w", path, err)
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("fallback.manual_rules_path: failed to parse %q: object key must be a string", path)
		}
		if _, exists := seenKeys[key]; exists {
			return fmt.Errorf("fallback.manual_rules_path: duplicate JSON key %q in %q", key, path)
		}
		seenKeys[key] = struct{}{}

		var rawModels json.RawMessage
		if err := decoder.Decode(&rawModels); err != nil {
			return fmt.Errorf("fallback.manual_rules_path: failed to parse %q: %w", path, err)
		}
		if bytes.Equal(bytes.TrimSpace(rawModels), []byte("null")) {
			return fmt.Errorf("fallback.manual_rules_path: null not allowed for %q in %q", key, path)
		}
		var models []string
		if err := json.Unmarshal(rawModels, &models); err != nil {
			return fmt.Errorf("fallback.manual_rules_path: failed to parse %q: %w", path, err)
		}
		decoded[key] = models
	}

	token, err = decoder.Token()
	if err != nil {
		return fmt.Errorf("fallback.manual_rules_path: failed to parse %q: %w", path, err)
	}
	delim, ok = token.(json.Delim)
	if !ok || delim != '}' {
		return fmt.Errorf("fallback.manual_rules_path: failed to parse %q: top-level JSON value must be an object", path)
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return fmt.Errorf("fallback.manual_rules_path: failed to parse %q: %w", path, err)
		}
		return fmt.Errorf("fallback.manual_rules_path: failed to parse %q: unexpected trailing JSON content", path)
	}

	manual := make(map[string][]string, len(decoded))
	for key, models := range decoded {
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("fallback.manual_rules_path: model key cannot be empty")
		}
		if _, exists := manual[key]; exists {
			return fmt.Errorf("fallback.manual_rules_path: duplicate manual rule key after trimming: %q", key)
		}
		normalized := make([]string, 0, len(models))
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			normalized = append(normalized, model)
		}
		manual[key] = normalized
	}
	cfg.Manual = manual
	return nil
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
			elemType := field.Type.Elem()
			if elemType.Kind() != reflect.Struct {
				continue
			}
			if fieldVal.IsNil() {
				// Only allocate if the pointed-to struct has env-tagged descendants;
				// otherwise leave it nil so optional config sections stay absent.
				if !hasEnvDescendants(elemType) {
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
