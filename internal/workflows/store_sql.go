package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores immutable workflow versions in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

var sqlTable = `CREATE TABLE IF NOT EXISTS workflow_versions (
		id TEXT PRIMARY KEY,
		scope_provider TEXT,
		scope_model TEXT,
		scope_user_path TEXT,
		scope_key TEXT NOT NULL,
		version INTEGER NOT NULL,
		active ` + sqlx.TypeBool + ` NOT NULL DEFAULT TRUE,
		managed_default ` + sqlx.TypeBool + ` NOT NULL DEFAULT FALSE,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		workflow_payload ` + sqlx.TypeJSON + ` NOT NULL,
		workflow_hash TEXT NOT NULL,
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		CHECK (scope_provider IS NOT NULL OR scope_model IS NULL)
	)`

var sqlIndexes = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_versions_scope_version
		ON workflow_versions(scope_key, version)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_versions_active_scope
		ON workflow_versions(scope_key) WHERE active = TRUE`,
	`CREATE INDEX IF NOT EXISTS idx_workflow_versions_active_created_at
		ON workflow_versions(active, created_at DESC)`,
}

// sqlMigrations backfill columns added after the table's first release.
var sqlMigrations = []string{
	`ALTER TABLE workflow_versions ADD COLUMN scope_user_path TEXT`,
	`ALTER TABLE workflow_versions ADD COLUMN managed_default ` + sqlx.TypeBool + ` NOT NULL DEFAULT FALSE`,
}

const selectVersionColumns = `
	SELECT id, scope_provider, scope_model, scope_user_path, scope_key, version,
		active, managed_default, name, description, workflow_payload,
		workflow_hash, created_at
	FROM workflow_versions
`

const insertVersionSQL = `
	INSERT INTO workflow_versions (
		id, scope_provider, scope_model, scope_user_path, scope_key, version,
		active, managed_default, name, description, workflow_payload,
		workflow_hash, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`

// NewSQLStore creates the workflow table and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlTable); err != nil {
		return nil, fmt.Errorf("initialize workflow versions table: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db, sqlMigrations...); err != nil {
		return nil, fmt.Errorf("initialize workflow versions table: %w", err)
	}
	if err := migrateCreatedAtToUnixSeconds(ctx, db); err != nil {
		return nil, err
	}
	if err := db.Schema(ctx, sqlIndexes...); err != nil {
		return nil, fmt.Errorf("initialize workflow versions table: %w", err)
	}
	// Rows written before managed_default existed are recognised by the name
	// and description the managed default has always used.
	if _, err := db.Exec(ctx, `
		UPDATE workflow_versions
		SET managed_default = TRUE
		WHERE managed_default = FALSE
		  AND scope_key = 'global'
		  AND name = ?
		  AND description = ?
	`, ManagedDefaultGlobalName, ManagedDefaultGlobalDescription); err != nil {
		return nil, fmt.Errorf("initialize workflow versions table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) ListActive(ctx context.Context) ([]Version, error) {
	rows, err := s.db.Query(ctx, selectVersionColumns+`
		WHERE active = TRUE
		ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list active workflows: %w", err)
	}
	defer rows.Close()
	return collectVersions(rows, scanSQLVersion)
}

func (s *SQLStore) Get(ctx context.Context, id string) (*Version, error) {
	version, err := scanSQLVersion(s.db.QueryRow(ctx, selectVersionColumns+`WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &version, nil
}

func (s *SQLStore) Create(ctx context.Context, input CreateInput) (*Version, error) {
	input, scopeKey, workflowHash, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}

	var created *Version
	err = s.db.InTx(ctx, func(q sqlx.Querier) error {
		nextVersion, err := nextVersionFor(ctx, q, scopeKey)
		if err != nil {
			return err
		}
		if input.Activate {
			if _, err := q.Exec(ctx,
				`UPDATE workflow_versions SET active = FALSE WHERE scope_key = ? AND active = TRUE`,
				scopeKey,
			); err != nil {
				return fmt.Errorf("deactivate current workflow version: %w", err)
			}
		}
		created, err = insertVersion(ctx, q, Version{
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
			CreatedAt:    time.Now().UTC(),
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// EnsureManagedDefaultGlobal publishes the managed default workflow unless the
// active global version is already it, or was authored by an operator.
func (s *SQLStore) EnsureManagedDefaultGlobal(ctx context.Context, input CreateInput, workflowHash string) (*Version, error) {
	var created *Version
	err := s.db.InTx(ctx, func(q sqlx.Querier) error {
		active, hasActive, err := activeGlobalVersion(ctx, q)
		if err != nil {
			return err
		}
		if hasActive {
			// An operator-authored active version is never replaced.
			if !active.Managed {
				return nil
			}
			if strings.TrimSpace(active.Name) == input.Name &&
				strings.TrimSpace(active.Description) == input.Description &&
				strings.TrimSpace(active.WorkflowHash) == workflowHash {
				return nil
			}
		}

		nextVersion, err := nextVersionFor(ctx, q, "global")
		if err != nil {
			return err
		}
		if hasActive {
			if _, err := q.Exec(ctx,
				`UPDATE workflow_versions SET active = FALSE WHERE id = ? AND active = TRUE`,
				active.ID,
			); err != nil {
				return fmt.Errorf("deactivate current workflow version: %w", err)
			}
		}
		created, err = insertVersion(ctx, q, Version{
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
			CreatedAt:    time.Now().UTC(),
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *SQLStore) Deactivate(ctx context.Context, id string) error {
	affected, err := s.db.Exec(ctx, `
		UPDATE workflow_versions
		SET active = FALSE
		WHERE id = ? AND active = TRUE
	`, id)
	if err != nil {
		return fmt.Errorf("deactivate workflow version: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Close() error {
	return nil
}

// nextVersionFor allocates the next version number within a scope.
func nextVersionFor(ctx context.Context, q sqlx.Querier, scopeKey string) (int, error) {
	var next int
	err := q.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM workflow_versions WHERE scope_key = ?`,
		scopeKey,
	).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("select next workflow version: %w", err)
	}
	return next, nil
}

// activeGlobalVersion returns the active global version, if there is one.
func activeGlobalVersion(ctx context.Context, q sqlx.Querier) (Version, bool, error) {
	version, err := scanSQLVersion(q.QueryRow(ctx, selectVersionColumns+`
		WHERE scope_key = 'global' AND active = TRUE
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`))
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return Version{}, false, nil
		}
		return Version{}, false, fmt.Errorf("load active global workflow: %w", err)
	}
	return version, true, nil
}

func insertVersion(ctx context.Context, q sqlx.Querier, version Version) (*Version, error) {
	payloadJSON, err := json.Marshal(version.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshal workflow payload: %w", err)
	}
	_, err = q.Exec(ctx, insertVersionSQL,
		version.ID,
		sqlutil.NullableString(version.Scope.Provider),
		sqlutil.NullableString(version.Scope.Model),
		sqlutil.NullableString(version.Scope.UserPath),
		version.ScopeKey,
		version.Version,
		version.Active,
		version.Managed,
		version.Name,
		version.Description,
		string(payloadJSON),
		version.WorkflowHash,
		version.CreatedAt.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("insert workflow version: %w", err)
	}
	return &version, nil
}

func scanSQLVersion(scanner versionRowScanner) (Version, error) {
	var (
		version                                  Version
		scopeProvider, scopeModel, scopeUserPath *string
		payloadJSON                              []byte
		createdAtUnix                            int64
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
		&createdAtUnix,
	); err != nil {
		return Version{}, err
	}

	version.Scope = Scope{
		Provider: sqlutil.DerefTrimmed(scopeProvider),
		Model:    sqlutil.DerefTrimmed(scopeModel),
		UserPath: storedScopeUserPath(version.ScopeKey, sqlutil.DerefTrimmed(scopeUserPath)),
	}
	version.CreatedAt = sqlutil.TimeFromUnix(createdAtUnix)
	if err := json.Unmarshal(payloadJSON, &version.Payload); err != nil {
		return Version{}, fmt.Errorf("decode workflow payload %q: %w", version.ID, err)
	}
	return version, nil
}
