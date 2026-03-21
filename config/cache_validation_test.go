package config

import (
	"testing"
)

func TestValidateCacheConfig_BothLocalAndRedis(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
		Redis: &RedisModelConfig{URL: "redis://localhost:6379"},
		},
	}
	err := ValidateCacheConfig(cfg)
	if err == nil {
		t.Fatal("expected error when both local and redis configured")
	}
	if err.Error() != "cache.model: cannot have both local and redis configured; choose one" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateCacheConfig_NeitherLocalNorRedis(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: nil,
			Redis: nil,
		},
	}
	err := ValidateCacheConfig(cfg)
	if err == nil {
		t.Fatal("expected error when neither local nor redis configured")
	}
	if err.Error() != "cache.model: must have either local or redis configured" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateCacheConfig_RedisWithoutURL(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: nil,
			Redis: &RedisModelConfig{URL: ""},
		},
	}
	err := ValidateCacheConfig(cfg)
	if err == nil {
		t.Fatal("expected error when redis configured but URL empty")
	}
	if err.Error() != "cache.model.redis: URL is required when using redis" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateCacheConfig_LocalOnly(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
	}
	err := ValidateCacheConfig(cfg)
	if err != nil {
		t.Errorf("expected no error for valid local config: %v", err)
	}
}

func TestValidateCacheConfig_RedisOnly(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: nil,
		Redis: &RedisModelConfig{URL: "redis://localhost:6379"},
		},
	}
	err := ValidateCacheConfig(cfg)
	if err != nil {
		t.Errorf("expected no error for valid redis config: %v", err)
	}
}

func TestValidateCacheConfig_SemanticDisabledIgnoresInvalidVectorStore(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: SemanticCacheConfig{
				Enabled: false,
				VectorStore: VectorStoreConfig{
					Type: "qdrant",
					// Intentionally missing URL — valid because semantic cache is off.
				},
			},
		},
	}
	if err := ValidateCacheConfig(cfg); err != nil {
		t.Fatalf("expected no error when semantic cache disabled: %v", err)
	}
}

func TestValidateCacheConfig_SemanticEnabledRequiresQdrantURL(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: SemanticCacheConfig{
				Enabled:             true,
				SimilarityThreshold: 0.9,
				TTL:                 3600,
				VectorStore: VectorStoreConfig{
					Type: "qdrant",
				},
			},
		},
	}
	if err := ValidateCacheConfig(cfg); err == nil {
		t.Fatal("expected error when semantic enabled without qdrant URL")
	}
}
