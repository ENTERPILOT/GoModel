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
	err = db.Schema(ctx, `CREATE INDEX IF NOT EXISTS idx_locked_note ON locked(note)`)
	require.ErrorContains(t, err, "could not lock its table")

	// Nothing was left half-applied, and both go through once the table is free.
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
