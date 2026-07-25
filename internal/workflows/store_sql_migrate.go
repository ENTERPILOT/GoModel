package workflows

import (
	"context"
	"fmt"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// migrateCreatedAtToUnixSeconds converts a PostgreSQL workflow_versions
// created_at column from TIMESTAMPTZ to BIGINT unix seconds.
//
// The two hand-written stores disagreed on how to store this one column:
// SQLite kept unix seconds in an INTEGER, PostgreSQL kept a TIMESTAMPTZ and
// bound a time.Time. Every other table in this schema uses unix seconds on
// both engines, so the unified store does too, and an existing PostgreSQL
// table is converted in place rather than left with a column the shared scan
// cannot read.
//
// EXTRACT(EPOCH FROM ...) yields the same instant, so timestamps survive the
// conversion. The table holds one row per published workflow version, so the
// rewrite is small.
func migrateCreatedAtToUnixSeconds(ctx context.Context, db sqlx.DB) error {
	if db.Dialect() != sqlx.PostgreSQL {
		return nil
	}
	const migration = `
		DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = 'workflow_versions'
				  AND column_name = 'created_at'
				  AND data_type = 'timestamp with time zone'
			) THEN
				ALTER TABLE workflow_versions
					ALTER COLUMN created_at TYPE BIGINT
					USING EXTRACT(EPOCH FROM created_at)::bigint;
			END IF;
		END $$;
	`
	if _, err := db.Exec(ctx, migration); err != nil {
		return fmt.Errorf("migrate workflow_versions created_at: %w", err)
	}
	return nil
}
