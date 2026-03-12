package batch

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// MemoryStore keeps batches in process memory.
// Data survives across requests but not process restarts.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]*StoredBatch
}

// NewMemoryStore creates an empty in-memory batch store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		items: make(map[string]*StoredBatch),
	}
}

// Create stores a new batch.
func (s *MemoryStore) Create(_ context.Context, batch *StoredBatch) error {
	if batch == nil || batch.Batch == nil || batch.Batch.ID == "" {
		return fmt.Errorf("batch id is required")
	}

	c, err := cloneBatch(batch)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[c.Batch.ID]; exists {
		return fmt.Errorf("batch already exists: %s", c.Batch.ID)
	}
	s.items[c.Batch.ID] = c
	return nil
}

// Get retrieves one batch by id.
func (s *MemoryStore) Get(_ context.Context, id string) (*StoredBatch, error) {
	s.mu.RLock()
	b, ok := s.items[id]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	return cloneBatch(b)
}

// List returns batches ordered by created_at desc, id desc.
func (s *MemoryStore) List(_ context.Context, limit int, after string) ([]*StoredBatch, error) {
	limit = normalizeLimit(limit)

	s.mu.RLock()
	all := make([]*StoredBatch, 0, len(s.items))
	for _, b := range s.items {
		c, err := cloneBatch(b)
		if err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		all = append(all, c)
	}
	s.mu.RUnlock()

	sort.Slice(all, func(i, j int) bool {
		if all[i].Batch.CreatedAt == all[j].Batch.CreatedAt {
			return all[i].Batch.ID > all[j].Batch.ID
		}
		return all[i].Batch.CreatedAt > all[j].Batch.CreatedAt
	})

	start := 0
	if after != "" {
		idx := -1
		for i := range all {
			if all[i].Batch.ID == after {
				idx = i
				break
			}
		}
		if idx == -1 {
			return nil, ErrNotFound
		}
		start = idx + 1
	}

	if start >= len(all) {
		return []*StoredBatch{}, nil
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], nil
}

// Update replaces an existing batch object.
func (s *MemoryStore) Update(_ context.Context, batch *StoredBatch) error {
	if batch == nil || batch.Batch == nil || batch.Batch.ID == "" {
		return fmt.Errorf("batch id is required")
	}
	c, err := cloneBatch(batch)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[c.Batch.ID]; !exists {
		return ErrNotFound
	}
	s.items[c.Batch.ID] = c
	return nil
}

// Close releases resources (no-op for memory store).
func (s *MemoryStore) Close() error {
	return nil
}
