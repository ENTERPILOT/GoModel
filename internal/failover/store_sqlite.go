package failover

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS failover_rules (
			primary_model TEXT PRIMARY KEY,
			fallback_models TEXT NOT NULL DEFAULT '[]',
			enabled INTEGER NOT NULL DEFAULT 1,
			managed_source TEXT NOT NULL DEFAULT 'dashboard',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`); err != nil {
		return nil, fmt.Errorf("failed to create failover_rules table: %w", err)
	}
	if err := migrateSQLiteFailoverRules(db); err != nil {
		return nil, err
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_failover_rules_enabled ON failover_rules(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_failover_rules_updated_at ON failover_rules(updated_at DESC)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("failed to create failover_rules index: %w", err)
		}
	}
	return &SQLiteStore{db: db}, nil
}

func migrateSQLiteFailoverRules(db *sql.DB) error {
	columns, err := sqliteFailoverRuleColumns(db)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil
	}
	needsMigration := columns["source"] || columns["targets"] || columns["description"]
	if !needsMigration {
		return nil
	}
	primaryExpr := "primary_model"
	if !columns["primary_model"] {
		primaryExpr = "source"
	}
	targetsExpr := "fallback_models"
	if !columns["fallback_models"] {
		targetsExpr = "targets"
	}
	enabledExpr := "1"
	if columns["enabled"] {
		enabledExpr = "enabled"
	}
	managedSourceExpr := "'dashboard'"
	if columns["managed_source"] {
		managedSourceExpr = "managed_source"
	}
	createdAtExpr := "strftime('%s', 'now')"
	if columns["created_at"] {
		createdAtExpr = "created_at"
	}
	updatedAtExpr := "strftime('%s', 'now')"
	if columns["updated_at"] {
		updatedAtExpr = "updated_at"
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin failover_rules migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
		CREATE TABLE failover_rules_migrated (
			primary_model TEXT PRIMARY KEY,
			fallback_models TEXT NOT NULL DEFAULT '[]',
			enabled INTEGER NOT NULL DEFAULT 1,
			managed_source TEXT NOT NULL DEFAULT 'dashboard',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migrated failover_rules table: %w", err)
	}
	insertSQL := fmt.Sprintf(`
		INSERT OR REPLACE INTO failover_rules_migrated (
			primary_model, fallback_models, enabled, managed_source, created_at, updated_at
		)
		SELECT
			TRIM(%s),
			%s,
			%s,
			%s,
			%s,
			%s
		FROM failover_rules
		WHERE TRIM(COALESCE(%s, '')) <> ''
	`, primaryExpr, targetsExpr, enabledExpr, managedSourceExpr, createdAtExpr, updatedAtExpr, primaryExpr)
	if _, err := tx.Exec(insertSQL); err != nil {
		return fmt.Errorf("copy failover_rules rows into migrated table: %w", err)
	}
	for _, stmt := range []string{
		`DROP TABLE failover_rules`,
		`ALTER TABLE failover_rules_migrated RENAME TO failover_rules`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("replace failover_rules table: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failover_rules migration: %w", err)
	}
	return nil
}

func sqliteFailoverRuleColumns(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info('failover_rules')`)
	if err != nil {
		return nil, fmt.Errorf("inspect failover_rules columns: %w", err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("scan failover_rules column: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate failover_rules columns: %w", err)
	}
	return columns, nil
}

func (s *SQLiteStore) List(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT primary_model, fallback_models, enabled, managed_source, created_at, updated_at
		FROM failover_rules
		ORDER BY primary_model ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list failover mappings: %w", err)
	}
	defer rows.Close()
	return collectRules(func() (Rule, bool, error) {
		if !rows.Next() {
			return Rule{}, false, nil
		}
		rule, err := scanSQLiteRule(rows)
		return rule, true, err
	}, rows.Err)
}

func (s *SQLiteStore) Get(ctx context.Context, source string) (*Rule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT primary_model, fallback_models, enabled, managed_source, created_at, updated_at
		FROM failover_rules
		WHERE primary_model = ?
	`, strings.TrimSpace(source))
	rule, err := scanSQLiteRule(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &rule, nil
}

const sqliteUpsertRuleSQL = `
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

func sqliteUpsertArgs(rule Rule) ([]any, error) {
	stampUpsert(&rule)
	targetsJSON, err := encodeTargets(rule.Targets)
	if err != nil {
		return nil, err
	}
	return []any{
		strings.TrimSpace(rule.Source),
		targetsJSON,
		boolToSQLite(rule.Enabled),
		rule.ManagedSource,
		rule.CreatedAt.Unix(),
		rule.UpdatedAt.Unix(),
	}, nil
}

func (s *SQLiteStore) Upsert(ctx context.Context, rule Rule) error {
	args, err := sqliteUpsertArgs(rule)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, sqliteUpsertRuleSQL, args...); err != nil {
		return fmt.Errorf("upsert failover mapping: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, source string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM failover_rules WHERE primary_model = ?`, strings.TrimSpace(source))
	if err != nil {
		return fmt.Errorf("delete failover mapping: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delete rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) DeleteAll(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM failover_rules`); err != nil {
		return fmt.Errorf("delete failover mappings: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Close() error { return nil }

func scanSQLiteRule(scanner interface{ Scan(dest ...any) error }) (Rule, error) {
	var rule Rule
	var targets string
	var enabled int
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&rule.Source,
		&targets,
		&enabled,
		&rule.ManagedSource,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Rule{}, err
	}
	var err error
	if rule.Targets, err = decodeTargets([]byte(targets)); err != nil {
		return Rule{}, err
	}
	rule.Enabled = enabled != 0
	rule.CreatedAt = time.Unix(createdAt, 0).UTC()
	rule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rule, nil
}

func boolToSQLite(v bool) int {
	if v {
		return 1
	}
	return 0
}
