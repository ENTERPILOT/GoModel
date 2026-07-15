package responsestore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/storage"
)

// SQLiteStore persists response snapshots in SQLite.
type SQLiteStore struct {
	db          *sql.DB
	ttl         time.Duration
	stopCleanup chan struct{}
	closeOnce   sync.Once
}

// NewSQLiteStore creates the response_snapshots table if needed and starts the
// hourly expired-snapshot sweep.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS response_snapshots (
			id TEXT PRIMARY KEY,
			data TEXT NOT NULL,
			stored_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create response_snapshots table: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_response_snapshots_expires_at ON response_snapshots(expires_at)"); err != nil {
		return nil, fmt.Errorf("failed to create response_snapshots expires index: %w", err)
	}

	store := &SQLiteStore{
		db:          db,
		ttl:         DefaultPersistentStoreTTL,
		stopCleanup: make(chan struct{}),
	}
	go storage.RunCleanupLoop(store.stopCleanup, CleanupInterval, store.cleanup)
	return store, nil
}

// Create stores a new response snapshot. An existing snapshot with the same id
// is only replaced when it has already expired.
func (s *SQLiteStore) Create(ctx context.Context, response *StoredResponse) error {
	now := time.Now().UTC()
	normalized, data, err := prepareStoredResponseForStorage(response, now, s.ttl, true)
	if err != nil {
		return err
	}
	if responseExpired(normalized, now) {
		return nil
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO response_snapshots (id, data, stored_at, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			stored_at = excluded.stored_at,
			expires_at = excluded.expires_at
		WHERE response_snapshots.expires_at > 0 AND response_snapshots.expires_at <= ?
	`, normalized.Response.ID, string(data), storage.UnixOrZero(normalized.StoredAt), storage.UnixOrZero(normalized.ExpiresAt), now.Unix())
	if err != nil {
		return fmt.Errorf("create response snapshot: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read create rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("response already exists: %s", normalized.Response.ID)
	}
	return nil
}

// Get retrieves one response snapshot by id.
func (s *SQLiteStore) Get(ctx context.Context, id string) (*StoredResponse, error) {
	return scanStoredResponseRow(s.db.QueryRowContext(ctx, `
		SELECT data, stored_at, expires_at FROM response_snapshots WHERE id = ?
	`, id), sql.ErrNoRows)
}

// Update replaces an existing, unexpired response snapshot. Zero StoredAt or
// ExpiresAt values preserve the stored retention columns.
func (s *SQLiteStore) Update(ctx context.Context, response *StoredResponse) error {
	now := time.Now().UTC()
	normalized, data, err := prepareStoredResponseForStorage(response, now, s.ttl, false)
	if err != nil {
		return err
	}
	storedAt := storage.UnixOrZero(normalized.StoredAt)
	expiresAt := storage.UnixOrZero(normalized.ExpiresAt)
	result, err := s.db.ExecContext(ctx, `
		UPDATE response_snapshots SET
			data = ?,
			stored_at = CASE WHEN ? = 0 THEN stored_at ELSE ? END,
			expires_at = CASE WHEN ? = 0 THEN expires_at ELSE ? END
		WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
	`, string(data), storedAt, storedAt, expiresAt, expiresAt, normalized.Response.ID, now.Unix())
	if err != nil {
		return fmt.Errorf("update response snapshot: %w", err)
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

// Delete removes one unexpired response snapshot by id.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM response_snapshots WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
	`, id, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("delete response snapshot: %w", err)
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

// DeleteExpired removes all expired response snapshots.
func (s *SQLiteStore) DeleteExpired(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM response_snapshots WHERE expires_at > 0 AND expires_at <= ?
	`, time.Now().Unix()); err != nil {
		return fmt.Errorf("delete expired response snapshots: %w", err)
	}
	return nil
}

func (s *SQLiteStore) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.DeleteExpired(ctx); err != nil {
		slog.Warn("response snapshot cleanup failed", "error", err)
	}
}

// Close stops the cleanup loop; DB lifecycle is managed by the storage layer.
func (s *SQLiteStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopCleanup)
	})
	return nil
}
