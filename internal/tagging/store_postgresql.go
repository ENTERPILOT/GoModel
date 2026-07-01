package tagging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore persists tagging rules in a key-value settings table.
type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLStore creates the tagging settings table when missing.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS tagging_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at BIGINT NOT NULL
		)
	`); err != nil {
		return nil, fmt.Errorf("failed to create tagging_settings table: %w", err)
	}
	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) GetRules(ctx context.Context) ([]Rule, error) {
	var value string
	err := s.pool.QueryRow(ctx, `SELECT value FROM tagging_settings WHERE key = $1`, rulesSettingKey).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tagging rules: %w", err)
	}
	return decodeRules([]byte(value))
}

func (s *PostgreSQLStore) SaveRules(ctx context.Context, rules []Rule) error {
	value, err := encodeRules(rules)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO tagging_settings (key, value, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at
	`, rulesSettingKey, string(value), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("save tagging rules: %w", err)
	}
	return nil
}

// Close is a no-op: the pool is managed by the storage layer.
func (s *PostgreSQLStore) Close() error {
	return nil
}
