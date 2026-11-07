package responsecache

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/embedding"
)

// blockingEmbedder never answers before its context ends, like a degraded
// embedding provider.
type blockingEmbedder struct{ mockEmbedder }

func (b *blockingEmbedder) Embed(ctx context.Context, _ string) ([]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// blockingSearchStore never answers a search before its context ends, like a
// degraded vector store.
type blockingSearchStore struct{ *MapVecStore }

func (b blockingSearchStore) Search(ctx context.Context, _ []float32, _ string, _ int) ([]VecResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSemanticCacheMiddleware_SlowLookupBypassesCache(t *testing.T) {
	tests := []struct {
		name     string
		embedder embedding.Embedder
		store    func(*MapVecStore) VecStore
	}{
		{
			name:     "slow embedder",
			embedder: &blockingEmbedder{},
			store:    func(s *MapVecStore) VecStore { return s },
		},
		{
			name:     "slow vector search",
			embedder: &mockEmbedder{vector: []float32{1, 0, 0}},
			store:    func(s *MapVecStore) VecStore { return blockingSearchStore{s} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := NewMapVecStore()
			m := newSemanticCacheMiddleware(tt.embedder, tt.store(inner), config.SemanticCacheConfig{
				SimilarityThreshold:     0.9,
				TTL:                     new(3600),
				MaxConversationMessages: new(10),
			}, nil)
			m.lookupTimeout = 50 * time.Millisecond
			body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)

			start := time.Now()
			rec := serveSemanticRequest(t, m, body, "")
			elapsed := time.Since(start)

			assert.Less(t, elapsed, time.Second)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.JSONEq(t, `{"answer":"42"}`, rec.Body.String())
			assert.Empty(t, rec.Header().Get("X-Cache"))
			m.wg.Wait()
			assert.Equal(t, 0, inner.Len())
		})
	}
}
