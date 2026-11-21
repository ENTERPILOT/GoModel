package sqlx_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

const lockedTable = `CREATE TABLE IF NOT EXISTS locked (id TEXT PRIMARY KEY, model TEXT)`

// newLockedTable returns a PostgreSQL DB whose `locked` table has a row
// inserted by a transaction that stays open until release is called: the
// state of a table under a long-running write on another replica.
func newLockedTable(t *testing.T) (db sqlx.DB, release func()) {
	t.Helper()
	pool := sqlxtest.NewPostgresPool(t)
	if pool == nil {
		return nil, nil
	}
	db, err := sqlx.NewPostgreSQL(pool)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Schema(ctx, lockedTable, `CREATE INDEX IF NOT EXISTS idx_locked_model ON locked(model)`))
	require.NoError(t, sqlx.AddColumns(ctx, db, `ALTER TABLE locked ADD COLUMN note TEXT`))

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO locked (id) VALUES ('held')`)
	require.NoError(t, err)
	var once sync.Once
	release = func() {
		once.Do(func() { assert.NoError(t, tx.Rollback(ctx)) })
	}
	t.Cleanup(release)
	return db, release
}

// A replica starting against an up-to-date schema must not queue for a table
// lock: the statements waiting behind it would stall every other replica.
func TestAppliedSchemaDoesNotWaitForTableLock(t *testing.T) {
	db, _ := newLockedTable(t)
	if db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	started := time.Now()
	require.NoError(t, db.Schema(ctx, lockedTable, `CREATE INDEX IF NOT EXISTS idx_locked_model ON locked(model)`))
	require.NoError(t, sqlx.AddColumns(ctx, db,
		`ALTER TABLE locked ADD COLUMN note TEXT`,
		`ALTER TABLE locked ADD COLUMN IF NOT EXISTS model TEXT`,
	))
	assert.Less(t, time.Since(started), 2*time.Second)
}

func TestSchemaChangeGivesUpOnAHeldTableLock(t *testing.T) {
	db, release := newLockedTable(t)
	if db == nil {
		return
	}
	sqlx.SetLockWait(db, 50*time.Millisecond, 10*time.Millisecond, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := sqlx.AddColumns(ctx, db, `ALTER TABLE locked ADD COLUMN enabled `+sqlx.TypeBool+` NOT NULL DEFAULT FALSE`)
	require.ErrorContains(t, err, "could not lock its table")
	require.ErrorIs(t, err, sqlx.ErrTableLocked)
	err = db.Schema(ctx, `CREATE INDEX IF NOT EXISTS idx_locked_note ON locked(note)`)
	require.ErrorContains(t, err, "could not lock its table")
	require.ErrorIs(t, err, sqlx.ErrTableLocked)

	// The failed change left nothing behind: read from the catalog, which
	// needs no lock on the table, while it is still held.
	columns, err := sqlx.Columns(ctx, db, "locked")
	require.NoError(t, err)
	require.NotContains(t, columns, "enabled")

	// Both go through once the table is free.
	release()
	require.NoError(t, sqlx.AddColumns(ctx, db, `ALTER TABLE locked ADD COLUMN enabled `+sqlx.TypeBool+` NOT NULL DEFAULT FALSE`))
	require.NoError(t, db.Schema(ctx, `CREATE INDEX IF NOT EXISTS idx_locked_note ON locked(note)`))
	_, err = db.Exec(ctx, `INSERT INTO locked (id, enabled, note) VALUES (?, ?, ?)`, "a", true, "n")
	require.NoError(t, err)
}

func TestSchemaChangeRetriesUntilTheTableLockIsFree(t *testing.T) {
	db, release := newLockedTable(t)
	if db == nil {
		return
	}
	sqlx.SetLockWait(db, 50*time.Millisecond, 10*time.Millisecond, 200)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	time.AfterFunc(300*time.Millisecond, release)
	require.NoError(t, sqlx.AddColumns(ctx, db, `ALTER TABLE locked ADD COLUMN enabled `+sqlx.TypeBool+` NOT NULL DEFAULT FALSE`))
	_, err := db.Exec(ctx, `INSERT INTO locked (id, enabled) VALUES (?, ?)`, "a", true)
	require.NoError(t, err)
}

// Two replicas start during a migration. One applies it while the other waits
// its turn; a long write then takes the table. The second replica must see
// that there is nothing left to do rather than queue for the table, stall the
// writes behind it and fail its start on a change that is already applied.
func TestSchemaChangeRechecksAfterWaitingForAnotherReplica(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		apply     func(ctx context.Context, db sqlx.DB, statement string) error
	}{
		{
			name:      "index",
			statement: `CREATE INDEX IF NOT EXISTS idx_locked_note ON locked(note)`,
			apply:     func(ctx context.Context, db sqlx.DB, statement string) error { return db.Schema(ctx, statement) },
		},
		{
			name:      "column",
			statement: `ALTER TABLE locked ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT FALSE`,
			apply: func(ctx context.Context, db sqlx.DB, statement string) error {
				return sqlx.AddColumns(ctx, db, statement)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := sqlxtest.NewPostgresPool(t)
			if pool == nil {
				return
			}
			db, err := sqlx.NewPostgreSQL(pool)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, db.Schema(ctx, lockedTable))
			require.NoError(t, sqlx.AddColumns(ctx, db, `ALTER TABLE locked ADD COLUMN note TEXT`))
			sqlx.SetLockWait(db, 50*time.Millisecond, 10*time.Millisecond, 3)
			waiting := func(query string, args ...any) func() bool {
				return func() bool {
					var count int
					err := pool.QueryRow(ctx, query, args...).Scan(&count)
					return err == nil && count > 0
				}
			}

			// The other replica holds the schema lock, and this one queues.
			other, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = other.Rollback(context.Background()) }()
			_, err = other.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, sqlx.SchemaLockKey)
			require.NoError(t, err)
			applied := make(chan error, 1)
			go func() { applied <- tt.apply(ctx, db, tt.statement) }()
			require.Eventually(t, waiting(
				`SELECT count(*) FROM pg_locks
					WHERE locktype = 'advisory' AND NOT granted
						AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
						AND classid = ($1::bigint >> 32) AND objid = ($1::bigint & 4294967295) AND objsubid = 1`,
				sqlx.SchemaLockKey), 10*time.Second, 10*time.Millisecond, "this replica waits for the schema lock")

			// The other replica applies the change. A write queues for the
			// table behind it, and holds the table from the moment it commits.
			_, err = other.Exec(ctx, tt.statement)
			require.NoError(t, err)
			writer, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = writer.Rollback(context.Background()) }()
			wrote := make(chan error, 1)
			go func() {
				_, err := writer.Exec(ctx, `INSERT INTO locked (id) VALUES ('held')`)
				wrote <- err
			}()
			require.Eventually(t, waiting(
				`SELECT count(*) FROM pg_locks WHERE relation = 'locked'::regclass AND NOT granted`),
				10*time.Second, 10*time.Millisecond, "the write waits for the table")
			require.NoError(t, other.Commit(ctx))
			require.NoError(t, <-wrote)

			require.NoError(t, <-applied, "a change another replica applied meanwhile is not attempted again")
		})
	}
}
