package batch

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore keeps batches in process memory.
// Data survives across requests but not process restarts.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]*StoredBatch
	// usageClaims holds when each pending usage claim was taken.
	usageClaims map[string]time.Time
}

// NewMemoryStore creates an empty in-memory batch store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		items:       make(map[string]*StoredBatch),
		usageClaims: make(map[string]time.Time),
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
func (s *MemoryStore) List(_ context.Context, limit int, after, userPath string) ([]*StoredBatch, error) {
	limit = normalizeLimit(limit)

	s.mu.RLock()
	all := make([]*StoredBatch, 0, len(s.items))
	for _, b := range s.items {
		if !batchInUserPath(userPath, b) {
			continue
		}
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
	end := min(start+limit, len(all))
	return all[start:end], nil
}

// Update replaces an existing batch object, keeping its usage marker.
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
	existing, exists := s.items[c.Batch.ID]
	if !exists {
		return ErrNotFound
	}
	if c.UsageLoggedAt == nil {
		c.UsageLoggedAt = existing.UsageLoggedAt
	}
	s.items[c.Batch.ID] = c
	return nil
}

// ClaimUsage grants the batch's usage claim unless its usage is logged or a
// claim younger than timeout is held.
func (s *MemoryStore) ClaimUsage(_ context.Context, id string, now time.Time, timeout time.Duration) (UsageClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.items[id]
	if !ok {
		return UsagePending, ErrNotFound
	}
	if existing.UsageLoggedAt != nil {
		return UsageLogged, nil
	}
	if claimedAt, held := s.usageClaims[id]; held && now.Sub(claimedAt) < timeout {
		return UsagePending, nil
	}
	s.usageClaims[id] = now
	return UsageClaimed, nil
}

// CompleteUsage marks the batch's usage as logged and releases the claim.
func (s *MemoryStore) CompleteUsage(_ context.Context, id string, loggedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	// Stored batches are read outside the lock, so the marker goes on a copy.
	logged := *existing
	loggedAt = loggedAt.UTC()
	logged.UsageLoggedAt = &loggedAt
	s.items[id] = &logged
	delete(s.usageClaims, id)
	return nil
}

// Delete removes a stored batch object.
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[id]; !exists {
		return ErrNotFound
	}
	delete(s.items, id)
	delete(s.usageClaims, id)
	return nil
}

// Close releases resources (no-op for memory store).
func (s *MemoryStore) Close() error {
	return nil
}
