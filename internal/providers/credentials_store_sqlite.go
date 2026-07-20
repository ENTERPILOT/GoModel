package providers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SQLiteCredentialStore stores admin-managed provider credentials in SQLite.
type SQLiteCredentialStore struct {
	db *sql.DB
}

// NewSQLiteCredentialStore creates the provider_credentials table and indexes
// if needed.
func NewSQLiteCredentialStore(db *sql.DB) (*SQLiteCredentialStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS provider_credentials (
			name TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			api_keys TEXT NOT NULL DEFAULT '[]',
			base_url TEXT NOT NULL DEFAULT '',
			api_version TEXT NOT NULL DEFAULT '',
			backend TEXT NOT NULL DEFAULT '',
			auth_type TEXT NOT NULL DEFAULT '',
			api_mode TEXT NOT NULL DEFAULT '',
			vertex_project TEXT NOT NULL DEFAULT '',
			vertex_location TEXT NOT NULL DEFAULT '',
			service_account_file TEXT NOT NULL DEFAULT '',
			service_account_json TEXT NOT NULL DEFAULT '',
			service_account_json_base64 TEXT NOT NULL DEFAULT '',
			gcp_scope TEXT NOT NULL DEFAULT '',
			models TEXT NOT NULL DEFAULT '[]',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider_credentials table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_provider_credentials_enabled ON provider_credentials(enabled)`); err != nil {
		return nil, fmt.Errorf("failed to create provider_credentials index: %w", err)
	}
	return &SQLiteCredentialStore{db: db}, nil
}

const sqliteSelectCredentialColumns = `name, type, api_keys, base_url, api_version, backend, auth_type, api_mode, vertex_project, vertex_location, service_account_file, service_account_json, service_account_json_base64, gcp_scope, models, enabled, created_at, updated_at`

func (s *SQLiteCredentialStore) List(ctx context.Context) ([]ManagedProviderCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sqliteSelectCredentialColumns+` FROM provider_credentials ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list provider credentials: %w", err)
	}
	defer rows.Close()
	return collectManagedProviderCredentials(func() (ManagedProviderCredential, bool, error) {
		if !rows.Next() {
			return ManagedProviderCredential{}, false, nil
		}
		cred, err := scanSQLiteCredential(rows)
		return cred, true, err
	}, rows.Err)
}

func (s *SQLiteCredentialStore) Get(ctx context.Context, name string) (*ManagedProviderCredential, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sqliteSelectCredentialColumns+` FROM provider_credentials WHERE name = ?`, normalizeCredentialName(name))
	cred, err := scanSQLiteCredential(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrCredentialNotFound
		}
		return nil, err
	}
	return &cred, nil
}

func (s *SQLiteCredentialStore) Upsert(ctx context.Context, cred ManagedProviderCredential) error {
	stampCredentialUpsert(&cred)
	keysJSON, err := encodeCredentialList(cred.APIKeys)
	if err != nil {
		return err
	}
	modelsJSON, err := encodeCredentialList(cred.Models)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO provider_credentials (
			name, type, api_keys, base_url, api_version, backend, auth_type, api_mode,
			vertex_project, vertex_location, service_account_file, service_account_json,
			service_account_json_base64, gcp_scope, models, enabled, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			type = excluded.type,
			api_keys = excluded.api_keys,
			base_url = excluded.base_url,
			api_version = excluded.api_version,
			backend = excluded.backend,
			auth_type = excluded.auth_type,
			api_mode = excluded.api_mode,
			vertex_project = excluded.vertex_project,
			vertex_location = excluded.vertex_location,
			service_account_file = excluded.service_account_file,
			service_account_json = excluded.service_account_json,
			service_account_json_base64 = excluded.service_account_json_base64,
			gcp_scope = excluded.gcp_scope,
			models = excluded.models,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`,
		normalizeCredentialName(cred.Name),
		cred.Type,
		keysJSON,
		cred.BaseURL,
		cred.APIVersion,
		cred.Backend,
		cred.AuthType,
		cred.APIMode,
		cred.VertexProject,
		cred.VertexLocation,
		cred.ServiceAccountFile,
		cred.ServiceAccountJSON,
		cred.ServiceAccountJSONBase64,
		cred.GCPScope,
		modelsJSON,
		boolToSQLiteInt(cred.Enabled),
		cred.CreatedAt.Unix(),
		cred.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert provider credential: %w", err)
	}
	return nil
}

func (s *SQLiteCredentialStore) Delete(ctx context.Context, name string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM provider_credentials WHERE name = ?`, normalizeCredentialName(name))
	if err != nil {
		return fmt.Errorf("delete provider credential: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delete rows affected: %w", err)
	}
	if affected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

func (s *SQLiteCredentialStore) Close() error {
	return nil
}

func scanSQLiteCredential(scanner interface{ Scan(dest ...any) error }) (ManagedProviderCredential, error) {
	var cred ManagedProviderCredential
	var apiKeys, models string
	var enabled int
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&cred.Name,
		&cred.Type,
		&apiKeys,
		&cred.BaseURL,
		&cred.APIVersion,
		&cred.Backend,
		&cred.AuthType,
		&cred.APIMode,
		&cred.VertexProject,
		&cred.VertexLocation,
		&cred.ServiceAccountFile,
		&cred.ServiceAccountJSON,
		&cred.ServiceAccountJSONBase64,
		&cred.GCPScope,
		&models,
		&enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return ManagedProviderCredential{}, err
	}
	var err error
	if cred.APIKeys, err = decodeCredentialList([]byte(apiKeys)); err != nil {
		return ManagedProviderCredential{}, err
	}
	if cred.Models, err = decodeCredentialList([]byte(models)); err != nil {
		return ManagedProviderCredential{}, err
	}
	cred.Enabled = enabled != 0
	cred.CreatedAt = time.Unix(createdAt, 0).UTC()
	cred.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return cred, nil
}

func boolToSQLiteInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
