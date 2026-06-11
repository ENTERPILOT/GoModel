package conversationstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"gomodel/internal/core"
)

const (
	// DefaultMemoryStoreTTL bounds in-memory conversation retention by age.
	// It mirrors the OpenAI Conversations retention window (~30 days).
	DefaultMemoryStoreTTL = 30 * 24 * time.Hour
	// DefaultMemoryStoreMaxEntries bounds in-memory conversation retention by count.
	DefaultMemoryStoreMaxEntries = 10000
	// DefaultMemoryStoreCleanupInterval limits full expired-entry sweeps.
	DefaultMemoryStoreCleanupInterval = time.Minute
)

// MemoryStore keeps conversation snapshots in process memory.
// Data survives across requests but not process restarts.
type MemoryStore struct {
	mu              sync.RWMutex
	items           map[string]*StoredConversation
	ttl             time.Duration
	maxEntries      int
	lastCleanup     time.Time
	cleanupInterval time.Duration
}

// MemoryStoreOption configures bounded in-memory conversation retention.
type MemoryStoreOption func(*MemoryStore)

// WithTTL expires stored conversations after ttl. Non-positive values disable TTL.
func WithTTL(ttl time.Duration) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.ttl = ttl
	}
}

// WithMaxEntries caps stored conversations with FIFO eviction. Non-positive values disable the cap.
func WithMaxEntries(maxEntries int) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.maxEntries = maxEntries
	}
}

// WithUnboundedRetention disables default in-memory retention bounds.
func WithUnboundedRetention() MemoryStoreOption {
	return func(s *MemoryStore) {
		s.ttl = 0
		s.maxEntries = 0
	}
}

// NewMemoryStore creates an empty in-memory conversation store.
// By default retention is bounded; pass WithUnboundedRetention to opt out.
func NewMemoryStore(options ...MemoryStoreOption) *MemoryStore {
	store := &MemoryStore{
		items:           make(map[string]*StoredConversation),
		ttl:             DefaultMemoryStoreTTL,
		maxEntries:      DefaultMemoryStoreMaxEntries,
		cleanupInterval: DefaultMemoryStoreCleanupInterval,
	}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	return store
}

// Create stores a new conversation snapshot.
func (s *MemoryStore) Create(_ context.Context, conversation *StoredConversation) error {
	if conversation == nil || conversation.Conversation == nil || conversation.Conversation.ID == "" {
		return fmt.Errorf("conversation id is required")
	}

	c, err := cloneConversation(conversation)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	prepareStoredConversationForMemory(c, now, s.ttl)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	if conversationExpired(c, now) {
		return nil
	}
	if existing, exists := s.items[c.Conversation.ID]; exists {
		if !conversationExpired(existing, now) {
			return fmt.Errorf("conversation already exists: %s", c.Conversation.ID)
		}
		delete(s.items, c.Conversation.ID)
	}
	s.items[c.Conversation.ID] = c
	s.enforceMaxEntriesLocked()
	return nil
}

// Get retrieves one conversation snapshot by id.
func (s *MemoryStore) Get(_ context.Context, id string) (*StoredConversation, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	conversation, ok := s.items[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	if conversationExpired(conversation, now) {
		delete(s.items, id)
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	s.mu.Unlock()
	return cloneConversation(conversation)
}

// Update replaces an existing conversation snapshot.
func (s *MemoryStore) Update(_ context.Context, conversation *StoredConversation) error {
	if conversation == nil || conversation.Conversation == nil || conversation.Conversation.ID == "" {
		return fmt.Errorf("conversation id is required")
	}
	c, err := cloneConversation(conversation)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	existing, exists := s.items[c.Conversation.ID]
	if !exists {
		return ErrNotFound
	}
	if conversationExpired(existing, now) {
		delete(s.items, c.Conversation.ID)
		return ErrNotFound
	}
	if c.StoredAt.IsZero() {
		c.StoredAt = existing.StoredAt
	}
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = existing.ExpiresAt
	}
	prepareStoredConversationForMemory(c, now, s.ttl)
	if conversationExpired(c, now) {
		delete(s.items, c.Conversation.ID)
		return ErrNotFound
	}
	s.items[c.Conversation.ID] = c
	s.enforceMaxEntriesLocked()
	return nil
}

// AppendItems atomically appends items to an existing conversation snapshot.
func (s *MemoryStore) AppendItems(_ context.Context, id string, items []json.RawMessage) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	conversation, exists := s.items[id]
	if !exists {
		return ErrNotFound
	}
	if conversationExpired(conversation, now) {
		delete(s.items, id)
		return ErrNotFound
	}
	for _, item := range items {
		conversation.Items = append(conversation.Items, core.CloneRawJSON(item))
	}
	return nil
}

// Delete removes one conversation snapshot by id.
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.cleanupExpiredLocked(now)
	conversation, exists := s.items[id]
	if !exists {
		return ErrNotFound
	}
	// Expired entries report as not found, matching Get and Update, even when
	// the throttled cleanup sweep has not removed them yet.
	if conversationExpired(conversation, now) {
		delete(s.items, id)
		return ErrNotFound
	}
	delete(s.items, id)
	return nil
}

// Close releases resources (no-op for memory store).
func (s *MemoryStore) Close() error {
	return nil
}

func prepareStoredConversationForMemory(conversation *StoredConversation, now time.Time, ttl time.Duration) {
	if conversation.StoredAt.IsZero() {
		conversation.StoredAt = now
	}
	if ttl > 0 && conversation.ExpiresAt.IsZero() {
		conversation.ExpiresAt = conversation.StoredAt.Add(ttl)
	}
}

func (s *MemoryStore) cleanupExpiredLocked(now time.Time) {
	if s.ttl <= 0 {
		return
	}
	if s.cleanupInterval > 0 && !s.lastCleanup.IsZero() && now.Sub(s.lastCleanup) < s.cleanupInterval {
		return
	}
	s.lastCleanup = now
	for id, conversation := range s.items {
		if conversationExpired(conversation, now) {
			delete(s.items, id)
		}
	}
}

func (s *MemoryStore) enforceMaxEntriesLocked() {
	if s.maxEntries <= 0 {
		return
	}
	overLimit := len(s.items) - s.maxEntries
	if overLimit <= 0 {
		return
	}

	entries := make([]memoryStoreEntry, 0, len(s.items))
	for id, conversation := range s.items {
		entries = append(entries, memoryStoreEntry{
			id:       id,
			storedAt: conversationStoredAt(conversation),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].storedAt.Equal(entries[j].storedAt) {
			return entries[i].id < entries[j].id
		}
		return entries[i].storedAt.Before(entries[j].storedAt)
	})
	for i := 0; i < overLimit && i < len(entries); i++ {
		delete(s.items, entries[i].id)
	}
}

type memoryStoreEntry struct {
	id       string
	storedAt time.Time
}

func conversationExpired(conversation *StoredConversation, now time.Time) bool {
	return conversation != nil && !conversation.ExpiresAt.IsZero() && !conversation.ExpiresAt.After(now)
}

func conversationStoredAt(conversation *StoredConversation) time.Time {
	if conversation == nil || conversation.StoredAt.IsZero() {
		return time.Time{}
	}
	return conversation.StoredAt
}
