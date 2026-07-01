package tagging

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/goccy/go-json"
)

// SQLiteStore persists tagging rules in a key-value settings table.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates the tagging settings table when missing.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS tagging_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`); err != nil {
		return nil, fmt.Errorf("failed to create tagging_settings table: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) GetRules(ctx context.Context) ([]Rule, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM tagging_settings WHERE key = ?`, rulesSettingKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tagging rules: %w", err)
	}
	return decodeRules([]byte(value))
}

func (s *SQLiteStore) SaveRules(ctx context.Context, rules []Rule) error {
	value, err := encodeRules(rules)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO tagging_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, rulesSettingKey, string(value), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("save tagging rules: %w", err)
	}
	return nil
}

// Close is a no-op: the DB handle is managed by the storage layer.
func (s *SQLiteStore) Close() error {
	return nil
}

func encodeRules(rules []Rule) ([]byte, error) {
	if rules == nil {
		rules = []Rule{}
	}
	value, err := json.Marshal(rules)
	if err != nil {
		return nil, fmt.Errorf("encode tagging rules: %w", err)
	}
	return value, nil
}

func decodeRules(value []byte) ([]Rule, error) {
	if len(value) == 0 {
		return nil, nil
	}
	var rules []Rule
	if err := json.Unmarshal(value, &rules); err != nil {
		return nil, fmt.Errorf("decode tagging rules: %w", err)
	}
	return rules, nil
}
