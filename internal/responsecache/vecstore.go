package responsecache

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gomodel/config"
)

// VecResult holds a single semantic cache lookup result.
type VecResult struct {
	Key      string
	Score    float32
	Response []byte
}

// VecStore is the generic interface for all vector database backends.
// Each backend must implement similarity search, insertion with TTL metadata,
// and bulk expired-entry deletion for background cleanup.
type VecStore interface {
	// Search returns up to limit results whose params_hash matches and whose
	// similarity score to vec is above the caller's threshold. Expired entries
	// encountered during search are deleted before returning a miss.
	Search(ctx context.Context, vec []float32, paramsHash string, limit int) ([]VecResult, error)

	// Insert stores vec along with its response bytes and TTL metadata.
	// expires_at is recorded as unix-seconds; Search and DeleteExpired filter on it.
	Insert(ctx context.Context, key string, vec []float32, response []byte, paramsHash string, ttl time.Duration) error

	// DeleteExpired removes all entries whose expires_at is in the past.
	// Called periodically by a background goroutine started in NewVecStore.
	DeleteExpired(ctx context.Context) error

	Close() error
}

// NewVecStore creates a VecStore for the backend selected by cfg.Type.
// Type must be non-empty and one of: qdrant, pgvector, pinecone, weaviate.
func NewVecStore(cfg config.VectorStoreConfig) (VecStore, error) {
	t := strings.TrimSpace(cfg.Type)
	if t == "" {
		return nil, fmt.Errorf("vecstore: vector_store.type is required (qdrant, pgvector, pinecone, weaviate)")
	}
	switch t {
	case "qdrant":
		return newQdrantStore(cfg.Qdrant)
	case "pgvector":
		return newPGVectorStore(cfg.PGVector)
	case "pinecone":
		return newPineconeStore(cfg.Pinecone)
	case "weaviate":
		return newWeaviateStore(cfg.Weaviate)
	default:
		return nil, fmt.Errorf("vecstore: unknown backend type %q (valid: qdrant, pgvector, pinecone, weaviate)", t)
	}
}
