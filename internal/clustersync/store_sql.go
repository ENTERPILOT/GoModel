package clustersync

import (
	"context"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore keeps configuration revisions in SQLite or PostgreSQL.
type SQLStore struct{ db sqlx.DB }

const sqlSchema = `
	CREATE TABLE IF NOT EXISTS config_revisions (
		entity TEXT PRIMARY KEY,
		revision ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)
`

// NewSQLStore initializes the shared config_revisions table.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema); err != nil {
		return nil, fmt.Errorf("create config revisions table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Bump increments entity's revision. The upsert is a single statement, so
// concurrent bumps from several instances never lose an increment.
func (s *SQLStore) Bump(ctx context.Context, entity string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO config_revisions (entity, revision, updated_at) VALUES (?, 1, ?)
		ON CONFLICT(entity) DO UPDATE SET revision = config_revisions.revision + 1, updated_at = excluded.updated_at
	`, entity, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("bump config revision %q: %w", entity, err)
	}
	return nil
}

// Revisions returns every stored revision.
func (s *SQLStore) Revisions(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT entity, revision FROM config_revisions`)
	if err != nil {
		return nil, fmt.Errorf("list config revisions: %w", err)
	}
	defer rows.Close()
	revisions := make(map[string]int64)
	for rows.Next() {
		var entity string
		var revision int64
		if err := rows.Scan(&entity, &revision); err != nil {
			return nil, fmt.Errorf("scan config revision: %w", err)
		}
		revisions[entity] = revision
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list config revisions: %w", err)
	}
	return revisions, nil
}
