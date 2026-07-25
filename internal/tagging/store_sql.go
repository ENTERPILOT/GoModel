package tagging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore persists tagging rules in a key-value settings table.
type SQLStore struct {
	db sqlx.DB
}

const sqlSchema = `
	CREATE TABLE IF NOT EXISTS tagging_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)
`

// NewSQLStore creates the tagging settings table when missing.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema); err != nil {
		return nil, fmt.Errorf("failed to create tagging_settings table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) GetRules(ctx context.Context) ([]Rule, error) {
	var value string
	err := s.db.QueryRow(ctx, `SELECT value FROM tagging_settings WHERE key = ?`, rulesSettingKey).Scan(&value)
	if errors.Is(err, sqlx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tagging rules: %w", err)
	}
	return decodeRules([]byte(value))
}

func (s *SQLStore) SaveRules(ctx context.Context, rules []Rule) error {
	value, err := encodeRules(rules)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO tagging_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, rulesSettingKey, string(value), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("save tagging rules: %w", err)
	}
	return nil
}

// Close is a no-op: the connection is managed by the storage layer.
func (s *SQLStore) Close() error {
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
