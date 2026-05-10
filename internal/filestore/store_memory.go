package filestore

import (
	"context"
	"sync"
)

// MemoryStore keeps file provider mappings in process memory.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]*StoredFile
}

// NewMemoryStore creates an empty in-memory file store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]*StoredFile)}
}

// Upsert creates or replaces a file mapping.
func (s *MemoryStore) Upsert(_ context.Context, file *StoredFile) error {
	cloned, err := cloneStoredFile(file)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.items[cloned.ID]; ok {
		cloned.CreatedAt = existing.CreatedAt
	}
	s.items[cloned.ID] = cloned
	return nil
}

// Get retrieves one file mapping by id.
func (s *MemoryStore) Get(_ context.Context, id string) (*StoredFile, error) {
	s.mu.RLock()
	file, ok := s.items[id]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	return cloneStoredFile(file)
}

// Delete removes one file mapping by id.
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return ErrNotFound
	}
	delete(s.items, id)
	return nil
}

// Close releases resources.
func (s *MemoryStore) Close() error {
	return nil
}
