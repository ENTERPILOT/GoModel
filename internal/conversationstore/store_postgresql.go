package conversationstore

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage"
)

// PostgreSQLStore persists conversation snapshots in PostgreSQL.
type PostgreSQLStore struct {
	pool        *pgxpool.Pool
	ttl         time.Duration
	stopCleanup chan struct{}
	closeOnce   sync.Once
}

// NewPostgreSQLStore creates the conversation_snapshots table if needed and
// starts the hourly expired-snapshot sweep.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS conversation_snapshots (
			id TEXT PRIMARY KEY,
			data TEXT NOT NULL,
			items JSONB NOT NULL DEFAULT '[]'::jsonb,
			stored_at BIGINT NOT NULL,
			expires_at BIGINT NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create conversation_snapshots table: %w", err)
	}
	if _, err := pool.Exec(ctx, "CREATE INDEX IF NOT EXISTS idx_conversation_snapshots_expires_at ON conversation_snapshots(expires_at)"); err != nil {
		return nil, fmt.Errorf("failed to create conversation_snapshots expires index: %w", err)
	}

	store := &PostgreSQLStore{
		pool:        pool,
		ttl:         DefaultPersistentStoreTTL,
		stopCleanup: make(chan struct{}),
	}
	go storage.RunCleanupLoop(store.stopCleanup, CleanupInterval, store.cleanup)
	return store, nil
}

// Create stores a new conversation snapshot. An existing snapshot with the
// same id is only replaced when it has already expired.
func (s *PostgreSQLStore) Create(ctx context.Context, conversation *StoredConversation) error {
	now := time.Now().UTC()
	normalized, data, items, err := prepareStoredConversationForStorage(conversation, now, s.ttl, true)
	if err != nil {
		return err
	}
	if conversationExpired(normalized, now) {
		return nil
	}
	cmd, err := s.pool.Exec(ctx, `
		INSERT INTO conversation_snapshots (id, data, items, stored_at, expires_at)
		VALUES ($1, $2, $3::jsonb, $4, $5)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			items = excluded.items,
			stored_at = excluded.stored_at,
			expires_at = excluded.expires_at
		WHERE conversation_snapshots.expires_at > 0 AND conversation_snapshots.expires_at <= $6
	`, normalized.Conversation.ID, string(data), string(items), storage.UnixOrZero(normalized.StoredAt), storage.UnixOrZero(normalized.ExpiresAt), now.Unix())
	if err != nil {
		return fmt.Errorf("create conversation snapshot: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("conversation already exists: %s", normalized.Conversation.ID)
	}
	return nil
}

// Get retrieves one conversation snapshot by id.
func (s *PostgreSQLStore) Get(ctx context.Context, id string) (*StoredConversation, error) {
	return scanStoredConversationRow(s.pool.QueryRow(ctx, `
		SELECT data, items::text, stored_at, expires_at FROM conversation_snapshots WHERE id = $1
	`, id), pgx.ErrNoRows)
}

// MergeMetadata overlays metadata inside the snapshot JSON without touching
// the item array, which is updated independently by Responses turns.
func (s *PostgreSQLStore) MergeMetadata(ctx context.Context, id string, metadata map[string]string) (*StoredConversation, error) {
	patch, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal conversation metadata: %w", err)
	}
	cmd, err := s.pool.Exec(ctx, `
		UPDATE conversation_snapshots SET data = jsonb_set(
			data::jsonb,
			'{conversation,metadata}',
			COALESCE(data::jsonb #> '{conversation,metadata}', '{}'::jsonb) || $2::jsonb
		)::text
		WHERE id = $1 AND (expires_at = 0 OR expires_at > $3)
		AND (
			SELECT COUNT(*)
			FROM jsonb_object_keys(
				COALESCE(data::jsonb #> '{conversation,metadata}', '{}'::jsonb) || $2::jsonb
			)
		) <= $4
	`, id, string(patch), time.Now().Unix(), core.MaxConversationMetadataPairs)
	if err != nil {
		return nil, fmt.Errorf("merge conversation metadata: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		if _, getErr := s.Get(ctx, id); getErr != nil {
			return nil, getErr
		}
		return nil, ErrMetadataLimitExceeded
	}
	return s.Get(ctx, id)
}

// AppendItems atomically appends items to an existing, unexpired conversation
// using jsonb array concatenation, so two concurrently completing turns cannot
// overwrite each other's exchange.
func (s *PostgreSQLStore) AppendItems(ctx context.Context, id string, items []json.RawMessage) error {
	if len(items) == 0 {
		return nil
	}
	if duplicateItemID(nil, items) != "" {
		return ErrDuplicateItem
	}
	appended, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("marshal conversation items: %w", err)
	}
	cmd, err := s.pool.Exec(ctx, `
		UPDATE conversation_snapshots SET items = items || $2::jsonb
		WHERE id = $1 AND (expires_at = 0 OR expires_at > $3)
		AND NOT EXISTS (
			SELECT 1
			FROM jsonb_array_elements(items) AS existing(value)
			JOIN jsonb_array_elements($2::jsonb) AS added(value)
			  ON existing.value ->> 'id' = added.value ->> 'id'
		)
	`, id, string(appended), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("append conversation items: %w", err)
	}
	if cmd.RowsAffected() == 0 {
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

// DeleteItem atomically removes the first matching item from the JSONB array.
func (s *PostgreSQLStore) DeleteItem(ctx context.Context, id, targetItemID string) (*StoredConversation, error) {
	cmd, err := s.pool.Exec(ctx, `
		WITH target AS (
			SELECT c.id, element.ordinality - 1 AS item_index
			FROM conversation_snapshots c
			CROSS JOIN LATERAL jsonb_array_elements(c.items) WITH ORDINALITY AS element(value, ordinality)
			WHERE c.id = $1
			  AND (c.expires_at = 0 OR c.expires_at > $3)
			  AND element.value ->> 'id' = $2
			ORDER BY element.ordinality
			LIMIT 1
		)
		UPDATE conversation_snapshots c
		SET items = c.items - target.item_index::int
		FROM target
		WHERE c.id = target.id
	`, id, targetItemID, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("delete conversation item: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		if _, getErr := s.Get(ctx, id); getErr != nil {
			return nil, getErr
		}
		return nil, ErrItemNotFound
	}
	return s.Get(ctx, id)
}

// Delete removes one unexpired conversation snapshot by id.
func (s *PostgreSQLStore) Delete(ctx context.Context, id string) error {
	cmd, err := s.pool.Exec(ctx, `
		DELETE FROM conversation_snapshots WHERE id = $1 AND (expires_at = 0 OR expires_at > $2)
	`, id, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("delete conversation snapshot: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpired removes all expired conversation snapshots.
func (s *PostgreSQLStore) DeleteExpired(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM conversation_snapshots WHERE expires_at > 0 AND expires_at <= $1
	`, time.Now().Unix()); err != nil {
		return fmt.Errorf("delete expired conversation snapshots: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.DeleteExpired(ctx); err != nil {
		slog.Warn("conversation snapshot cleanup failed", "error", err)
	}
}

// Close stops the cleanup loop; pool lifecycle is managed by the storage layer.
func (s *PostgreSQLStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopCleanup)
	})
	return nil
}
