package conversationstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage"
)

// SQLiteStore persists conversation snapshots in SQLite.
type SQLiteStore struct {
	db          *sql.DB
	ttl         time.Duration
	stopCleanup chan struct{}
	closeOnce   sync.Once
}

// NewSQLiteStore creates the conversation_snapshots table if needed and starts
// the hourly expired-snapshot sweep.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS conversation_snapshots (
			id TEXT PRIMARY KEY,
			data TEXT NOT NULL,
			items TEXT NOT NULL DEFAULT '[]',
			stored_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create conversation_snapshots table: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_conversation_snapshots_expires_at ON conversation_snapshots(expires_at)"); err != nil {
		return nil, fmt.Errorf("failed to create conversation_snapshots expires index: %w", err)
	}

	store := &SQLiteStore{
		db:          db,
		ttl:         DefaultPersistentStoreTTL,
		stopCleanup: make(chan struct{}),
	}
	go storage.RunCleanupLoop(store.stopCleanup, CleanupInterval, store.cleanup)
	return store, nil
}

// Create stores a new conversation snapshot. An existing snapshot with the
// same id is only replaced when it has already expired.
func (s *SQLiteStore) Create(ctx context.Context, conversation *StoredConversation) error {
	now := time.Now().UTC()
	normalized, data, items, err := prepareStoredConversationForStorage(conversation, now, s.ttl, true)
	if err != nil {
		return err
	}
	if conversationExpired(normalized, now) {
		return nil
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO conversation_snapshots (id, data, items, stored_at, expires_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			items = excluded.items,
			stored_at = excluded.stored_at,
			expires_at = excluded.expires_at
		WHERE conversation_snapshots.expires_at > 0 AND conversation_snapshots.expires_at <= ?
	`, normalized.Conversation.ID, string(data), string(items), storage.UnixOrZero(normalized.StoredAt), storage.UnixOrZero(normalized.ExpiresAt), now.Unix())
	if err != nil {
		return fmt.Errorf("create conversation snapshot: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read create rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("conversation already exists: %s", normalized.Conversation.ID)
	}
	return nil
}

// Get retrieves one conversation snapshot by id.
func (s *SQLiteStore) Get(ctx context.Context, id string) (*StoredConversation, error) {
	return scanStoredConversationRow(s.db.QueryRowContext(ctx, `
		SELECT data, items, stored_at, expires_at FROM conversation_snapshots WHERE id = ?
	`, id), sql.ErrNoRows)
}

// Update replaces an existing, unexpired conversation snapshot including its
// items. Zero StoredAt or ExpiresAt values preserve the stored retention columns.
func (s *SQLiteStore) Update(ctx context.Context, conversation *StoredConversation) error {
	now := time.Now().UTC()
	normalized, data, items, err := prepareStoredConversationForStorage(conversation, now, s.ttl, false)
	if err != nil {
		return err
	}
	storedAt := storage.UnixOrZero(normalized.StoredAt)
	expiresAt := storage.UnixOrZero(normalized.ExpiresAt)
	result, err := s.db.ExecContext(ctx, `
		UPDATE conversation_snapshots SET
			data = ?,
			items = ?,
			stored_at = CASE WHEN ? = 0 THEN stored_at ELSE ? END,
			expires_at = CASE WHEN ? = 0 THEN expires_at ELSE ? END
		WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
	`, string(data), string(items), storedAt, storedAt, expiresAt, expiresAt, normalized.Conversation.ID, now.Unix())
	if err != nil {
		return fmt.Errorf("update conversation snapshot: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read update rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// AppendItems atomically appends items to an existing, unexpired conversation.
// The append happens in a single UPDATE via chained json_insert '$[#]' paths,
// so two concurrently completing turns cannot overwrite each other's exchange.
func (s *SQLiteStore) AppendItems(ctx context.Context, id string, items []json.RawMessage) error {
	if len(items) == 0 {
		return nil
	}

	var expr strings.Builder
	expr.WriteString("json_insert(items")
	args := make([]any, 0, len(items)+2)
	for _, item := range items {
		expr.WriteString(", '$[#]', json(?)")
		args = append(args, string(item))
	}
	expr.WriteString(")")
	args = append(args, id, time.Now().Unix())

	result, err := s.db.ExecContext(ctx,
		"UPDATE conversation_snapshots SET items = "+expr.String()+
			" WHERE id = ? AND (expires_at = 0 OR expires_at > ?)", args...)
	if err != nil {
		return fmt.Errorf("append conversation items: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read append rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes one unexpired conversation snapshot by id.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM conversation_snapshots WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
	`, id, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("delete conversation snapshot: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delete rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpired removes all expired conversation snapshots.
func (s *SQLiteStore) DeleteExpired(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM conversation_snapshots WHERE expires_at > 0 AND expires_at <= ?
	`, time.Now().Unix()); err != nil {
		return fmt.Errorf("delete expired conversation snapshots: %w", err)
	}
	return nil
}

func (s *SQLiteStore) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.DeleteExpired(ctx); err != nil {
		slog.Warn("conversation snapshot cleanup failed", "error", err)
	}
}

// Close stops the cleanup loop; DB lifecycle is managed by the storage layer.
func (s *SQLiteStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopCleanup)
	})
	return nil
}
