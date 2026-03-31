package executionplans

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SQLiteStore stores immutable execution-plan versions in SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates the execution-plan table and indexes if needed.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}

	statements := []string{
		`CREATE TABLE IF NOT EXISTS execution_plan_versions (
			id TEXT PRIMARY KEY,
			scope_provider TEXT,
			scope_model TEXT,
			scope_user_path TEXT,
			scope_key TEXT NOT NULL,
			version INTEGER NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			plan_payload JSON NOT NULL,
			plan_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			CHECK (scope_provider IS NOT NULL OR scope_model IS NULL)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_execution_plan_versions_scope_version
			ON execution_plan_versions(scope_key, version)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_execution_plan_versions_active_scope
			ON execution_plan_versions(scope_key) WHERE active = 1`,
		`CREATE INDEX IF NOT EXISTS idx_execution_plan_versions_active_created_at
			ON execution_plan_versions(active, created_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return nil, fmt.Errorf("initialize execution plan versions table: %w", err)
		}
	}
	if _, err := db.Exec(`ALTER TABLE execution_plan_versions ADD COLUMN scope_user_path TEXT`); err != nil && !isSQLiteDuplicateColumnError(err) {
		return nil, fmt.Errorf("initialize execution plan versions table: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func isSQLiteDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate column") || strings.Contains(message, "already exists")
}

func (s *SQLiteStore) ListActive(ctx context.Context) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, scope_provider, scope_model, scope_user_path, scope_key, version, active, name, description, plan_payload, plan_hash, created_at
		FROM execution_plan_versions
		WHERE active = 1
		ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list active execution plans: %w", err)
	}
	defer rows.Close()
	return collectVersions(rows, func(scanner versionRowScanner) (Version, error) {
		return scanSQLiteVersion(scanner)
	})
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (*Version, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, scope_provider, scope_model, scope_user_path, scope_key, version, active, name, description, plan_payload, plan_hash, created_at
		FROM execution_plan_versions
		WHERE id = ?
	`, id)
	version, err := scanSQLiteVersion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &version, nil
}

func (s *SQLiteStore) Create(ctx context.Context, input CreateInput) (*Version, error) {
	input, scopeKey, planHash, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire execution plan connection: %w", err)
	}
	defer func() {
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, fmt.Errorf("begin execution plan transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
	}()

	var nextVersion int
	if err := conn.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM execution_plan_versions WHERE scope_key = ?`,
		scopeKey,
	).Scan(&nextVersion); err != nil {
		return nil, fmt.Errorf("select next execution plan version: %w", err)
	}

	if input.Activate {
		if _, err := conn.ExecContext(ctx,
			`UPDATE execution_plan_versions SET active = 0 WHERE scope_key = ? AND active = 1`,
			scopeKey,
		); err != nil {
			return nil, fmt.Errorf("deactivate current execution plan version: %w", err)
		}
	}

	payloadJSON, err := json.Marshal(input.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshal execution plan payload: %w", err)
	}

	now := time.Now().UTC()
	version := &Version{
		ID:          uuid.NewString(),
		Scope:       input.Scope,
		ScopeKey:    scopeKey,
		Version:     nextVersion,
		Active:      input.Activate,
		Name:        input.Name,
		Description: input.Description,
		Payload:     input.Payload,
		PlanHash:    planHash,
		CreatedAt:   now,
	}

	if _, err := conn.ExecContext(ctx, `
		INSERT INTO execution_plan_versions (
			id, scope_provider, scope_model, scope_user_path, scope_key, version, active, name, description, plan_payload, plan_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		version.ID,
		nullableString(version.Scope.Provider),
		nullableString(version.Scope.Model),
		nullableString(version.Scope.UserPath),
		version.ScopeKey,
		version.Version,
		boolToSQLite(version.Active),
		version.Name,
		version.Description,
		string(payloadJSON),
		version.PlanHash,
		version.CreatedAt.Unix(),
	); err != nil {
		return nil, fmt.Errorf("insert execution plan version: %w", err)
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, fmt.Errorf("commit execution plan version: %w", err)
	}
	committed = true
	return version, nil
}

func (s *SQLiteStore) Deactivate(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE execution_plan_versions
		SET active = 0
		WHERE id = ? AND active = 1
	`, id)
	if err != nil {
		return fmt.Errorf("deactivate execution plan version: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("deactivate execution plan version rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return nil
}

func scanSQLiteVersion(scanner interface {
	Scan(dest ...any) error
}) (Version, error) {
	var (
		version       Version
		scopeProvider sql.NullString
		scopeModel    sql.NullString
		scopeUserPath sql.NullString
		active        int
		payloadJSON   string
		createdAtUnix int64
	)

	if err := scanner.Scan(
		&version.ID,
		&scopeProvider,
		&scopeModel,
		&scopeUserPath,
		&version.ScopeKey,
		&version.Version,
		&active,
		&version.Name,
		&version.Description,
		&payloadJSON,
		&version.PlanHash,
		&createdAtUnix,
	); err != nil {
		return Version{}, err
	}

	version.Scope = Scope{
		Provider: scopeProvider.String,
		Model:    scopeModel.String,
		UserPath: storedScopeUserPath(version.ScopeKey, scopeUserPath.String),
	}
	version.Active = active != 0
	version.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	if err := json.Unmarshal([]byte(payloadJSON), &version.Payload); err != nil {
		return Version{}, fmt.Errorf("decode execution plan payload %q: %w", version.ID, err)
	}
	return version, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolToSQLite(value bool) int {
	if value {
		return 1
	}
	return 0
}
