package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// CacheConfig holds model and response cache configuration.
type CacheConfig struct {
	Model    ModelCacheConfig    `yaml:"model"`
	Response ResponseCacheConfig `yaml:"response"`
}

// ModelCacheConfig holds cache configuration for model registry.
// Exactly one of Local or Redis must be non-nil.
type ModelCacheConfig struct {
	RefreshInterval int `yaml:"refresh_interval" env:"CACHE_REFRESH_INTERVAL"`
	// RecheckInterval is how often (seconds) providers whose latest refresh
	// failed are re-checked, so outage recovery is detected without waiting
	// for the next full refresh. Zero or negative disables the fast recheck.
	RecheckInterval int               `yaml:"recheck_interval" env:"PROVIDER_RECHECK_INTERVAL"`
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
		sem.TTL = new(3600)
	}
	if sem.MaxConversationMessages == nil {
		sem.MaxConversationMessages = new(3)
	}
}

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
