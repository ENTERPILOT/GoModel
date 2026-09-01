package runtimesettings

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore persists runtime settings in SQLite or PostgreSQL.
type SQLStore struct{ db sqlx.DB }

const sqlSchema = `
	CREATE TABLE IF NOT EXISTS runtime_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)
`

// NewSQLStore initializes the shared runtime-settings table.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema); err != nil {
		return nil, fmt.Errorf("create runtime settings table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Get returns a persisted value when present.
func (s *SQLStore) Get(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(ctx, `SELECT value FROM runtime_settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sqlx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get runtime setting %q: %w", key, err)
	}
	return value, true, nil
}

// SetDefault inserts value unless key exists, then reads back the winner.
// ON CONFLICT DO NOTHING is atomic in both SQLite and PostgreSQL, so two
// instances inserting at once cannot both believe they won.
func (s *SQLStore) SetDefault(ctx context.Context, key, value string) (string, error) {
	_, err := s.db.Exec(ctx, `
		INSERT INTO runtime_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO NOTHING
	`, key, value, time.Now().Unix())
	if err != nil {
		return "", fmt.Errorf("initialise runtime setting %q: %w", key, err)
	}
	stored, found, err := s.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("initialise runtime setting %q: value missing after insert", key)
	}
	return stored, nil
}

// Set upserts a persisted value.
func (s *SQLStore) Set(ctx context.Context, key, value string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO runtime_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, key, value, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("save runtime setting %q: %w", key, err)
	}
	return nil
}
