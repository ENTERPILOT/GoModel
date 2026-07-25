package guardrails

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores guardrail definitions in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS guardrail_definitions (
		name TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		user_path TEXT,
		config ` + sqlx.TypeJSON + ` NOT NULL,
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_guardrail_definitions_type ON guardrail_definitions(type)`,
	`CREATE INDEX IF NOT EXISTS idx_guardrail_definitions_updated_at ON guardrail_definitions(updated_at DESC)`,
}

// sqlMigrations backfill columns added after the table's first release.
var sqlMigrations = []string{
	`ALTER TABLE guardrail_definitions ADD COLUMN user_path TEXT`,
}

const selectDefinitionColumns = `
	SELECT name, type, description, user_path, config, created_at, updated_at
	FROM guardrail_definitions
`

const upsertDefinitionSQL = `
	INSERT INTO guardrail_definitions (name, type, description, user_path, config, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(name) DO UPDATE SET
		type = excluded.type,
		description = excluded.description,
		user_path = excluded.user_path,
		config = excluded.config,
		updated_at = excluded.updated_at
`

// NewSQLStore creates the guardrail table and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema[0]); err != nil {
		return nil, fmt.Errorf("initialize guardrail definitions table: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db, sqlMigrations...); err != nil {
		return nil, fmt.Errorf("initialize guardrail definitions table: %w", err)
	}
	if err := db.Schema(ctx, sqlSchema[1:]...); err != nil {
		return nil, fmt.Errorf("initialize guardrail definitions table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) List(ctx context.Context) ([]Definition, error) {
	rows, err := s.db.Query(ctx, selectDefinitionColumns+`ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list guardrails: %w", err)
	}
	defer rows.Close()
	return collectDefinitions(rows, scanSQLDefinition)
}

func (s *SQLStore) Get(ctx context.Context, name string) (*Definition, error) {
	row := s.db.QueryRow(ctx, selectDefinitionColumns+`WHERE name = ?`, normalizeDefinitionName(name))
	definition, err := scanSQLDefinition(row)
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &definition, nil
}

func (s *SQLStore) Upsert(ctx context.Context, definition Definition) error {
	normalized, err := prepareDefinitionUpsert(definition, time.Now().UTC())
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, upsertDefinitionSQL, definitionUpsertArgs(normalized)...); err != nil {
		return fmt.Errorf("upsert guardrail: %w", err)
	}
	return nil
}

func (s *SQLStore) UpsertMany(ctx context.Context, definitions []Definition) error {
	if len(definitions) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return s.db.InTx(ctx, func(q sqlx.Querier) error {
		for _, definition := range definitions {
			normalized, err := prepareDefinitionUpsert(definition, now)
			if err != nil {
				return err
			}
			if _, err := q.Exec(ctx, upsertDefinitionSQL, definitionUpsertArgs(normalized)...); err != nil {
				return fmt.Errorf("upsert guardrail %q: %w", normalized.Name, err)
			}
		}
		return nil
	})
}

func (s *SQLStore) Delete(ctx context.Context, name string) error {
	affected, err := s.db.Exec(ctx,
		`DELETE FROM guardrail_definitions WHERE name = ?`, normalizeDefinitionName(name))
	if err != nil {
		return fmt.Errorf("delete guardrail: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Close() error {
	return nil
}

// prepareDefinitionUpsert validates a definition and stamps its timestamps.
// created_at is only set when absent, so a re-upsert keeps the original.
func prepareDefinitionUpsert(definition Definition, now time.Time) (Definition, error) {
	normalized, err := normalizeDefinition(definition)
	if err != nil {
		return Definition{}, err
	}
	stamp := time.Unix(now.Unix(), 0).UTC()
	if normalized.CreatedAt.IsZero() {
		normalized.CreatedAt = stamp
	}
	normalized.UpdatedAt = stamp
	return normalized, nil
}

func definitionUpsertArgs(definition Definition) []any {
	return []any{
		definition.Name,
		definition.Type,
		definition.Description,
		sqlutil.NullableString(definition.UserPath),
		string(definition.Config),
		definition.CreatedAt.Unix(),
		definition.UpdatedAt.Unix(),
	}
}

func scanSQLDefinition(scanner definitionScanner) (Definition, error) {
	var (
		definition    Definition
		userPath      *string
		configJSON    []byte
		createdAtUnix int64
		updatedAtUnix int64
	)
	if err := scanner.Scan(
		&definition.Name,
		&definition.Type,
		&definition.Description,
		&userPath,
		&configJSON,
		&createdAtUnix,
		&updatedAtUnix,
	); err != nil {
		return Definition{}, err
	}
	definition.UserPath = sqlutil.DerefTrimmed(userPath)
	definition.Config = configJSON
	definition.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	definition.UpdatedAt = time.Unix(updatedAtUnix, 0).UTC()
	return definition, nil
}
