package batch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores batches in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS batches (
		id TEXT PRIMARY KEY,
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		status TEXT NOT NULL,
		data TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_batches_created_at ON batches(created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_batches_status ON batches(status)`,
}

// NewSQLStore creates the batches table and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema...); err != nil {
		return nil, fmt.Errorf("failed to create batches table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Create inserts a new batch.
func (s *SQLStore) Create(ctx context.Context, batch *StoredBatch) error {
	payload, err := serializeBatch(batch)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO batches (id, created_at, updated_at, status, data)
		VALUES (?, ?, ?, ?, ?)
	`, batch.Batch.ID, batch.Batch.CreatedAt, time.Now().Unix(), batch.Batch.Status, string(payload))
	if err != nil {
		return fmt.Errorf("insert batch: %w", err)
	}
	return nil
}

// Get returns a batch by id.
func (s *SQLStore) Get(ctx context.Context, id string) (*StoredBatch, error) {
	var payload []byte
	err := s.db.QueryRow(ctx, "SELECT data FROM batches WHERE id = ?", id).Scan(&payload)
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
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
func (s *SQLStore) List(ctx context.Context, limit int, after string) ([]*StoredBatch, error) {
	limit = normalizeLimit(limit)

	var rows sqlx.Rows
	var err error
	if after == "" {
		rows, err = s.db.Query(ctx, `
			SELECT data
			FROM batches
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		`, limit)
	} else {
		var cursorCreatedAt int64
		err = s.db.QueryRow(ctx, "SELECT created_at FROM batches WHERE id = ?", after).Scan(&cursorCreatedAt)
		if err != nil {
			if errors.Is(err, sqlx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("query after cursor: %w", err)
		}

		rows, err = s.db.Query(ctx, `
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
func (s *SQLStore) Update(ctx context.Context, batch *StoredBatch) error {
	payload, err := serializeBatch(batch)
	if err != nil {
		return err
	}

	affected, err := s.db.Exec(ctx, `
		UPDATE batches
		SET updated_at = ?, status = ?, data = ?
		WHERE id = ?
	`, time.Now().Unix(), batch.Batch.Status, string(payload), batch.Batch.ID)
	if err != nil {
		return fmt.Errorf("update batch: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a stored batch object.
func (s *SQLStore) Delete(ctx context.Context, id string) error {
	affected, err := s.db.Exec(ctx, `DELETE FROM batches WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete batch: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Close is a no-op; connection lifecycle is managed by the storage layer.
func (s *SQLStore) Close() error {
	return nil
}
