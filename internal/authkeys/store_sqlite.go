package authkeys

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLiteStore stores auth keys in SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates the auth_keys table and indexes if needed.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS auth_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			user_path TEXT,
			redacted_value TEXT NOT NULL,
			secret_hash TEXT NOT NULL UNIQUE,
			enabled INTEGER NOT NULL DEFAULT 1,
			expires_at INTEGER,
			deactivated_at INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth_keys table: %w", err)
	}

	migrations := []string{
		`ALTER TABLE auth_keys ADD COLUMN user_path TEXT`,
	}
	for _, migration := range migrations {
		if _, err := db.Exec(migration); err != nil && !isSQLiteDuplicateColumnError(err) {
			return nil, fmt.Errorf("failed to run migration %q: %w", migration, err)
		}
	}
	for _, index := range []string{
		`CREATE INDEX IF NOT EXISTS idx_auth_keys_enabled ON auth_keys(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_keys_created_at ON auth_keys(created_at DESC)`,
	} {
		if _, err := db.Exec(index); err != nil {
			return nil, fmt.Errorf("failed to create auth_keys index: %w", err)
		}
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) List(ctx context.Context) ([]AuthKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description, user_path, redacted_value, secret_hash, enabled, expires_at, deactivated_at, created_at, updated_at
		FROM auth_keys
		ORDER BY created_at DESC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list auth keys: %w", err)
	}
	defer rows.Close()
	result, err := collectAuthKeys(rows, scanSQLiteAuthKey)
	if err != nil {
		return nil, fmt.Errorf("iterate auth keys: %w", err)
	}
	return result, nil
}

func (s *SQLiteStore) Create(ctx context.Context, key AuthKey) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_keys (id, name, description, user_path, redacted_value, secret_hash, enabled, expires_at, deactivated_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, key.ID, key.Name, key.Description, nullableString(key.UserPath), key.RedactedValue, key.SecretHash, boolToSQLite(key.Enabled), unixOrNil(key.ExpiresAt), unixOrNil(key.DeactivatedAt), key.CreatedAt.Unix(), key.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("create auth key: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Deactivate(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE auth_keys
		SET enabled = 0,
			deactivated_at = COALESCE(deactivated_at, ?),
			updated_at = ?
		WHERE id = ?
	`, now.Unix(), now.Unix(), normalizeID(id))
	if err != nil {
		return fmt.Errorf("deactivate auth key: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deactivate rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return nil
}

func scanSQLiteAuthKey(scanner authKeyScanner) (AuthKey, error) {
	var key AuthKey
	var userPath sql.NullString
	var enabled int
	var expiresAt sql.NullInt64
	var deactivatedAt sql.NullInt64
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&key.ID,
		&key.Name,
		&key.Description,
		&userPath,
		&key.RedactedValue,
		&key.SecretHash,
		&enabled,
		&expiresAt,
		&deactivatedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AuthKey{}, ErrNotFound
		}
		return AuthKey{}, err
	}
	key.UserPath = nullableStringValue(userPath)
	key.Enabled = enabled != 0
	key.ExpiresAt = unixPtr(expiresAt)
	key.DeactivatedAt = unixPtr(deactivatedAt)
	key.CreatedAt = time.Unix(createdAt, 0).UTC()
	key.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return key, nil
}

func isSQLiteDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate column") || strings.Contains(message, "already exists")
}

func boolToSQLite(v bool) int {
	if v {
		return 1
	}
	return 0
}

func unixOrNil(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Unix()
}

func unixPtr(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	t := time.Unix(value.Int64, 0).UTC()
	return &t
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func nullableStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}
