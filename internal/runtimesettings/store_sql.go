package runtimesettings

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

type SQLStore struct{ db sqlx.DB }

const sqlSchema = `
	CREATE TABLE IF NOT EXISTS runtime_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)
`

func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema); err != nil {
		return nil, fmt.Errorf("create runtime settings table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

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

func (s *SQLStore) Close() error { return nil }
