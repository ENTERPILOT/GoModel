package providers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLCredentialStore stores admin-managed provider credentials in a SQL
// database.
type SQLCredentialStore struct {
	db sqlx.DB
}

var credentialSQLSchema = []string{
	`CREATE TABLE IF NOT EXISTS provider_credentials (
		name TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		api_keys TEXT NOT NULL DEFAULT '[]',
		session_sticky_keys ` + sqlx.TypeBool + ` NOT NULL DEFAULT TRUE,
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
		proxy_url TEXT NOT NULL DEFAULT '',
		models TEXT NOT NULL DEFAULT '[]',
		enabled ` + sqlx.TypeBool + ` NOT NULL DEFAULT TRUE,
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_provider_credentials_enabled ON provider_credentials(enabled)`,
}

const selectCredentialColumns = `name, type, api_keys, base_url, api_version, backend, auth_type, api_mode, ` +
	`vertex_project, vertex_location, service_account_file, service_account_json, ` +
	`service_account_json_base64, gcp_scope, proxy_url, models, session_sticky_keys, enabled, created_at, updated_at`

// NewSQLCredentialStore creates the provider_credentials table and indexes if
// needed.
func NewSQLCredentialStore(ctx context.Context, db sqlx.DB) (*SQLCredentialStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, credentialSQLSchema...); err != nil {
		return nil, fmt.Errorf("failed to create provider_credentials table: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db,
		`ALTER TABLE provider_credentials ADD COLUMN session_sticky_keys `+sqlx.TypeBool+` NOT NULL DEFAULT TRUE`,
		`ALTER TABLE provider_credentials ADD COLUMN proxy_url TEXT NOT NULL DEFAULT ''`,
	); err != nil {
		return nil, fmt.Errorf("migrate provider_credentials: %w", err)
	}
	return &SQLCredentialStore{db: db}, nil
}

func (s *SQLCredentialStore) List(ctx context.Context) ([]ManagedProviderCredential, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+selectCredentialColumns+` FROM provider_credentials ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list provider credentials: %w", err)
	}
	defer rows.Close()
	return collectManagedProviderCredentials(func() (ManagedProviderCredential, bool, error) {
		if !rows.Next() {
			return ManagedProviderCredential{}, false, nil
		}
		cred, err := scanSQLCredential(rows)
		return cred, true, err
	}, rows.Err)
}

func (s *SQLCredentialStore) Get(ctx context.Context, name string) (*ManagedProviderCredential, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+selectCredentialColumns+` FROM provider_credentials WHERE name = ?`,
		normalizeCredentialName(name))
	cred, err := scanSQLCredential(row)
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return nil, ErrCredentialNotFound
		}
		return nil, err
	}
	return &cred, nil
}

func (s *SQLCredentialStore) Upsert(ctx context.Context, cred ManagedProviderCredential) error {
	stampCredentialUpsert(&cred)
	keysJSON, err := encodeCredentialList(cred.APIKeys)
	if err != nil {
		return err
	}
	modelsJSON, err := encodeCredentialList(cred.Models)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO provider_credentials (
			name, type, api_keys, base_url, api_version, backend, auth_type, api_mode,
			vertex_project, vertex_location, service_account_file, service_account_json,
			service_account_json_base64, gcp_scope, proxy_url, models, session_sticky_keys, enabled, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			proxy_url = excluded.proxy_url,
			models = excluded.models,
			session_sticky_keys = excluded.session_sticky_keys,
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
		cred.ProxyURL,
		modelsJSON,
		sessionStickyKeysEnabled(cred.SessionStickyKeys),
		cred.Enabled,
		cred.CreatedAt.Unix(),
		cred.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert provider credential: %w", err)
	}
	return nil
}

func (s *SQLCredentialStore) Delete(ctx context.Context, name string) error {
	affected, err := s.db.Exec(ctx,
		`DELETE FROM provider_credentials WHERE name = ?`, normalizeCredentialName(name))
	if err != nil {
		return fmt.Errorf("delete provider credential: %w", err)
	}
	if affected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

func (s *SQLCredentialStore) Close() error {
	return nil
}

func scanSQLCredential(scanner sqlx.Row) (ManagedProviderCredential, error) {
	var cred ManagedProviderCredential
	var apiKeys, models []byte
	var sessionStickyKeys bool
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
		&cred.ProxyURL,
		&models,
		&sessionStickyKeys,
		&cred.Enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return ManagedProviderCredential{}, err
	}
	cred.SessionStickyKeys = &sessionStickyKeys
	var err error
	if cred.APIKeys, err = decodeCredentialList(apiKeys); err != nil {
		return ManagedProviderCredential{}, err
	}
	if cred.Models, err = decodeCredentialList(models); err != nil {
		return ManagedProviderCredential{}, err
	}
	cred.CreatedAt = time.Unix(createdAt, 0).UTC()
	cred.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return cred, nil
}
