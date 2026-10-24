package mediastore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore keeps media records in SQLite or PostgreSQL.
type SQLStore struct {
	db sqlx.DB
}

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS media_objects (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		source TEXT NOT NULL,
		content_type TEXT NOT NULL DEFAULT '',
		bytes ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		storage_key TEXT NOT NULL,
		request_id TEXT NOT NULL DEFAULT '',
		user_path TEXT NOT NULL DEFAULT '',
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		expires_at ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_media_objects_expires_at ON media_objects(expires_at)`,
	`CREATE INDEX IF NOT EXISTS idx_media_objects_request_id ON media_objects(request_id)`,
}

const sqlObjectColumns = `id, kind, source, content_type, bytes, storage_key, request_id, user_path, created_at, expires_at`

// NewSQLStore creates the media_objects table and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema...); err != nil {
		return nil, fmt.Errorf("failed to create media_objects table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Insert stores a record; an existing id is replaced.
func (s *SQLStore) Insert(ctx context.Context, object *Object) error {
	normalized, err := normalizeObject(object)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO media_objects (`+sqlObjectColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind = excluded.kind,
			source = excluded.source,
			content_type = excluded.content_type,
			bytes = excluded.bytes,
			storage_key = excluded.storage_key,
			request_id = excluded.request_id,
			user_path = excluded.user_path,
			created_at = excluded.created_at,
			expires_at = excluded.expires_at
	`, normalized.ID, string(normalized.Kind), string(normalized.Source), normalized.ContentType, normalized.Bytes,
		normalized.StorageKey, normalized.RequestID, normalized.UserPath,
		normalized.CreatedAt.Unix(), storage.UnixOrZero(normalized.ExpiresAt))
	if err != nil {
		return fmt.Errorf("insert media object: %w", err)
	}
	return nil
}

// Get returns one record by id.
func (s *SQLStore) Get(ctx context.Context, id string) (*Object, error) {
	object, err := scanObject(s.db.QueryRow(ctx, `SELECT `+sqlObjectColumns+` FROM media_objects WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query media object: %w", err)
	}
	return object, nil
}

// Delete removes one record by id.
func (s *SQLStore) Delete(ctx context.Context, id string) error {
	affected, err := s.db.Exec(ctx, `DELETE FROM media_objects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete media object: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Expired returns expired records, oldest expiry first.
func (s *SQLStore) Expired(ctx context.Context, now time.Time, limit int) ([]*Object, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+sqlObjectColumns+`
		FROM media_objects
		WHERE expires_at > 0 AND expires_at <= ?
		ORDER BY expires_at ASC, id ASC
		LIMIT ?
	`, now.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("list expired media objects: %w", err)
	}
	defer rows.Close()
	objects := make([]*Object, 0, limit)
	for rows.Next() {
		object, err := scanObject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan media object: %w", err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate media objects: %w", err)
	}
	return objects, nil
}

// Close is a no-op; connection lifecycle is managed by the storage layer.
func (s *SQLStore) Close() error {
	return nil
}

func scanObject(row sqlx.Row) (*Object, error) {
	var (
		object    Object
		kind      string
		source    string
		createdAt int64
		expiresAt int64
	)
	if err := row.Scan(&object.ID, &kind, &source, &object.ContentType, &object.Bytes, &object.StorageKey,
		&object.RequestID, &object.UserPath, &createdAt, &expiresAt); err != nil {
		return nil, err
	}
	object.Kind = Kind(kind)
	object.Source = Source(source)
	object.CreatedAt = time.Unix(createdAt, 0).UTC()
	object.ExpiresAt = storage.UnixTime(expiresAt)
	return &object, nil
}
