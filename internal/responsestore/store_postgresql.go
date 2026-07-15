package responsestore

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/enterpilot/gomodel/internal/storage"
)

// PostgreSQLStore persists response snapshots in PostgreSQL.
type PostgreSQLStore struct {
	pool        *pgxpool.Pool
	ttl         time.Duration
	stopCleanup chan struct{}
	closeOnce   sync.Once
}

// NewPostgreSQLStore creates the response_snapshots table if needed and starts
// the hourly expired-snapshot sweep.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS response_snapshots (
			id TEXT PRIMARY KEY,
			data TEXT NOT NULL,
			stored_at BIGINT NOT NULL,
			expires_at BIGINT NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create response_snapshots table: %w", err)
	}
	if _, err := pool.Exec(ctx, "CREATE INDEX IF NOT EXISTS idx_response_snapshots_expires_at ON response_snapshots(expires_at)"); err != nil {
		return nil, fmt.Errorf("failed to create response_snapshots expires index: %w", err)
	}

	store := &PostgreSQLStore{
		pool:        pool,
		ttl:         DefaultPersistentStoreTTL,
		stopCleanup: make(chan struct{}),
	}
	go storage.RunCleanupLoop(store.stopCleanup, CleanupInterval, store.cleanup)
	return store, nil
}

// Create stores a new response snapshot. An existing snapshot with the same id
// is only replaced when it has already expired.
func (s *PostgreSQLStore) Create(ctx context.Context, response *StoredResponse) error {
	now := time.Now().UTC()
	normalized, data, err := prepareStoredResponseForStorage(response, now, s.ttl, true)
	if err != nil {
		return err
	}
	if responseExpired(normalized, now) {
		return nil
	}
	cmd, err := s.pool.Exec(ctx, `
		INSERT INTO response_snapshots (id, data, stored_at, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			stored_at = excluded.stored_at,
			expires_at = excluded.expires_at
		WHERE response_snapshots.expires_at > 0 AND response_snapshots.expires_at <= $5
	`, normalized.Response.ID, string(data), storage.UnixOrZero(normalized.StoredAt), storage.UnixOrZero(normalized.ExpiresAt), now.Unix())
	if err != nil {
		return fmt.Errorf("create response snapshot: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("response already exists: %s", normalized.Response.ID)
	}
	return nil
}

// Get retrieves one response snapshot by id.
func (s *PostgreSQLStore) Get(ctx context.Context, id string) (*StoredResponse, error) {
	return scanStoredResponseRow(s.pool.QueryRow(ctx, `
		SELECT data, stored_at, expires_at FROM response_snapshots WHERE id = $1
	`, id), pgx.ErrNoRows)
}

// Update replaces an existing, unexpired response snapshot. Zero StoredAt or
// ExpiresAt values preserve the stored retention columns.
func (s *PostgreSQLStore) Update(ctx context.Context, response *StoredResponse) error {
	now := time.Now().UTC()
	normalized, data, err := prepareStoredResponseForStorage(response, now, s.ttl, false)
	if err != nil {
		return err
	}
	cmd, err := s.pool.Exec(ctx, `
		UPDATE response_snapshots SET
			data = $1,
			stored_at = CASE WHEN $2 = 0 THEN stored_at ELSE $2 END,
			expires_at = CASE WHEN $3 = 0 THEN expires_at ELSE $3 END
		WHERE id = $4 AND (expires_at = 0 OR expires_at > $5)
	`, string(data), storage.UnixOrZero(normalized.StoredAt), storage.UnixOrZero(normalized.ExpiresAt), normalized.Response.ID, now.Unix())
	if err != nil {
		return fmt.Errorf("update response snapshot: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes one unexpired response snapshot by id.
func (s *PostgreSQLStore) Delete(ctx context.Context, id string) error {
	cmd, err := s.pool.Exec(ctx, `
		DELETE FROM response_snapshots WHERE id = $1 AND (expires_at = 0 OR expires_at > $2)
	`, id, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("delete response snapshot: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpired removes all expired response snapshots.
func (s *PostgreSQLStore) DeleteExpired(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM response_snapshots WHERE expires_at > 0 AND expires_at <= $1
	`, time.Now().Unix()); err != nil {
		return fmt.Errorf("delete expired response snapshots: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.DeleteExpired(ctx); err != nil {
		slog.Warn("response snapshot cleanup failed", "error", err)
	}
}

// Close stops the cleanup loop; pool lifecycle is managed by the storage layer.
func (s *PostgreSQLStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopCleanup)
	})
	return nil
}
