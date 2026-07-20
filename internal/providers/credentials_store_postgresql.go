package providers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLCredentialStore stores admin-managed provider credentials in
// PostgreSQL.
type PostgreSQLCredentialStore struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLCredentialStore creates the provider_credentials table and
// indexes if needed.
func NewPostgreSQLCredentialStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLCredentialStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS provider_credentials (
			name TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			api_keys JSONB NOT NULL DEFAULT '[]'::jsonb,
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
			models JSONB NOT NULL DEFAULT '[]'::jsonb,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider_credentials table: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_provider_credentials_enabled ON provider_credentials(enabled)`); err != nil {
		return nil, fmt.Errorf("failed to create provider_credentials index: %w", err)
	}
	return &PostgreSQLCredentialStore{pool: pool}, nil
}

const postgresSelectCredentialColumns = `name, type, api_keys, base_url, api_version, backend, auth_type, api_mode, vertex_project, vertex_location, service_account_file, service_account_json, service_account_json_base64, gcp_scope, models, enabled, created_at, updated_at`

func (s *PostgreSQLCredentialStore) List(ctx context.Context) ([]ManagedProviderCredential, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+postgresSelectCredentialColumns+` FROM provider_credentials ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list provider credentials: %w", err)
	}
	defer rows.Close()
	return collectManagedProviderCredentials(func() (ManagedProviderCredential, bool, error) {
		if !rows.Next() {
			return ManagedProviderCredential{}, false, nil
		}
		cred, err := scanPostgreSQLCredential(rows)
		return cred, true, err
	}, rows.Err)
}

func (s *PostgreSQLCredentialStore) Get(ctx context.Context, name string) (*ManagedProviderCredential, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+postgresSelectCredentialColumns+` FROM provider_credentials WHERE name = $1`, normalizeCredentialName(name))
	cred, err := scanPostgreSQLCredential(row)
	if err != nil {
		if errors.Is(err, ErrCredentialNotFound) {
			return nil, ErrCredentialNotFound
		}
		return nil, err
	}
	return &cred, nil
}

func (s *PostgreSQLCredentialStore) Upsert(ctx context.Context, cred ManagedProviderCredential) error {
	stampCredentialUpsert(&cred)
	keysJSON, err := encodeCredentialList(cred.APIKeys)
	if err != nil {
		return err
	}
	modelsJSON, err := encodeCredentialList(cred.Models)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO provider_credentials (
			name, type, api_keys, base_url, api_version, backend, auth_type, api_mode,
			vertex_project, vertex_location, service_account_file, service_account_json,
			service_account_json_base64, gcp_scope, models, enabled, created_at, updated_at
		)
		VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb, $16, $17, $18)
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
		cred.Enabled,
		cred.CreatedAt.Unix(),
		cred.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert provider credential: %w", err)
	}
	return nil
}

func (s *PostgreSQLCredentialStore) Delete(ctx context.Context, name string) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM provider_credentials WHERE name = $1`, normalizeCredentialName(name))
	if err != nil {
		return fmt.Errorf("delete provider credential: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

func (s *PostgreSQLCredentialStore) Close() error {
	return nil
}

func scanPostgreSQLCredential(scanner interface{ Scan(dest ...any) error }) (ManagedProviderCredential, error) {
	var cred ManagedProviderCredential
	var apiKeys, models []byte
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
		&cred.Enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ManagedProviderCredential{}, ErrCredentialNotFound
		}
		return ManagedProviderCredential{}, fmt.Errorf("scan provider credential: %w", err)
	}
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
