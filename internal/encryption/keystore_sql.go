package encryption

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLKeyStore stores wrapped data keys in SQLite or PostgreSQL. Binary values
// are stored as base64 text so one schema serves both engines.
type SQLKeyStore struct {
	db sqlx.DB
}

const keySQLSchema = `CREATE TABLE IF NOT EXISTS encryption_keys (
	id TEXT PRIMARY KEY,
	wrapped_key TEXT NOT NULL,
	wrapper_id TEXT NOT NULL,
	kdf TEXT NOT NULL DEFAULT '',
	kdf_params TEXT NOT NULL DEFAULT '',
	kdf_salt TEXT NOT NULL DEFAULT '',
	active ` + sqlx.TypeBool + ` NOT NULL DEFAULT FALSE,
	created_at ` + sqlx.TypeInt64 + ` NOT NULL
)`

// NewSQLKeyStore creates the encryption_keys table if needed.
func NewSQLKeyStore(ctx context.Context, db sqlx.DB) (*SQLKeyStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, keySQLSchema); err != nil {
		return nil, fmt.Errorf("create encryption_keys table: %w", err)
	}
	return &SQLKeyStore{db: db}, nil
}

func (s *SQLKeyStore) List(ctx context.Context) ([]Key, error) {
	rows, err := s.db.Query(ctx, `SELECT id, wrapped_key, wrapper_id, kdf, kdf_params, kdf_salt, active, created_at
		FROM encryption_keys ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list encryption keys: %w", err)
	}
	defer rows.Close()
	var keys []Key
	for rows.Next() {
		var (
			key             Key
			wrapped, salt   string
			createdAtSecond int64
		)
		if err := rows.Scan(&key.ID, &wrapped, &key.WrapperID, &key.KDF, &key.KDFParams, &salt, &key.Active, &createdAtSecond); err != nil {
			return nil, fmt.Errorf("scan encryption key: %w", err)
		}
		if key.Wrapped, err = base64.StdEncoding.DecodeString(wrapped); err != nil {
			return nil, fmt.Errorf("decode encryption key %q: %w", key.ID, err)
		}
		if key.Salt, err = base64.StdEncoding.DecodeString(salt); err != nil {
			return nil, fmt.Errorf("decode encryption key %q salt: %w", key.ID, err)
		}
		key.CreatedAt = time.Unix(createdAtSecond, 0).UTC()
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list encryption keys: %w", err)
	}
	return keys, nil
}

func (s *SQLKeyStore) Insert(ctx context.Context, key Key) (bool, error) {
	affected, err := s.db.Exec(ctx, `INSERT INTO encryption_keys
		(id, wrapped_key, wrapper_id, kdf, kdf_params, kdf_salt, active, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		key.ID,
		base64.StdEncoding.EncodeToString(key.Wrapped),
		key.WrapperID,
		key.KDF,
		key.KDFParams,
		base64.StdEncoding.EncodeToString(key.Salt),
		key.Active,
		key.CreatedAt.Unix(),
	)
	if err != nil {
		return false, fmt.Errorf("insert encryption key: %w", err)
	}
	return affected > 0, nil
}

func (s *SQLKeyStore) UpdateWrapping(ctx context.Context, key Key) error {
	affected, err := s.db.Exec(ctx, `UPDATE encryption_keys
		SET wrapped_key = ?, wrapper_id = ?, kdf = ?, kdf_params = ?, kdf_salt = ?
		WHERE id = ?`,
		base64.StdEncoding.EncodeToString(key.Wrapped),
		key.WrapperID,
		key.KDF,
		key.KDFParams,
		base64.StdEncoding.EncodeToString(key.Salt),
		key.ID,
	)
	if err != nil {
		return fmt.Errorf("update encryption key: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("update encryption key: key %q not found", key.ID)
	}
	return nil
}

// Activate flips every row in one statement, so readers never see two active
// keys or none.
func (s *SQLKeyStore) Activate(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	affected, err := s.db.Exec(ctx,
		`UPDATE encryption_keys SET active = CASE WHEN id = ? THEN TRUE ELSE FALSE END
		WHERE EXISTS (SELECT 1 FROM encryption_keys WHERE id = ?)`, id, id)
	if err != nil {
		return fmt.Errorf("activate encryption key: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("activate encryption key: key %q not found", id)
	}
	return nil
}
