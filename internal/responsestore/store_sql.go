package responsestore

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore persists response snapshots in a SQL database.
type SQLStore struct {
	db          sqlx.DB
	ttl         time.Duration
	stopCleanup chan struct{}
	closeOnce   sync.Once
}

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS response_snapshots (
		id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		stored_at ` + sqlx.TypeInt64 + ` NOT NULL,
		expires_at ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_response_snapshots_expires_at ON response_snapshots(expires_at)`,
}

// NewSQLStore creates the response_snapshots table if needed and starts the
// hourly expired-snapshot sweep.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema...); err != nil {
		return nil, fmt.Errorf("failed to create response_snapshots table: %w", err)
	}

	store := &SQLStore{
		db:          db,
		ttl:         DefaultPersistentStoreTTL,
		stopCleanup: make(chan struct{}),
	}
	go storage.RunCleanupLoop(store.stopCleanup, CleanupInterval, store.cleanup)
	return store, nil
}

// Create stores a new response snapshot. An existing snapshot with the same id
// is only replaced when it has already expired.
func (s *SQLStore) Create(ctx context.Context, response *StoredResponse) error {
	now := time.Now().UTC()
	normalized, data, err := prepareStoredResponseForStorage(response, now, s.ttl, true)
	if err != nil {
		return err
	}
	if responseExpired(normalized, now) {
		return nil
	}
	return s.createRow(ctx, normalized.Response.ID, data,
		storage.UnixOrZero(normalized.StoredAt), storage.UnixOrZero(normalized.ExpiresAt), now)
}

// createRow inserts one snapshot row, replacing an existing row only when it
// has already expired.
func (s *SQLStore) createRow(ctx context.Context, id string, data []byte, storedAt, expiresAt int64, now time.Time) error {
	affected, err := s.db.Exec(ctx, `
		INSERT INTO response_snapshots (id, data, stored_at, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			stored_at = excluded.stored_at,
			expires_at = excluded.expires_at
		WHERE response_snapshots.expires_at > 0 AND response_snapshots.expires_at <= ?
	`, id, string(data), storedAt, expiresAt, now.Unix())
	if err != nil {
		return fmt.Errorf("create response snapshot: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("response already exists: %s", id)
	}
	return nil
}

// createSerialized stores an already-serialized snapshot, stamping retention
// into the columns only. Reads take StoredAt/ExpiresAt from the columns, so
// the serialized data does not need to carry them. Explicit retention values
// are preserved and zero values receive the same defaults Create applies; an
// already-expired snapshot is silently skipped, also mirroring Create.
func (s *SQLStore) createSerialized(ctx context.Context, id string, data []byte, storedAt, expiresAt time.Time) error {
	now := time.Now().UTC()
	storedAt, expiresAt = stampRetention(storedAt, expiresAt, now, s.ttl)
	if !expiresAt.IsZero() && !expiresAt.After(now) {
		return nil
	}
	return s.createRow(ctx, id, data, storedAt.Unix(), storage.UnixOrZero(expiresAt), now)
}

// updateSerialized replaces an existing, unexpired snapshot with the same
// semantics as Update: zero retention values preserve the stored columns,
// non-zero values replace them.
func (s *SQLStore) updateSerialized(ctx context.Context, id string, data []byte, storedAt, expiresAt time.Time) error {
	return s.updateRow(ctx, id, data, storage.UnixOrZero(storedAt), storage.UnixOrZero(expiresAt))
}

// updateRow rewrites one unexpired snapshot row. Zero retention values keep
// the existing column values.
func (s *SQLStore) updateRow(ctx context.Context, id string, data []byte, storedAt, expiresAt int64) error {
	affected, err := s.db.Exec(ctx, `
		UPDATE response_snapshots SET
			data = ?,
			stored_at = CASE WHEN ? = 0 THEN stored_at ELSE ? END,
			expires_at = CASE WHEN ? = 0 THEN expires_at ELSE ? END
		WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
	`, string(data), storedAt, storedAt, expiresAt, expiresAt, id, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("update response snapshot: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Get retrieves one response snapshot by id.
func (s *SQLStore) Get(ctx context.Context, id string) (*StoredResponse, error) {
	return scanStoredResponseRow(s.db.QueryRow(ctx, `
		SELECT data, stored_at, expires_at FROM response_snapshots WHERE id = ?
	`, id))
}

// Update replaces an existing, unexpired response snapshot. Zero StoredAt or
// ExpiresAt values preserve the stored retention columns.
func (s *SQLStore) Update(ctx context.Context, response *StoredResponse) error {
	now := time.Now().UTC()
	normalized, data, err := prepareStoredResponseForStorage(response, now, s.ttl, false)
	if err != nil {
		return err
	}
	return s.updateRow(ctx, normalized.Response.ID, data,
		storage.UnixOrZero(normalized.StoredAt), storage.UnixOrZero(normalized.ExpiresAt))
}

// Delete removes one unexpired response snapshot by id.
func (s *SQLStore) Delete(ctx context.Context, id string) error {
	affected, err := s.db.Exec(ctx, `
		DELETE FROM response_snapshots WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
	`, id, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("delete response snapshot: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpired removes all expired response snapshots.
func (s *SQLStore) DeleteExpired(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `
		DELETE FROM response_snapshots WHERE expires_at > 0 AND expires_at <= ?
	`, time.Now().Unix()); err != nil {
		return fmt.Errorf("delete expired response snapshots: %w", err)
	}
	return nil
}

func (s *SQLStore) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.DeleteExpired(ctx); err != nil {
		slog.Warn("response snapshot cleanup failed", "error", err)
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
