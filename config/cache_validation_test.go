package config

import (
	"strings"
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

func boolPtr(b bool) *bool { return &b }

func TestValidateCacheConfig_SemanticDisabledIgnoresInvalidVectorStore(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled: boolPtr(false),
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
			Semantic: &SemanticCacheConfig{
				Enabled:             boolPtr(true),
				SimilarityThreshold: 0.9,
				TTL:                 intPtr(3600),
				Embedder:            EmbedderConfig{Provider: "openai"},
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

func TestValidateCacheConfig_SemanticEnabledRequiresQdrantCollection(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled:             boolPtr(true),
				SimilarityThreshold: 0.9,
				TTL:                 intPtr(3600),
				Embedder:            EmbedderConfig{Provider: "openai"},
				VectorStore: VectorStoreConfig{
					Type:   "qdrant",
					Qdrant: QdrantConfig{URL: "http://localhost:6333"},
				},
			},
		},
	}
	if err := ValidateCacheConfig(cfg); err == nil {
		t.Fatal("expected error when qdrant collection empty")
	}
}

func TestValidateCacheConfig_SemanticSimilarityThresholdInvalid(t *testing.T) {
	base := CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled:  boolPtr(true),
				TTL:      intPtr(3600),
				Embedder: EmbedderConfig{Provider: "openai"},
				VectorStore: VectorStoreConfig{
					Type: "pgvector",
					PGVector: PGVectorConfig{
						URL:       "postgres://localhost/test",
						Table:     "gomodel_semantic_cache",
						Dimension: 1536,
					},
				},
			},
		},
	}

	for _, tc := range []struct {
		name  string
		th    float64
		want  string
	}{
		{"zero", 0, "similarity_threshold"},
		{"negative", -0.1, "similarity_threshold"},
		{"above_one", 1.01, "similarity_threshold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Response.Semantic.SimilarityThreshold = tc.th
			err := ValidateCacheConfig(&cfg)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error should mention %q: %v", tc.want, err)
			}
		})
	}
}

func TestValidateCacheConfig_SemanticRequiresEmbedderProvider(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled:             boolPtr(true),
				SimilarityThreshold: 0.9,
				TTL:                 intPtr(3600),
				VectorStore: VectorStoreConfig{
					Type: "pgvector",
					PGVector: PGVectorConfig{
						URL:       "postgres://localhost/test",
						Dimension: 768,
					},
				},
			},
		},
	}
	err := ValidateCacheConfig(cfg)
	if err == nil {
		t.Fatal("expected error when semantic enabled without embedder provider")
	}
	if !strings.Contains(err.Error(), "embedder.provider") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateCacheConfig_SemanticRejectsLocalEmbedder(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled:             boolPtr(true),
				SimilarityThreshold: 0.9,
				TTL:                 intPtr(3600),
				Embedder:            EmbedderConfig{Provider: "local"},
				VectorStore: VectorStoreConfig{
					Type: "pgvector",
					PGVector: PGVectorConfig{
						URL:       "postgres://localhost/test",
						Dimension: 768,
					},
				},
			},
		},
	}
	err := ValidateCacheConfig(cfg)
	if err == nil {
		t.Fatal("expected error for local embedder provider")
	}
}

func TestValidateCacheConfig_SemanticNegativeTTL(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled:             boolPtr(true),
				SimilarityThreshold: 0.9,
				TTL:                 intPtr(-1),
				Embedder:            EmbedderConfig{Provider: "openai"},
				VectorStore: VectorStoreConfig{
					Type: "pgvector",
					PGVector: PGVectorConfig{
						URL:       "postgres://localhost/test",
						Dimension: 768,
					},
				},
			},
		},
	}
	err := ValidateCacheConfig(cfg)
	if err == nil {
		t.Fatal("expected error for negative semantic ttl")
	}
	if !strings.Contains(err.Error(), "ttl") {
		t.Fatalf("expected ttl in error: %v", err)
	}
}
