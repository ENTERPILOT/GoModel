package authkeys

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore stores auth keys in PostgreSQL.
type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLStore creates the auth_keys table and indexes if needed.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS auth_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			redacted_value TEXT NOT NULL,
			secret_hash TEXT NOT NULL UNIQUE,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			expires_at BIGINT,
			deactivated_at BIGINT,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth_keys table: %w", err)
	}
	for _, index := range []string{
		`CREATE INDEX IF NOT EXISTS idx_auth_keys_enabled ON auth_keys(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_keys_created_at ON auth_keys(created_at DESC)`,
	} {
		if _, err := pool.Exec(ctx, index); err != nil {
			return nil, fmt.Errorf("failed to create auth_keys index: %w", err)
		}
	}
	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) List(ctx context.Context) ([]AuthKey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, description, redacted_value, secret_hash, enabled, expires_at, deactivated_at, created_at, updated_at
		FROM auth_keys
		ORDER BY created_at DESC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list auth keys: %w", err)
	}
	defer rows.Close()
	result, err := collectAuthKeys(rows, scanPostgreSQLAuthKey)
	if err != nil {
		return nil, fmt.Errorf("iterate auth keys: %w", err)
	}
	return result, nil
}

func (s *PostgreSQLStore) Create(ctx context.Context, key AuthKey) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth_keys (id, name, description, redacted_value, secret_hash, enabled, expires_at, deactivated_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, key.ID, key.Name, key.Description, key.RedactedValue, key.SecretHash, key.Enabled, pgUnixOrNil(key.ExpiresAt), pgUnixOrNil(key.DeactivatedAt), key.CreatedAt.Unix(), key.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("create auth key: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Deactivate(ctx context.Context, id string, now time.Time) error {
	cmd, err := s.pool.Exec(ctx, `
		UPDATE auth_keys
		SET enabled = FALSE,
			deactivated_at = COALESCE(deactivated_at, $1),
			updated_at = $2
		WHERE id = $3
	`, now.Unix(), now.Unix(), normalizeID(id))
	if err != nil {
		return fmt.Errorf("deactivate auth key: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgreSQLStore) Close() error {
	return nil
}

func scanPostgreSQLAuthKey(scanner authKeyScanner) (AuthKey, error) {
	var key AuthKey
	var expiresAt *int64
	var deactivatedAt *int64
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&key.ID,
		&key.Name,
		&key.Description,
		&key.RedactedValue,
		&key.SecretHash,
		&key.Enabled,
		&expiresAt,
		&deactivatedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return AuthKey{}, ErrNotFound
		}
		return AuthKey{}, err
	}
	key.ExpiresAt = int64PtrToTime(expiresAt)
	key.DeactivatedAt = int64PtrToTime(deactivatedAt)
	key.CreatedAt = time.Unix(createdAt, 0).UTC()
	key.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return key, nil
}

func pgUnixOrNil(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Unix()
}

func int64PtrToTime(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	t := time.Unix(*value, 0).UTC()
	return &t
}
