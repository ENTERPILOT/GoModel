package conversationstore

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore persists conversation snapshots in a SQL database.
type SQLStore struct {
	db          sqlx.DB
	json        jsonMutations
	ttl         time.Duration
	stopCleanup chan struct{}
	closeOnce   sync.Once
}

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS conversation_snapshots (
		id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		items ` + sqlx.TypeJSONText + ` NOT NULL DEFAULT '[]',
		stored_at ` + sqlx.TypeInt64 + ` NOT NULL,
		expires_at ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_conversation_snapshots_expires_at ON conversation_snapshots(expires_at)`,
}

// NewSQLStore creates the conversation_snapshots table if needed and starts
// the hourly expired-snapshot sweep.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	mutations, err := mutationsFor(db.Dialect())
	if err != nil {
		return nil, err
	}
	if err := db.Schema(ctx, sqlSchema...); err != nil {
		return nil, fmt.Errorf("failed to create conversation_snapshots table: %w", err)
	}

	store := &SQLStore{
		db:          db,
		json:        mutations,
		ttl:         DefaultPersistentStoreTTL,
		stopCleanup: make(chan struct{}),
	}
	go storage.RunCleanupLoop(store.stopCleanup, CleanupInterval, store.cleanup)
	return store, nil
}

// Create stores a new conversation snapshot. An existing snapshot with the
// same id is only replaced when it has already expired.
func (s *SQLStore) Create(ctx context.Context, conversation *StoredConversation) error {
	now := time.Now().UTC()
	normalized, data, items, err := prepareStoredConversationForStorage(conversation, now, s.ttl, true)
	if err != nil {
		return err
	}
	if conversationExpired(normalized, now) {
		return nil
	}
	affected, err := s.db.Exec(ctx, `
		INSERT INTO conversation_snapshots (id, data, items, stored_at, expires_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			items = excluded.items,
			stored_at = excluded.stored_at,
			expires_at = excluded.expires_at
		WHERE conversation_snapshots.expires_at > 0 AND conversation_snapshots.expires_at <= ?
	`, normalized.Conversation.ID, string(data), string(items),
		storage.UnixOrZero(normalized.StoredAt), storage.UnixOrZero(normalized.ExpiresAt), now.Unix())
	if err != nil {
		return fmt.Errorf("create conversation snapshot: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("conversation already exists: %s", normalized.Conversation.ID)
	}
	return nil
}

// Get retrieves one conversation snapshot by id.
func (s *SQLStore) Get(ctx context.Context, id string) (*StoredConversation, error) {
	return scanStoredConversationRow(s.db.QueryRow(ctx, `
		SELECT data, items, stored_at, expires_at FROM conversation_snapshots WHERE id = ?
	`, id))
}

// MergeMetadata atomically overlays metadata in the snapshot JSON while
// leaving the independently stored item array untouched.
func (s *SQLStore) MergeMetadata(ctx context.Context, id string, metadata map[string]string) (*StoredConversation, error) {
	patch, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal conversation metadata: %w", err)
	}
	query, args := s.json.mergeMetadata(id, patch, time.Now().Unix())
	affected, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("merge conversation metadata: %w", err)
	}
	if affected == 0 {
		// No row changed for one of two reasons: the conversation is gone, or
		// the merge would have exceeded the metadata pair limit.
		if _, getErr := s.Get(ctx, id); getErr != nil {
			return nil, getErr
		}
		return nil, ErrMetadataLimitExceeded
	}
	return s.Get(ctx, id)
}

// AppendItems atomically appends items to an existing, unexpired conversation,
// so two concurrently completing turns cannot overwrite each other's exchange.
func (s *SQLStore) AppendItems(ctx context.Context, id string, items []json.RawMessage) error {
	if len(items) == 0 {
		return nil
	}
	if duplicateItemID(nil, items) != "" {
		return ErrDuplicateItem
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("marshal conversation items: %w", err)
	}

	query, args := s.json.appendItems(id, items, encoded, time.Now().Unix())
	affected, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("append conversation items: %w", err)
	}
	if affected == 0 {
		// Either the conversation is gone, or one of the item ids is already
		// stored; the statement declines both.
		stored, getErr := s.Get(ctx, id)
		if getErr != nil {
			return getErr
		}
		if duplicateItemID(stored.Items, items) != "" {
			return ErrDuplicateItem
		}
		return ErrNotFound
	}
	return nil
}

// DeleteItem atomically removes the first item with the requested id.
func (s *SQLStore) DeleteItem(ctx context.Context, id, targetItemID string) (*StoredConversation, error) {
	query, args := s.json.deleteItem(id, targetItemID, time.Now().Unix())
	affected, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("delete conversation item: %w", err)
	}
	if affected == 0 {
		if _, getErr := s.Get(ctx, id); getErr != nil {
			return nil, getErr
		}
		return nil, ErrItemNotFound
	}
	return s.Get(ctx, id)
}

// Delete removes one unexpired conversation snapshot by id.
func (s *SQLStore) Delete(ctx context.Context, id string) error {
	affected, err := s.db.Exec(ctx, `
		DELETE FROM conversation_snapshots WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
	`, id, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("delete conversation snapshot: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpired removes all expired conversation snapshots.
func (s *SQLStore) DeleteExpired(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `
		DELETE FROM conversation_snapshots WHERE expires_at > 0 AND expires_at <= ?
	`, time.Now().Unix()); err != nil {
		return fmt.Errorf("delete expired conversation snapshots: %w", err)
	}
	return nil
}

func (s *SQLStore) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.DeleteExpired(ctx); err != nil {
		slog.Warn("conversation snapshot cleanup failed", "error", err)
	}
}

// Close stops the cleanup loop; connection lifecycle is managed by the
// storage layer.
func (s *SQLStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopCleanup)
	})
	return nil
}
