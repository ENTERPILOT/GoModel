package batch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore stores batches in PostgreSQL.
type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLStore creates the batches table and indexes if needed.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS batches (
			id TEXT PRIMARY KEY,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL,
			status TEXT NOT NULL,
			data JSONB NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create batches table: %w", err)
	}

	if _, err := pool.Exec(ctx, "CREATE INDEX IF NOT EXISTS idx_batches_created_at ON batches(created_at DESC)"); err != nil {
		return nil, fmt.Errorf("failed to create batches created_at index: %w", err)
	}
	if _, err := pool.Exec(ctx, "CREATE INDEX IF NOT EXISTS idx_batches_status ON batches(status)"); err != nil {
		return nil, fmt.Errorf("failed to create batches status index: %w", err)
	}

	return &PostgreSQLStore{pool: pool}, nil
}

// Create inserts a new batch.
func (s *PostgreSQLStore) Create(ctx context.Context, batch *StoredBatch) error {
	payload, err := serializeBatch(batch)
	if err != nil {
		return err
	}

	updatedAt := time.Now().Unix()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO batches (id, created_at, updated_at, status, data)
		VALUES ($1, $2, $3, $4, $5::jsonb)
	`, batch.Batch.ID, batch.Batch.CreatedAt, updatedAt, batch.Batch.Status, payload)
	if err != nil {
		return fmt.Errorf("insert batch: %w", err)
	}
	return nil
}

// Get returns a batch by id.
func (s *PostgreSQLStore) Get(ctx context.Context, id string) (*StoredBatch, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, "SELECT data FROM batches WHERE id = $1", id).Scan(&payload)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query batch: %w", err)
	}

	batch, err := deserializeBatch(payload)
	if err != nil {
		return nil, fmt.Errorf("decode batch: %w", err)
	}
	return batch, nil
}

// List returns batches ordered by created_at desc, id desc.
func (s *PostgreSQLStore) List(ctx context.Context, limit int, after string) ([]*StoredBatch, error) {
	limit = normalizeLimit(limit)

	var rows pgx.Rows
	var err error
	if after == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT data
			FROM batches
			ORDER BY created_at DESC, id DESC
			LIMIT $1
		`, limit)
	} else {
		var cursorCreatedAt int64
		err = s.pool.QueryRow(ctx, "SELECT created_at FROM batches WHERE id = $1", after).Scan(&cursorCreatedAt)
		switch {
		case err == nil:
			rows, err = s.pool.Query(ctx, `
				SELECT data
				FROM batches
				WHERE (created_at < $1) OR (created_at = $1 AND id < $2)
				ORDER BY created_at DESC, id DESC
				LIMIT $3
			`, cursorCreatedAt, after, limit)
		case errors.Is(err, pgx.ErrNoRows):
			// Cursor may have been deleted between requests; restart pagination from newest items.
			rows, err = s.pool.Query(ctx, `
				SELECT data
				FROM batches
				ORDER BY created_at DESC, id DESC
				LIMIT $1
			`, limit)
		default:
			return nil, fmt.Errorf("query after cursor: %w", err)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("list batches: %w", err)
	}
	defer rows.Close()

	items := make([]*StoredBatch, 0, limit)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan batch row: %w", err)
		}
		batch, err := deserializeBatch(payload)
		if err != nil {
			return nil, fmt.Errorf("decode batch row: %w", err)
		}
		items = append(items, batch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate batch rows: %w", err)
	}

	return items, nil
}

// Update updates a stored batch object.
func (s *PostgreSQLStore) Update(ctx context.Context, batch *StoredBatch) error {
	payload, err := serializeBatch(batch)
	if err != nil {
		return err
	}

	updatedAt := time.Now().Unix()
	cmd, err := s.pool.Exec(ctx, `
		UPDATE batches
		SET updated_at = $1, status = $2, data = $3::jsonb
		WHERE id = $4
	`, updatedAt, batch.Batch.Status, payload, batch.Batch.ID)
	if err != nil {
		return fmt.Errorf("update batch: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Close is a no-op; pool lifecycle is managed by storage layer.
func (s *PostgreSQLStore) Close() error {
	return nil
}
