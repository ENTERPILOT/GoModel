package failover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores failover rules in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

var sqlTable = `CREATE TABLE IF NOT EXISTS failover_rules (
	primary_model TEXT PRIMARY KEY,
	fallback_models ` + sqlx.TypeJSONText + ` NOT NULL DEFAULT '[]',
	enabled ` + sqlx.TypeBool + ` NOT NULL DEFAULT TRUE,
	managed_source TEXT NOT NULL DEFAULT 'dashboard',
	created_at ` + sqlx.TypeInt64 + ` NOT NULL,
	updated_at ` + sqlx.TypeInt64 + ` NOT NULL
)`

var sqlIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_failover_rules_enabled ON failover_rules(enabled)`,
	`CREATE INDEX IF NOT EXISTS idx_failover_rules_updated_at ON failover_rules(updated_at DESC)`,
}

const selectRuleColumns = `
	SELECT primary_model, fallback_models, enabled, managed_source, created_at, updated_at
	FROM failover_rules
`

const upsertRuleSQL = `
	INSERT INTO failover_rules (
		primary_model, fallback_models, enabled, managed_source, created_at, updated_at
	)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(primary_model) DO UPDATE SET
		fallback_models = excluded.fallback_models,
		enabled = excluded.enabled,
		managed_source = excluded.managed_source,
		updated_at = excluded.updated_at
`

// NewSQLStore creates the failover_rules table and indexes if needed, after
// migrating any pre-rename table shape.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	// Create first, then migrate: on a fresh database the create wins and the
	// migration finds nothing to reshape; on an existing one the create is a
	// no-op and the migration renames the legacy columns. This is the order
	// both hand-written stores used.
	if err := db.Schema(ctx, sqlTable); err != nil {
		return nil, fmt.Errorf("failed to create failover_rules table: %w", err)
	}
	if err := migrateLegacyRuleTable(ctx, db); err != nil {
		return nil, err
	}
	if err := db.Schema(ctx, sqlIndexes...); err != nil {
		return nil, fmt.Errorf("failed to create failover_rules index: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) List(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.Query(ctx, selectRuleColumns+`ORDER BY primary_model ASC`)
	if err != nil {
		return nil, fmt.Errorf("list failover mappings: %w", err)
	}
	defer rows.Close()
	return collectRules(func() (Rule, bool, error) {
		if !rows.Next() {
			return Rule{}, false, nil
		}
		rule, err := scanSQLRule(rows)
		return rule, true, err
	}, rows.Err)
}

func (s *SQLStore) Get(ctx context.Context, source string) (*Rule, error) {
	row := s.db.QueryRow(ctx, selectRuleColumns+`WHERE primary_model = ?`, strings.TrimSpace(source))
	rule, err := scanSQLRule(row)
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &rule, nil
}

func (s *SQLStore) Upsert(ctx context.Context, rule Rule) error {
	args, err := ruleUpsertArgs(rule)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, upsertRuleSQL, args...); err != nil {
		return fmt.Errorf("upsert failover mapping: %w", err)
	}
	return nil
}

func (s *SQLStore) Delete(ctx context.Context, source string) error {
	affected, err := s.db.Exec(ctx,
		`DELETE FROM failover_rules WHERE primary_model = ?`, strings.TrimSpace(source))
	if err != nil {
		return fmt.Errorf("delete failover mapping: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) DeleteAll(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM failover_rules`); err != nil {
		return fmt.Errorf("delete failover mappings: %w", err)
	}
	return nil
}

func (s *SQLStore) Close() error { return nil }

func ruleUpsertArgs(rule Rule) ([]any, error) {
	stampUpsert(&rule)
	targetsJSON, err := encodeTargets(rule.Targets)
	if err != nil {
		return nil, err
	}
	return []any{
		strings.TrimSpace(rule.Source),
		targetsJSON,
		rule.Enabled,
		rule.ManagedSource,
		rule.CreatedAt.Unix(),
		rule.UpdatedAt.Unix(),
	}, nil
}

func scanSQLRule(scanner sqlx.Row) (Rule, error) {
	var rule Rule
	var targets []byte
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&rule.Source,
		&targets,
		&rule.Enabled,
		&rule.ManagedSource,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Rule{}, err
	}
	var err error
	if rule.Targets, err = decodeTargets(targets); err != nil {
		return Rule{}, err
	}
	rule.CreatedAt = time.Unix(createdAt, 0).UTC()
	rule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rule, nil
}
