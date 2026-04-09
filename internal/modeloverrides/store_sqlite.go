package modeloverrides

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SQLiteStore stores model overrides in SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates the model_overrides table and indexes if needed.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS model_overrides (
			selector TEXT PRIMARY KEY,
			provider_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			enabled INTEGER NULL,
			force_disabled INTEGER NOT NULL DEFAULT 0,
			allowed_only_for_user_paths TEXT NOT NULL DEFAULT '[]',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create model_overrides table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_model_overrides_provider_name ON model_overrides(provider_name)`); err != nil {
		return nil, fmt.Errorf("failed to create model_overrides provider_name index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_model_overrides_model ON model_overrides(model)`); err != nil {
		return nil, fmt.Errorf("failed to create model_overrides model index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_model_overrides_updated_at ON model_overrides(updated_at DESC)`); err != nil {
		return nil, fmt.Errorf("failed to create model_overrides updated_at index: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) List(ctx context.Context) ([]Override, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT selector, provider_name, model, enabled, force_disabled, allowed_only_for_user_paths, created_at, updated_at
		FROM model_overrides
		ORDER BY selector ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list model overrides: %w", err)
	}
	defer rows.Close()
	return collectOverrides(func() (Override, bool, error) {
		if !rows.Next() {
			return Override{}, false, nil
		}
		override, err := scanSQLiteOverride(rows)
		return override, true, err
	}, rows.Err)
}

func (s *SQLiteStore) Upsert(ctx context.Context, override Override) error {
	override, err := normalizeStoredOverride(override)
	if err != nil {
		return err
	}

	pathsJSON, err := json.Marshal(override.AllowedOnlyForUserPaths)
	if err != nil {
		return fmt.Errorf("encode allowed_only_for_user_paths: %w", err)
	}

	now := time.Now().UTC().Unix()
	if override.CreatedAt.IsZero() {
		override.CreatedAt = time.Unix(now, 0).UTC()
	}
	override.UpdatedAt = time.Unix(now, 0).UTC()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO model_overrides (
			selector, provider_name, model, enabled, force_disabled, allowed_only_for_user_paths, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(selector) DO UPDATE SET
			provider_name = excluded.provider_name,
			model = excluded.model,
			enabled = excluded.enabled,
			force_disabled = excluded.force_disabled,
			allowed_only_for_user_paths = excluded.allowed_only_for_user_paths,
			updated_at = excluded.updated_at
	`,
		override.Selector,
		override.ProviderName,
		override.Model,
		sqliteNullableBool(override.Enabled),
		boolToSQLite(override.ForceDisabled),
		string(pathsJSON),
		override.CreatedAt.Unix(),
		override.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert model override: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, selector string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM model_overrides WHERE selector = ?`, strings.TrimSpace(selector))
	if err != nil {
		return fmt.Errorf("delete model override: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delete rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return nil
}

func scanSQLiteOverride(scanner interface{ Scan(dest ...any) error }) (Override, error) {
	var override Override
	var enabled sql.NullBool
	var forceDisabled int
	var allowedOnlyForUserPaths string
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&override.Selector,
		&override.ProviderName,
		&override.Model,
		&enabled,
		&forceDisabled,
		&allowedOnlyForUserPaths,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Override{}, fmt.Errorf("scan model override: %w", err)
	}
	if enabled.Valid {
		override.Enabled = &enabled.Bool
	}
	override.ForceDisabled = forceDisabled != 0
	if err := json.Unmarshal([]byte(allowedOnlyForUserPaths), &override.AllowedOnlyForUserPaths); err != nil {
		return Override{}, fmt.Errorf("decode allowed_only_for_user_paths: %w", err)
	}
	override.CreatedAt = time.Unix(createdAt, 0).UTC()
	override.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return override, nil
}

func sqliteNullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	if *value {
		return 1
	}
	return 0
}

func boolToSQLite(value bool) int {
	if value {
		return 1
	}
	return 0
}
