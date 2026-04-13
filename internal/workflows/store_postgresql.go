package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore stores immutable workflow versions in PostgreSQL.
type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLStore creates the workflow table and indexes if needed.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}

	statements := []string{
		`CREATE TABLE IF NOT EXISTS workflow_versions (
			id UUID PRIMARY KEY,
			scope_provider TEXT,
			scope_model TEXT,
			scope_user_path TEXT,
			scope_key TEXT NOT NULL,
			version INTEGER NOT NULL,
			active BOOLEAN NOT NULL DEFAULT TRUE,
			managed_default BOOLEAN NOT NULL DEFAULT FALSE,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			workflow_payload JSONB NOT NULL,
			workflow_hash TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			CHECK (scope_provider IS NOT NULL OR scope_model IS NULL)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_versions_scope_version
			ON workflow_versions(scope_key, version)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_versions_active_scope
			ON workflow_versions(scope_key) WHERE active = TRUE`,
		`CREATE INDEX IF NOT EXISTS idx_workflow_versions_active_created_at
			ON workflow_versions(active, created_at DESC)`,
		`ALTER TABLE workflow_versions ADD COLUMN IF NOT EXISTS scope_user_path TEXT`,
		`ALTER TABLE workflow_versions ADD COLUMN IF NOT EXISTS managed_default BOOLEAN NOT NULL DEFAULT FALSE`,
		`UPDATE workflow_versions
			SET managed_default = TRUE
			WHERE managed_default = FALSE
			  AND scope_key = 'global'
			  AND name = '` + ManagedDefaultGlobalName + `'
			  AND description = '` + ManagedDefaultGlobalDescription + `'`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return nil, fmt.Errorf("initialize workflow versions table: %w", err)
		}
	}

	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) ListActive(ctx context.Context) ([]Version, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, scope_provider, scope_model, scope_user_path, scope_key, version, active, managed_default, name, description, workflow_payload, workflow_hash, created_at
		FROM workflow_versions
		WHERE active = TRUE
		ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list active workflows: %w", err)
	}
	defer rows.Close()
	return collectVersions(rows, func(scanner versionRowScanner) (Version, error) {
		return scanPostgreSQLVersion(scanner)
	})
}

func (s *PostgreSQLStore) Get(ctx context.Context, id string) (*Version, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, scope_provider, scope_model, scope_user_path, scope_key, version, active, managed_default, name, description, workflow_payload, workflow_hash, created_at
		FROM workflow_versions
		WHERE id::text = $1
	`, id)
	version, err := scanPostgreSQLVersion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &version, nil
}

func (s *PostgreSQLStore) Create(ctx context.Context, input CreateInput) (*Version, error) {
	input, scopeKey, workflowHash, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for range 5 {
		version, err := s.createVersion(ctx, input, scopeKey, workflowHash)
		if err == nil {
			return version, nil
		}
		if !isPostgreSQLUniqueViolation(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("insert workflow version after concurrent retries: %w", lastErr)
}

func (s *PostgreSQLStore) EnsureManagedDefaultGlobal(ctx context.Context, input CreateInput, workflowHash string) (*Version, error) {
	var lastErr error
	for range 5 {
		version, err := s.ensureManagedDefaultGlobal(ctx, input, workflowHash)
		if err == nil {
			return version, nil
		}
		if !isPostgreSQLUniqueViolation(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("ensure managed default workflow after concurrent retries: %w", lastErr)
}

func (s *PostgreSQLStore) createVersion(ctx context.Context, input CreateInput, scopeKey, workflowHash string) (*Version, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin workflow transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var nextVersion int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM workflow_versions WHERE scope_key = $1`,
		scopeKey,
	).Scan(&nextVersion); err != nil {
		return nil, fmt.Errorf("select next workflow version: %w", err)
	}

	if input.Activate {
		if _, err := tx.Exec(ctx,
			`UPDATE workflow_versions SET active = FALSE WHERE scope_key = $1 AND active = TRUE`,
			scopeKey,
		); err != nil {
			return nil, fmt.Errorf("deactivate current workflow version: %w", err)
		}
	}

	payloadJSON, err := json.Marshal(input.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshal workflow payload: %w", err)
	}

	now := time.Now().UTC()
	version := &Version{
		ID:           uuid.NewString(),
		Scope:        input.Scope,
		ScopeKey:     scopeKey,
		Version:      nextVersion,
		Active:       input.Activate,
		Managed:      input.Managed,
		Name:         input.Name,
		Description:  input.Description,
		Payload:      input.Payload,
		WorkflowHash: workflowHash,
		CreatedAt:    now,
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO workflow_versions (
			id, scope_provider, scope_model, scope_user_path, scope_key, version, active, managed_default, name, description, workflow_payload, workflow_hash, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		version.ID,
		nullIfEmpty(version.Scope.Provider),
		nullIfEmpty(version.Scope.Model),
		nullIfEmpty(version.Scope.UserPath),
		version.ScopeKey,
		version.Version,
		version.Active,
		version.Managed,
		version.Name,
		version.Description,
		payloadJSON,
		version.WorkflowHash,
		version.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("insert workflow version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit workflow version: %w", err)
	}
	return version, nil
}

func (s *PostgreSQLStore) ensureManagedDefaultGlobal(ctx context.Context, input CreateInput, workflowHash string) (*Version, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin workflow transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	row := tx.QueryRow(ctx, `
		SELECT id, scope_provider, scope_model, scope_user_path, scope_key, version, active, managed_default, name, description, workflow_payload, workflow_hash, created_at
		FROM workflow_versions
		WHERE scope_key = 'global' AND active = TRUE
		ORDER BY created_at DESC, id DESC
		LIMIT 1
		FOR UPDATE
	`)
	activeVersion, err := scanPostgreSQLVersion(row)
	hasActive := true
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			hasActive = false
		} else {
			return nil, fmt.Errorf("load active global workflow: %w", err)
		}
	}
	if hasActive {
		if !activeVersion.Managed {
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("commit workflow transaction: %w", err)
			}
			return nil, nil
		}
		if strings.TrimSpace(activeVersion.Name) == input.Name &&
			strings.TrimSpace(activeVersion.Description) == input.Description &&
			strings.TrimSpace(activeVersion.WorkflowHash) == workflowHash {
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("commit workflow transaction: %w", err)
			}
			return nil, nil
		}
	}

	var nextVersion int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM workflow_versions WHERE scope_key = 'global'`,
	).Scan(&nextVersion); err != nil {
		return nil, fmt.Errorf("select next workflow version: %w", err)
	}

	if hasActive {
		if _, err := tx.Exec(ctx,
			`UPDATE workflow_versions SET active = FALSE WHERE id = $1 AND active = TRUE`,
			activeVersion.ID,
		); err != nil {
			return nil, fmt.Errorf("deactivate current workflow version: %w", err)
		}
	}

	payloadJSON, err := json.Marshal(input.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshal workflow payload: %w", err)
	}

	now := time.Now().UTC()
	version := &Version{
		ID:           uuid.NewString(),
		Scope:        input.Scope,
		ScopeKey:     "global",
		Version:      nextVersion,
		Active:       true,
		Managed:      true,
		Name:         input.Name,
		Description:  input.Description,
		Payload:      input.Payload,
		WorkflowHash: workflowHash,
		CreatedAt:    now,
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO workflow_versions (
			id, scope_provider, scope_model, scope_user_path, scope_key, version, active, managed_default, name, description, workflow_payload, workflow_hash, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		version.ID,
		nullIfEmpty(version.Scope.Provider),
		nullIfEmpty(version.Scope.Model),
		nullIfEmpty(version.Scope.UserPath),
		version.ScopeKey,
		version.Version,
		version.Active,
		version.Managed,
		version.Name,
		version.Description,
		payloadJSON,
		version.WorkflowHash,
		version.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("insert workflow version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit workflow version: %w", err)
	}
	return version, nil
}

func (s *PostgreSQLStore) Deactivate(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_versions
		SET active = FALSE
		WHERE id::text = $1 AND active = TRUE
	`, id)
	if err != nil {
		return fmt.Errorf("deactivate workflow version: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgreSQLStore) Close() error {
	return nil
}

func scanPostgreSQLVersion(scanner interface {
	Scan(dest ...any) error
}) (Version, error) {
	var (
		version       Version
		scopeProvider *string
		scopeModel    *string
		scopeUserPath *string
		payloadJSON   []byte
	)

	if err := scanner.Scan(
		&version.ID,
		&scopeProvider,
		&scopeModel,
		&scopeUserPath,
		&version.ScopeKey,
		&version.Version,
		&version.Active,
		&version.Managed,
		&version.Name,
		&version.Description,
		&payloadJSON,
		&version.WorkflowHash,
		&version.CreatedAt,
	); err != nil {
		return Version{}, err
	}

	if scopeProvider != nil {
		version.Scope.Provider = *scopeProvider
	}
	if scopeModel != nil {
		version.Scope.Model = *scopeModel
	}
	version.Scope.UserPath = storedScopeUserPath(version.ScopeKey, valueOrEmpty(scopeUserPath))
	if err := json.Unmarshal(payloadJSON, &version.Payload); err != nil {
		return Version{}, fmt.Errorf("decode workflow payload %q: %w", version.ID, err)
	}
	return version, nil
}

func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func isPostgreSQLUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
