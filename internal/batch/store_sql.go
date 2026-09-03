package batch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
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

// sqlMigrations add columns to tables created by earlier releases. The
// user_path column lets scoped listings filter in the database; rows written
// before it existed are backfilled from their serialized payload on start.
var sqlMigrations = []string{
	`ALTER TABLE batches ADD COLUMN user_path TEXT`,
}

var sqlPostMigrationSchema = []string{
	`CREATE INDEX IF NOT EXISTS idx_batches_user_path ON batches(user_path)`,
}

// NewSQLStore creates the batches table and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema...); err != nil {
		return nil, fmt.Errorf("failed to create batches table: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db, sqlMigrations...); err != nil {
		return nil, fmt.Errorf("failed to migrate batches table: %w", err)
	}
	if err := db.Schema(ctx, sqlPostMigrationSchema...); err != nil {
		return nil, fmt.Errorf("failed to index batches table: %w", err)
	}
	store := &SQLStore{db: db}
	store.backfillUserPath(ctx)
	return store, nil
}

// backfillUserPathChunk bounds one backfill pass so start-up never holds a
// large table in memory or a long write lock.
const backfillUserPathChunk = 500

// backfillUserPath copies the user path out of payloads persisted before the
// column existed, one bounded chunk at a time. Rows still NULL after a
// failure are picked up on the next start, so a partial pass is safe; it is
// logged rather than fatal because scoped listings merely miss those rows
// until then, while every other operation works unchanged.
func (s *SQLStore) backfillUserPath(ctx context.Context) {
	for {
		rows, err := s.db.Query(ctx, "SELECT id, data FROM batches WHERE user_path IS NULL LIMIT ?", backfillUserPathChunk)
		if err != nil {
			slog.Warn("batch user_path backfill paused", "error", err)
			return
		}
		type legacyRow struct{ id, userPath string }
		var legacy []legacyRow
		for rows.Next() {
			var id string
			var payload []byte
			if err := rows.Scan(&id, &payload); err != nil {
				rows.Close()
				slog.Warn("batch user_path backfill paused", "error", err)
				return
			}
			userPath := ""
			if batch, err := deserializeBatch(payload); err == nil {
				userPath = batch.UserPath
			}
			legacy = append(legacy, legacyRow{id: id, userPath: userPath})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			slog.Warn("batch user_path backfill paused", "error", err)
			return
		}
		if len(legacy) == 0 {
			return
		}
		for _, row := range legacy {
			if _, err := s.db.Exec(ctx, "UPDATE batches SET user_path = ? WHERE id = ?", row.userPath, row.id); err != nil {
				slog.Warn("batch user_path backfill paused", "batch_id", row.id, "error", err)
				return
			}
		}
		if len(legacy) < backfillUserPathChunk {
			return
		}
	}
}

// Create inserts a new batch.
func (s *SQLStore) Create(ctx context.Context, batch *StoredBatch) error {
	payload, err := serializeBatch(batch)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO batches (id, created_at, updated_at, status, data, user_path)
		VALUES (?, ?, ?, ?, ?, ?)
	`, batch.Batch.ID, batch.Batch.CreatedAt, time.Now().Unix(), batch.Batch.Status, string(payload), strings.TrimSpace(batch.UserPath))
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

// List returns batches ordered by created_at desc, id desc, optionally
// confined to one user-path subtree.
func (s *SQLStore) List(ctx context.Context, limit int, after, userPath string) ([]*StoredBatch, error) {
	limit = normalizeLimit(limit)

	var conditions []string
	var args []any
	switch userPath {
	case "":
	case "/":
		// Root admits every tracked path, never the legacy rows without one.
		conditions = append(conditions, "user_path <> ''")
	default:
		conditions = append(conditions, "(user_path = ? OR user_path LIKE ? ESCAPE '\\')")
		args = append(args, userPath, sqlutil.EscapeLikeWildcards(userPath)+"/%")
	}
	if after != "" {
		var cursorCreatedAt int64
		var cursorUserPath string
		err := s.db.QueryRow(ctx, "SELECT created_at, COALESCE(user_path, '') FROM batches WHERE id = ?", after).Scan(&cursorCreatedAt, &cursorUserPath)
		if err != nil {
			if errors.Is(err, sqlx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("query after cursor: %w", err)
		}
		if userPath != "" && !core.UserPathContains(userPath, cursorUserPath) {
			// A cursor outside the subtree is indistinguishable from a missing one.
			return nil, ErrNotFound
		}
		conditions = append(conditions, "((created_at < ?) OR (created_at = ? AND id < ?))")
		args = append(args, cursorCreatedAt, cursorCreatedAt, after)
	}
	args = append(args, limit)

	rows, err := s.db.Query(ctx, `
		SELECT data
		FROM batches`+sqlutil.BuildWhereClause(conditions)+`
		ORDER BY created_at DESC, id DESC
		LIMIT ?
	`, args...)
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
		SET updated_at = ?, status = ?, data = ?, user_path = ?
		WHERE id = ?
	`, time.Now().Unix(), batch.Batch.Status, string(payload), strings.TrimSpace(batch.UserPath), batch.Batch.ID)
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
