package batch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SQLiteStore stores batches in SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates the batches table and indexes if needed.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS batches (
			id TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			status TEXT NOT NULL,
			data TEXT NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create batches table: %w", err)
	}

	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_batches_created_at ON batches(created_at DESC)"); err != nil {
		return nil, fmt.Errorf("failed to create batches created_at index: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_batches_status ON batches(status)"); err != nil {
		return nil, fmt.Errorf("failed to create batches status index: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

// Create inserts a new batch.
func (s *SQLiteStore) Create(ctx context.Context, batch *StoredBatch) error {
	payload, err := serializeBatch(batch)
	if err != nil {
		return err
	}

	updatedAt := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO batches (id, created_at, updated_at, status, data)
		VALUES (?, ?, ?, ?, ?)
	`, batch.Batch.ID, batch.Batch.CreatedAt, updatedAt, batch.Batch.Status, string(payload))
	if err != nil {
		return fmt.Errorf("insert batch: %w", err)
	}
	return nil
}

// Get returns a batch by id.
func (s *SQLiteStore) Get(ctx context.Context, id string) (*StoredBatch, error) {
	var payload string
	err := s.db.QueryRowContext(ctx, "SELECT data FROM batches WHERE id = ?", id).Scan(&payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query batch: %w", err)
	}

	batch, err := deserializeBatch([]byte(payload))
	if err != nil {
		return nil, fmt.Errorf("decode batch: %w", err)
	}
	return batch, nil
}

// List returns batches ordered by created_at desc, id desc.
func (s *SQLiteStore) List(ctx context.Context, limit int, after string) ([]*StoredBatch, error) {
	limit = normalizeLimit(limit)

	var rows *sql.Rows
	var err error
	if after == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT data
			FROM batches
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		`, limit)
	} else {
		var cursorCreatedAt int64
		err = s.db.QueryRowContext(ctx, "SELECT created_at FROM batches WHERE id = ?", after).Scan(&cursorCreatedAt)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("query after cursor: %w", err)
		}

		rows, err = s.db.QueryContext(ctx, `
			SELECT data
			FROM batches
			WHERE (created_at < ?) OR (created_at = ? AND id < ?)
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		`, cursorCreatedAt, cursorCreatedAt, after, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list batches: %w", err)
	}
	defer rows.Close()

	items := make([]*StoredBatch, 0, limit)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan batch row: %w", err)
		}
		batch, err := deserializeBatch([]byte(payload))
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
func (s *SQLiteStore) Update(ctx context.Context, batch *StoredBatch) error {
	payload, err := serializeBatch(batch)
	if err != nil {
		return err
	}

	updatedAt := time.Now().Unix()
	result, err := s.db.ExecContext(ctx, `
		UPDATE batches
		SET updated_at = ?, status = ?, data = ?
		WHERE id = ?
	`, updatedAt, batch.Batch.Status, string(payload), batch.Batch.ID)
	if err != nil {
		return fmt.Errorf("update batch: %w", err)
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

// Close is a no-op; DB lifecycle is managed by storage layer.
func (s *SQLiteStore) Close() error {
	return nil
}
