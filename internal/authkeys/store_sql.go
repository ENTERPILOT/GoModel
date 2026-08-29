package authkeys

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores auth keys in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

// Boolean defaults are spelled TRUE/FALSE rather than 1/0: PostgreSQL rejects
// an integer default on a BOOLEAN column, and SQLite accepts the keywords and
// stores them as 1/0 in its INTEGER column.
var sqlTable = `CREATE TABLE IF NOT EXISTS auth_keys (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		user_path TEXT,
		user_id TEXT,
		labels ` + sqlx.TypeJSON + `,
		dashboard_access ` + sqlx.TypeBool + ` NOT NULL DEFAULT FALSE,
		redacted_value TEXT NOT NULL,
		secret_hash TEXT NOT NULL UNIQUE,
		enabled ` + sqlx.TypeBool + ` NOT NULL DEFAULT TRUE,
		expires_at ` + sqlx.TypeInt64 + `,
		deactivated_at ` + sqlx.TypeInt64 + `,
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`

var sqlIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_auth_keys_enabled ON auth_keys(enabled)`,
	`CREATE INDEX IF NOT EXISTS idx_auth_keys_created_at ON auth_keys(created_at DESC)`,
}

// sqlMigrations backfill columns added after the table's first release.
var sqlMigrations = []string{
	`ALTER TABLE auth_keys ADD COLUMN user_path TEXT`,
	`ALTER TABLE auth_keys ADD COLUMN labels ` + sqlx.TypeJSON,
	`ALTER TABLE auth_keys ADD COLUMN dashboard_access ` + sqlx.TypeBool + ` NOT NULL DEFAULT FALSE`,
	`ALTER TABLE auth_keys ADD COLUMN user_id TEXT`,
}

const selectAuthKeyColumns = `
	SELECT id, name, description, user_path, user_id, labels, dashboard_access,
		redacted_value, secret_hash, enabled, expires_at, deactivated_at,
		created_at, updated_at
	FROM auth_keys
`

// NewSQLStore creates the auth_keys table and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlTable); err != nil {
		return nil, fmt.Errorf("failed to create auth_keys table: %w", err)
	}
	// Migrations run before the indexes so that an index over a backfilled
	// column would find it present on an older database.
	if err := sqlx.AddColumns(ctx, db, sqlMigrations...); err != nil {
		return nil, err
	}
	if err := db.Schema(ctx, sqlIndexes...); err != nil {
		return nil, fmt.Errorf("failed to create auth_keys index: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) List(ctx context.Context) ([]AuthKey, error) {
	rows, err := s.db.Query(ctx, selectAuthKeyColumns+`ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list auth keys: %w", err)
	}
	defer rows.Close()
	result, err := collectAuthKeys(rows, scanSQLAuthKey)
	if err != nil {
		return nil, fmt.Errorf("iterate auth keys: %w", err)
	}
	return result, nil
}

func (s *SQLStore) Create(ctx context.Context, key AuthKey) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO auth_keys (
			id, name, description, user_path, user_id, labels, dashboard_access,
			redacted_value, secret_hash, enabled, expires_at, deactivated_at,
			created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, key.ID, key.Name, key.Description,
		sqlutil.NullableString(key.UserPath), sqlutil.NullableString(key.UserID),
		sqlutil.NullableJSONStrings(key.Labels, key.ID),
		key.DashboardAccess, key.RedactedValue, key.SecretHash, key.Enabled,
		sqlutil.UnixOrNil(key.ExpiresAt), sqlutil.UnixOrNil(key.DeactivatedAt),
		key.CreatedAt.Unix(), key.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("create auth key: %w", err)
	}
	return nil
}

func (s *SQLStore) UpdateLabels(ctx context.Context, id string, labels []string, now time.Time) error {
	affected, err := s.db.Exec(ctx, `
		UPDATE auth_keys
		SET labels = ?,
			updated_at = ?
		WHERE id = ?
	`, sqlutil.NullableJSONStrings(labels, id), now.Unix(), normalizeID(id))
	if err != nil {
		return fmt.Errorf("update auth key labels: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) UpdateDashboardAccess(ctx context.Context, id string, allowed bool, now time.Time) error {
	affected, err := s.db.Exec(ctx, `
		UPDATE auth_keys
		SET dashboard_access = ?,
			updated_at = ?
		WHERE id = ?
	`, allowed, now.Unix(), normalizeID(id))
	if err != nil {
		return fmt.Errorf("update auth key dashboard access: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Deactivate(ctx context.Context, id string, now time.Time) error {
	affected, err := s.db.Exec(ctx, `
		UPDATE auth_keys
		SET enabled = FALSE,
			deactivated_at = COALESCE(deactivated_at, ?),
			updated_at = ?
		WHERE id = ?
	`, now.Unix(), now.Unix(), normalizeID(id))
	if err != nil {
		return fmt.Errorf("deactivate auth key: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Close() error {
	return nil
}

func scanSQLAuthKey(scanner authKeyScanner) (AuthKey, error) {
	var key AuthKey
	var userPath, userID, labelsJSON *string
	var expiresAt, deactivatedAt *int64
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&key.ID,
		&key.Name,
		&key.Description,
		&userPath,
		&userID,
		&labelsJSON,
		&key.DashboardAccess,
		&key.RedactedValue,
		&key.SecretHash,
		&key.Enabled,
		&expiresAt,
		&deactivatedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return AuthKey{}, ErrNotFound
		}
		return AuthKey{}, err
	}
	key.UserPath = sqlutil.DerefTrimmed(userPath)
	key.UserID = sqlutil.DerefTrimmed(userID)
	if labelsJSON != nil {
		key.Labels = sqlutil.StringsFromJSON(*labelsJSON, key.ID)
	}
	key.ExpiresAt = sqlutil.TimeFromUnixPtr(expiresAt)
	key.DeactivatedAt = sqlutil.TimeFromUnixPtr(deactivatedAt)
	key.CreatedAt = time.Unix(createdAt, 0).UTC()
	key.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return key, nil
}
