package mediastore

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore keeps media records in process memory.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]*Object
}

// NewMemoryStore returns an empty in-memory record store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]*Object)}
}

// Insert stores a record; an existing id is replaced.
func (s *MemoryStore) Insert(_ context.Context, object *Object) error {
	normalized, err := normalizeObject(object)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.items[normalized.ID] = normalized
	s.mu.Unlock()
	return nil
}

// Get returns a copy of the record.
func (s *MemoryStore) Get(_ context.Context, id string) (*Object, error) {
	s.mu.RLock()
	object, ok := s.items[id]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	cloned := *object
	return &cloned, nil
}

// Delete removes the record.
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return ErrNotFound
	}
	delete(s.items, id)
	return nil
}

// Expired returns expired records, oldest expiry first.
func (s *MemoryStore) Expired(_ context.Context, now time.Time, limit int) ([]*Object, error) {
	s.mu.RLock()
	expired := make([]*Object, 0)
	for _, object := range s.items {
		if object.Expired(now) {
			cloned := *object
			expired = append(expired, &cloned)
		}
	}
	s.mu.RUnlock()
	sort.Slice(expired, func(i, j int) bool {
		if expired[i].ExpiresAt.Equal(expired[j].ExpiresAt) {
			return expired[i].ID < expired[j].ID
		}
		return expired[i].ExpiresAt.Before(expired[j].ExpiresAt)
	})
	if limit > 0 && len(expired) > limit {
		expired = expired[:limit]
	}
	return expired, nil
}

// Close releases nothing.
func (s *MemoryStore) Close() error {
	return nil
}
