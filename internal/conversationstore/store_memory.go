package conversationstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	// DefaultMemoryStoreTTL bounds in-memory conversation retention by age.
	// It mirrors the OpenAI Conversations retention window (~30 days).
	DefaultMemoryStoreTTL = 30 * 24 * time.Hour
	// DefaultMemoryStoreMaxEntries bounds in-memory conversation retention by count.
	DefaultMemoryStoreMaxEntries = 10000
	// DefaultMemoryStoreMaxBytes bounds in-memory conversation retention by
	// total serialized size. Conversations grow per turn without bound, so
	// entry counts alone do not bound memory.
	DefaultMemoryStoreMaxBytes = 64 << 20
	// DefaultMemoryStoreCleanupInterval limits full expired-entry sweeps.
	DefaultMemoryStoreCleanupInterval = time.Minute
)

// MemoryStore keeps conversation snapshots in process memory.
// Data survives across requests but not process restarts.
type MemoryStore struct {
	mu              sync.RWMutex
	items           map[string]*StoredConversation
	sizes           map[string]int64
	totalBytes      int64
	ttl             time.Duration
	maxEntries      int
	maxBytes        int64
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

// WithMaxBytes caps the total serialized size of stored conversations with
// FIFO eviction. Non-positive values disable the cap.
func WithMaxBytes(maxBytes int64) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.maxBytes = maxBytes
	}
}

// NewMemoryStore creates an empty in-memory conversation store.
// Retention is bounded by default; options can adjust or disable the bounds.
func NewMemoryStore(options ...MemoryStoreOption) *MemoryStore {
	store := &MemoryStore{
		items:           make(map[string]*StoredConversation),
		sizes:           make(map[string]int64),
		ttl:             DefaultMemoryStoreTTL,
		maxEntries:      DefaultMemoryStoreMaxEntries,
		maxBytes:        DefaultMemoryStoreMaxBytes,
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

	c, size, err := cloneConversationWithSize(conversation)
	if err != nil {
		return err
	}
	if err := s.checkByteBudget(size); err != nil {
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
		s.removeLocked(c.Conversation.ID)
	}
	s.putLocked(c.Conversation.ID, c, size)
	s.enforceBoundsLocked(c.Conversation.ID)
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
		s.removeLocked(id)
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
	c, size, err := cloneConversationWithSize(conversation)
	if err != nil {
		return err
	}
	if err := s.checkByteBudget(size); err != nil {
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
		s.removeLocked(c.Conversation.ID)
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
		s.removeLocked(c.Conversation.ID)
		return ErrNotFound
	}
	s.putLocked(c.Conversation.ID, c, size)
	s.enforceBoundsLocked(c.Conversation.ID)
	return nil
}

// AppendItems atomically appends items to an existing conversation snapshot.
func (s *MemoryStore) AppendItems(_ context.Context, id string, items []json.RawMessage) error {
	if len(items) == 0 {
		return nil
	}
	var added int64
	for _, item := range items {
		added += int64(len(item))
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
		s.removeLocked(id)
		return ErrNotFound
	}
	// Reject growth past the byte budget before mutating, mirroring Create and
	// Update; otherwise bound enforcement would have to drop the very
	// conversation the caller believes was just persisted.
	if s.maxBytes > 0 && s.sizes[id]+added > s.maxBytes {
		return fmt.Errorf("conversation snapshot would grow to %d bytes, exceeding the in-memory store budget of %d bytes", s.sizes[id]+added, s.maxBytes)
	}
	for _, item := range items {
		conversation.Items = append(conversation.Items, core.CloneRawJSON(item))
	}
	s.sizes[id] += added
	s.totalBytes += added
	s.enforceBoundsLocked(id)
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
		s.removeLocked(id)
		return ErrNotFound
	}
	s.removeLocked(id)
	return nil
}

// Close releases resources (no-op for memory store).
func (s *MemoryStore) Close() error {
	return nil
}

// checkByteBudget rejects snapshots that could never fit the byte budget, so
// callers see an explicit storage failure instead of silent eviction churn.
func (s *MemoryStore) checkByteBudget(size int64) error {
	if s.maxBytes > 0 && size > s.maxBytes {
		return fmt.Errorf("conversation snapshot of %d bytes exceeds the in-memory store budget of %d bytes", size, s.maxBytes)
	}
	return nil
}

func (s *MemoryStore) putLocked(id string, conversation *StoredConversation, size int64) {
	s.removeLocked(id)
	s.items[id] = conversation
	s.sizes[id] = size
	s.totalBytes += size
}

func (s *MemoryStore) removeLocked(id string) {
	if _, ok := s.items[id]; !ok {
		return
	}
	delete(s.items, id)
	s.totalBytes -= s.sizes[id]
	delete(s.sizes, id)
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
			s.removeLocked(id)
		}
	}
}

// enforceBoundsLocked evicts oldest-first until both the entry-count and
// byte-budget caps hold. The protect id — the entry the caller just wrote,
// which the byte-budget checks guarantee fits on its own — is never evicted,
// so a successful write cannot be silently undone by its own bound enforcement.
func (s *MemoryStore) enforceBoundsLocked(protect string) {
	overEntries := s.maxEntries > 0 && len(s.items) > s.maxEntries
	overBytes := s.maxBytes > 0 && s.totalBytes > s.maxBytes
	if !overEntries && !overBytes {
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
	for _, entry := range entries {
		if (s.maxEntries <= 0 || len(s.items) <= s.maxEntries) &&
			(s.maxBytes <= 0 || s.totalBytes <= s.maxBytes) {
			return
		}
		if entry.id == protect {
			continue
		}
		s.removeLocked(entry.id)
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
