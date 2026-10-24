package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCacheConfig_BothLocalAndRedis(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: &RedisModelConfig{URL: "redis://localhost:6379"},
		},
	}
	err := ValidateCacheConfig(cfg)
	require.NoError(t, err)
}

func TestValidateCacheConfig_NeitherLocalNorRedis(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: nil,
			Redis: nil,
		},
	}
	err := ValidateCacheConfig(cfg)
	require.Error(t, err)
	assert.Equal(t, "cache.model: must have either local or redis configured", err.Error())
}

func TestValidateCacheConfig_RedisWithoutURL(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: nil,
			Redis: &RedisModelConfig{URL: ""},
		},
	}
	err := ValidateCacheConfig(cfg)
	require.Error(t, err)
	assert.Equal(t, "cache.model.redis: URL is required when using redis", err.Error())
}

func TestValidateCacheConfig_LocalOnly(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
	}
	err := ValidateCacheConfig(cfg)
	assert.NoError(t, err)
}

func TestValidateCacheConfig_RedisOnly(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: nil,
			Redis: &RedisModelConfig{URL: "redis://localhost:6379"},
		},
	}
	err := ValidateCacheConfig(cfg)
	assert.NoError(t, err)
}

func TestValidateCacheConfig_SemanticDisabledIgnoresInvalidVectorStore(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled: new(false),
				VectorStore: VectorStoreConfig{
					Type: "qdrant",
					// Intentionally missing URL — valid because semantic cache is off.
				},
			},
		},
	}
	err := ValidateCacheConfig(cfg)
	require.NoError(t, err)
}

func TestValidateCacheConfig_SemanticEnabledRequiresQdrantURL(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled:             new(true),
				SimilarityThreshold: 0.9,
				TTL:                 new(3600),
				Embedder:            EmbedderConfig{Provider: "openai"},
				VectorStore: VectorStoreConfig{
					Type: "qdrant",
				},
			},
		},
	}
	require.Error(t, ValidateCacheConfig(cfg))
}

func TestValidateCacheConfig_SemanticEnabledRequiresQdrantCollection(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled:             new(true),
				SimilarityThreshold: 0.9,
				TTL:                 new(3600),
				Embedder:            EmbedderConfig{Provider: "openai"},
				VectorStore: VectorStoreConfig{
					Type:   "qdrant",
					Qdrant: QdrantConfig{URL: "http://localhost:6333"},
				},
			},
		},
	}
	require.Error(t, ValidateCacheConfig(cfg))
}

func TestValidateCacheConfig_SemanticSimilarityThresholdInvalid(t *testing.T) {
	base := CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled:  new(true),
				TTL:      new(3600),
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
		name string
		th   float64
		want string
	}{
		{"zero", 0, "similarity_threshold"},
		{"negative", -0.1, "similarity_threshold"},
		{"above_one", 1.01, "similarity_threshold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Response.Semantic.SimilarityThreshold = tc.th
			err := ValidateCacheConfig(&cfg)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
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
				Enabled:             new(true),
				SimilarityThreshold: 0.9,
				TTL:                 new(3600),
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
	require.Error(t, err)
	require.Contains(t, err.Error(), "embedder.provider")
}

func TestValidateCacheConfig_SemanticRejectsLocalEmbedder(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled:             new(true),
				SimilarityThreshold: 0.9,
				TTL:                 new(3600),
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
	require.Error(t, err)
}

func TestValidateCacheConfig_SemanticNegativeTTL(t *testing.T) {
	cfg := &CacheConfig{
		Model: ModelCacheConfig{
			Local: &LocalCacheConfig{CacheDir: ".cache"},
			Redis: nil,
		},
		Response: ResponseCacheConfig{
			Semantic: &SemanticCacheConfig{
				Enabled:             new(true),
				SimilarityThreshold: 0.9,
				TTL:                 new(-1),
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
	require.Error(t, err)
	require.Contains(t, err.Error(), "ttl")
}
