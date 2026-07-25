package failover

import (
	"context"
	"fmt"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// Migrating the pre-rename failover_rules table (source → primary_model,
// targets → fallback_models, description dropped) is the one part of this
// store that cannot be written once.
//
// PostgreSQL renames columns in place. SQLite historically could not, so its
// migration copies rows into a correctly-shaped table and swaps it in — and
// that copy is also where padded primary keys get trimmed, since the lookups
// trim their argument and would otherwise never match a padded row.
//
// Both paths are kept as they were rather than rewritten to a common form:
// they run against real databases in the field, where a subtle behaviour
// change would silently orphan rules.

func migrateLegacyRuleTable(ctx context.Context, db sqlx.DB) error {
	switch db.Dialect() {
	case sqlx.SQLite:
		return migrateSQLiteRuleTable(ctx, db)
	case sqlx.PostgreSQL:
		return migratePostgresRuleTable(ctx, db)
	default:
		return fmt.Errorf("unsupported dialect %q", db.Dialect())
	}
}

func migrateSQLiteRuleTable(ctx context.Context, db sqlx.DB) error {
	columns, err := sqliteRuleColumns(ctx, db)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil
	}
	if !columns["source"] && !columns["targets"] && !columns["description"] {
		return nil
	}

	// Read each value from whichever column name this database still uses,
	// defaulting the ones a very old table never had.
	pick := func(preferred, legacy string) string {
		if columns[preferred] {
			return preferred
		}
		return legacy
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
	primaryExpr := pick("primary_model", "source")
	targetsExpr := pick("fallback_models", "targets")

	return db.InTx(ctx, func(q sqlx.Querier) error {
		if _, err := q.Exec(ctx, `
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
		copySQL := fmt.Sprintf(`
			INSERT OR REPLACE INTO failover_rules_migrated (
				primary_model, fallback_models, enabled, managed_source, created_at, updated_at
			)
			SELECT TRIM(%s), %s, %s, %s, %s, %s
			FROM failover_rules
			WHERE TRIM(COALESCE(%s, '')) <> ''
		`, primaryExpr, targetsExpr, enabledExpr, managedSourceExpr, createdAtExpr, updatedAtExpr, primaryExpr)
		if _, err := q.Exec(ctx, copySQL); err != nil {
			return fmt.Errorf("copy failover_rules rows into migrated table: %w", err)
		}
		for _, stmt := range []string{
			`DROP TABLE failover_rules`,
			`ALTER TABLE failover_rules_migrated RENAME TO failover_rules`,
		} {
			if _, err := q.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("replace failover_rules table: %w", err)
			}
		}
		return nil
	})
}

func sqliteRuleColumns(ctx context.Context, db sqlx.DB) (map[string]bool, error) {
	rows, err := db.Query(ctx, `PRAGMA table_info('failover_rules')`)
	if err != nil {
		return nil, fmt.Errorf("inspect failover_rules columns: %w", err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
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

func migratePostgresRuleTable(ctx context.Context, db sqlx.DB) error {
	// The trim runs whenever primary_model exists, independently of the
	// rename, so rules migrated by an earlier non-trimming version stay
	// reachable by the trim-normalizing Get and Delete lookups.
	const migration = `
		DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'source'
			) AND NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'primary_model'
			) THEN
				ALTER TABLE failover_rules RENAME COLUMN source TO primary_model;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'primary_model'
			) THEN
				UPDATE failover_rules
				SET primary_model = btrim(primary_model)
				WHERE primary_model <> btrim(primary_model);
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'targets'
			) AND NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'fallback_models'
			) THEN
				ALTER TABLE failover_rules RENAME COLUMN targets TO fallback_models;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'description'
			) THEN
				ALTER TABLE failover_rules DROP COLUMN description;
			END IF;
		END $$;
	`
	if _, err := db.Exec(ctx, migration); err != nil {
		return fmt.Errorf("failed to migrate failover_rules table: %w", err)
	}
	return nil
}
