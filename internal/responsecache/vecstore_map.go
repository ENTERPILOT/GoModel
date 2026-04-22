package responsecache

import (
	"context"
	"math"
	"sync"
	"time"
)

// MapVecStore is an in-memory VecStore for testing.
// It supports cosine-similarity search via dot-product on normalised vectors and TTL filtering.
type MapVecStore struct {
	mu      sync.Mutex
	entries []mapVecEntry
}

type mapVecEntry struct {
	key        string
	vec        []float32
	response   []byte
	paramsHash string
	expiresAt  int64
}

// NewMapVecStore creates a new in-memory VecStore.
func NewMapVecStore() *MapVecStore {
	return &MapVecStore{}
}

func (s *MapVecStore) Search(_ context.Context, vec []float32, paramsHash string, limit int) ([]VecResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	var results []VecResult
	var toDelete []int
	for i, e := range s.entries {
		if e.paramsHash != paramsHash {
			continue
		}
		if e.expiresAt > 0 && e.expiresAt < now {
			toDelete = append(toDelete, i)
			continue
		}
		score := cosineSimilarity(vec, e.vec)
		results = append(results, VecResult{Key: e.key, Score: score, Response: e.response})
	}
	if len(toDelete) > 0 {
		filtered := make([]mapVecEntry, 0, len(s.entries)-len(toDelete))
		deleteSet := make(map[int]struct{}, len(toDelete))
		for _, idx := range toDelete {
			deleteSet[idx] = struct{}{}
		}
		for i, e := range s.entries {
			if _, ok := deleteSet[i]; !ok {
				filtered = append(filtered, e)
			}
		}
		s.entries = filtered
	}
	sortVecResults(results)
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *MapVecStore) Insert(_ context.Context, key string, vec []float32, response []byte, paramsHash string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expiresAt int64
	if ttl != 0 {
		expiresAt = time.Now().Add(ttl).Unix()
	}
	s.entries = append(s.entries, mapVecEntry{
		key:        key,
		vec:        vec,
		response:   response,
		paramsHash: paramsHash,
		expiresAt:  expiresAt,
	})
	return nil
}

func (s *MapVecStore) DeleteExpired(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	filtered := make([]mapVecEntry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.expiresAt <= 0 || e.expiresAt >= now {
			filtered = append(filtered, e)
		}
	}
	s.entries = filtered
	return nil
}

func (s *MapVecStore) Close() error { return nil }

func (s *MapVecStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}

func sortVecResults(results []VecResult) {
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].Score > results[j-1].Score; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
}
